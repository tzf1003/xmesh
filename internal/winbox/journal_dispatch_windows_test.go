package winbox

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type recoveryTestGuard func() error

func (f recoveryTestGuard) Close() error { return f() }

// These fixtures launch no sandbox and grant no filesystem objects. Cleanup can
// delete their exact profile directly even after a test intentionally retains a
// damaged journal; no execution or ACL obligations can remain.
func dispatchProfile(t *testing.T, directory, name string, complete bool) journalRecord {
	t.Helper()
	j, err := openBoundGrantJournal(context.Background(), filepath.Join(directory, name), true)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	p, err := newProfile(j)
	if err != nil {
		t.Fatal(err)
	}
	record := j.records[0]
	if complete {
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		windows.FreeSid(p.sid)
		p.sid = nil
		t.Cleanup(func() {
			result, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(p.name)))
			if uint32(result) != 0x80070002 && uint32(result) != 0x80070490 {
				if err := hresult("delete dispatch fixture profile", result); err != nil {
					t.Error(err)
				}
			}
		})
	}
	return record
}

func Test_WindowsAppContainerJournalRecoverySweep(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	state := filepath.Join(t.TempDir(), "state")
	privateJournalDirectory(t, state)
	pending := dispatchProfile(t, state, "pending.journal", false)
	dispatchProfile(t, state, "complete.journal", true)
	busy, err := openBoundGrantJournal(context.Background(), filepath.Join(state, "busy.journal"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if _, err := busy.append(journalProfile()); err != nil {
		t.Fatal(err)
	}
	damagedPath := filepath.Join(state, "damaged.journal")
	damaged, err := openBoundGrantJournal(context.Background(), damagedPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := damaged.append(journalProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := damaged.file.WriteAt([]byte{1, 2, 3}, damaged.bytes); err != nil {
		t.Fatal(err)
	}
	if err := damaged.file.Sync(); err != nil {
		t.Fatal(err)
	}
	damaged.Close()
	before, err := os.ReadFile(damagedPath)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := openBoundGrantJournal(context.Background(), filepath.Join(state, "empty.journal"), true)
	if err != nil {
		t.Fatal(err)
	}
	empty.Close()
	prepared, closed := 0, 0
	report, err := recoverJournalDirectory(context.Background(), state, 8, 32, func(ctx context.Context, info journalRecoveryInfo) (*journalRecoveryLease, error) {
		prepared++
		if info.Profile != pending.Profile || info.SID != pending.SID || info.Workspace != nil || len(info.Objects) != 0 || len(info.Volumes) != 0 {
			t.Fatal("unexpected detached recovery manifest", info)
		}
		if err := os.Rename(state, state+".moved"); err == nil {
			t.Fatal("sweep lost its namespace binding")
		}
		return &journalRecoveryLease{Guard: recoveryTestGuard(func() error { closed++; return nil })}, nil
	})
	if !errors.Is(err, errJournalRecoveryBusy) || !errors.Is(err, errJournalIncomplete) || prepared != 1 || closed != 1 || len(report) != 5 {
		t.Fatal("mixed sweep result", report, err, prepared, closed)
	}
	expected := map[string]string{"pending.journal": "recovered", "complete.journal": "complete", "busy.journal": "busy", "damaged.journal": "retained", "empty.journal": "retained"}
	for _, result := range report {
		if result.State != expected[result.Name] || (result.Err != nil) != (result.State == "busy" || result.State == "retained") {
			t.Fatal("incorrect per-journal result", result)
		}
	}
	after, err := os.ReadFile(damagedPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("damaged journal was changed", err)
	}
	if len(busy.records) != 1 {
		t.Fatal("busy journal was changed")
	}
	busy.Close()
	for _, name := range []string{"busy.journal", "damaged.journal", "empty.journal"} {
		if err := os.Remove(filepath.Join(state, name)); err != nil {
			t.Fatal(err)
		}
	}
	report, err = recoverJournalDirectory(context.Background(), state, 8, 32, func(context.Context, journalRecoveryInfo) (*journalRecoveryLease, error) {
		t.Fatal("completed journal requested process coordination")
		return nil, nil
	})
	if err != nil || len(report) != 2 {
		t.Fatal("repeat completed sweep", report, err)
	}
	for _, result := range report {
		if result.State != "complete" {
			t.Fatal(result)
		}
	}
	if err := os.Rename(state, state+".moved"); err != nil {
		t.Fatal("sweep retained directory handles", err)
	}
}

func Test_WindowsAppContainerJournalRecoveryPreflight(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, mode := range []string{"limit", "foreign", "directory", "reparse", "hardlink", "duplicate", "cancelled", "coordinator-error", "missing-guard", "cancel-after-prepare", "guard-close-error"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			state := filepath.Join(base, "state")
			privateJournalDirectory(t, state)
			dispatchProfile(t, state, "pending.journal", false)
			path := filepath.Join(state, "pending.journal")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			limit := 8
			switch mode {
			case "limit":
				dispatchProfile(t, state, "second.journal", false)
				limit = 1
			case "foreign":
				if err := os.WriteFile(filepath.Join(state, "foreign.txt"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(state, "child.journal"), 0700); err != nil {
					t.Fatal(err)
				}
			case "reparse":
				if err := os.Symlink(base, filepath.Join(state, "alias.journal")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(base, "alias")); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				copy, err := openBoundGrantJournal(context.Background(), filepath.Join(state, "copy.journal"), true)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := copy.file.WriteAt(before, 0); err != nil {
					t.Fatal(err)
				}
				if err := copy.file.Sync(); err != nil {
					t.Fatal(err)
				}
				copy.Close()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			prepared, closed := 0, 0
			sentinel := errors.New("coordinator fixture failure")
			report, err := recoverJournalDirectory(ctx, state, limit, 32, func(context.Context, journalRecoveryInfo) (*journalRecoveryLease, error) {
				prepared++
				lease := &journalRecoveryLease{Guard: recoveryTestGuard(func() error {
					closed++
					if mode == "guard-close-error" {
						return sentinel
					}
					return nil
				})}
				if mode == "coordinator-error" {
					return lease, sentinel
				}
				if mode == "missing-guard" {
					return &journalRecoveryLease{}, nil
				}
				if mode == "cancel-after-prepare" {
					cancel()
					return lease, nil
				}
				if mode == "guard-close-error" {
					return lease, nil
				}
				t.Fatal("preflight failure invoked coordinator", mode)
				return nil, nil
			})
			if err == nil {
				t.Fatal("invalid recovery preflight succeeded", mode, report)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "guard-close-error" {
				if prepared != 1 || closed != 1 || len(report) != 1 || report[0].State != "recovered" || !errors.Is(err, sentinel) || bytes.Equal(before, after) {
					t.Fatal("durable recovery/guard close result", report, err)
				}
			} else {
				if !bytes.Equal(before, after) {
					t.Fatal("failed preflight changed journal", mode)
				}
				wantPrepare := mode == "coordinator-error" || mode == "missing-guard" || mode == "cancel-after-prepare"
				if (prepared == 1) != wantPrepare || closed != prepared && mode != "missing-guard" {
					t.Fatal("coordinator guard lifecycle", prepared, closed)
				}
				if mode == "cancel-after-prepare" && !errors.Is(err, context.Canceled) {
					t.Fatal("lost cancellation", err)
				}
			}
		})
	}
}

func Test_WindowsAppContainerJournalRecoveryCreationCoordination(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	state := filepath.Join(t.TempDir(), "state")
	privateJournalDirectory(t, state)
	dispatchProfile(t, state, "pending.journal", false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var child *exec.Cmd
	var childDone chan error
	defer func() {
		if child != nil && child.Process != nil {
			_ = child.Process.Kill()
		}
	}()
	_, err := recoverJournalDirectory(ctx, state, 8, 32, func(context.Context, journalRecoveryInfo) (*journalRecoveryLease, error) {
		blockedCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		defer stop()
		blocked := make(chan error, 1)
		go func() {
			j, err := openBoundGrantJournal(blockedCtx, filepath.Join(state, "cancelled.journal"), true)
			if j != nil {
				j.Close()
			}
			blocked <- err
		}()
		select {
		case err := <-blocked:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("creation ignored sweep cancellation", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if _, err := os.Stat(filepath.Join(state, "cancelled.journal")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("cancelled waiter created a file", err)
		}
		child = exec.CommandContext(ctx, os.Args[0], "-test.run=^Test_WindowsAppContainerJournalCreationHelper$")
		child.Env = append(os.Environ(), "XMESH_JOURNAL_CREATE_PATH="+filepath.Join(state, "child.journal"))
		stdout, err := child.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		ready := make(chan string, 1)
		go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
		select {
		case line := <-ready:
			if line != "attempting\n" && line != "attempting\r\n" {
				t.Fatal("creation helper readiness", line)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		childDone = make(chan error, 1)
		go func() { childDone <- child.Wait() }()
		select {
		case err := <-childDone:
			t.Fatal("cross-process creation bypassed sweep", err)
		case <-time.After(100 * time.Millisecond):
		}
		if _, err := os.Stat(filepath.Join(state, "child.journal")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("blocked helper created a journal", err)
		}
		return &journalRecoveryLease{Guard: recoveryTestGuard(func() error { return nil })}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-childDone:
		if err != nil {
			t.Fatal("creation failed after sweep release", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := os.Stat(filepath.Join(state, "child.journal")); err != nil {
		t.Fatal("creation did not resume", err)
	}
}

func Test_WindowsAppContainerJournalCreationHelper(t *testing.T) {
	path := os.Getenv("XMESH_JOURNAL_CREATE_PATH")
	if path == "" {
		t.Skip("journal creation coordination helper")
	}
	fmt.Println("attempting")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	j, err := openBoundGrantJournal(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}
