package winbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerWorkspaceChangeMonitor(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"periodic", "cancel", "history-loss", "append-failure", "delayed-delete", "cancel-disposition"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			initial := filepath.Join(root, "initial.txt")
			delayed := stage == "delayed-delete" || stage == "cancel-disposition"
			if delayed {
				if err := os.WriteFile(initial, []byte("initial"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			setMediumLabel(t, root)
			before, control := kernelJournalState(t, root)
			label := objectLabel(t, root)
			state := filepath.Join(base, "state")
			privateJournalDirectory(t, state)
			path := filepath.Join(state, "monitor.journal")
			j, err := openBoundGrantJournal(ctx, path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			p, err := newProfile(j)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := p.Close(); err != nil {
					t.Error("monitor profile cleanup", err)
				}
			}()
			g, err := p.grantWorkspace(ctx, root, 32)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := g.Close(); err != nil {
					t.Error("monitor workspace cleanup", err)
				}
			}()
			if err := beginWorkspaceChangeTracking(ctx, j, p.sid, 32); err != nil {
				t.Fatal(err)
			}
			owner, err := newExecutionProcesses(p, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if !owner.closed {
					if err := owner.reap(context.Background(), 1); err != nil {
						t.Error("monitor owner reaping", err)
						return
					}
					if err := owner.Close(); err != nil {
						t.Error("monitor owner close", err)
					}
				}
			}()
			count := len(j.records)
			stopped, stop := context.WithCancel(ctx)
			stop()
			if _, err := g.monitorChanges(stopped, owner, 20*time.Millisecond, maxWorkspaceChangeEvents); !errors.Is(err, context.Canceled) || len(j.records) != count || p.workspaceMonitor != nil || p.workspaceClosing {
				t.Fatal("cancelled monitor admission changed ownership", err)
			}
			for _, interval := range []time.Duration{0, time.Millisecond, 2 * time.Minute} {
				if _, err := g.monitorChanges(ctx, owner, interval, maxWorkspaceChangeEvents); err == nil {
					t.Fatal("unbounded monitor interval accepted")
				}
			}
			watch, cancelWatch := context.WithCancel(ctx)
			defer cancelWatch()
			monitor, err := g.monitorChanges(watch, owner, 20*time.Millisecond, maxWorkspaceChangeEvents)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				// Fault cases reopen journals below before normal teardown. The
				// monitor must stay attached until its goroutine has terminated.
				if p.processes == owner && !owner.closed {
					_ = owner.reap(context.Background(), 1)
				}
				_ = monitor.Stop(context.Background())
			}()
			if _, err := g.monitorChanges(ctx, owner, 20*time.Millisecond, maxWorkspaceChangeEvents); err == nil {
				t.Fatal("duplicate monitor admitted")
			}
			if err := g.Close(); !errors.Is(err, errWorkspaceMonitorActive) {
				t.Fatal("active monitor allowed workspace cleanup", err)
			}
			if err := monitor.Stop(ctx); !errors.Is(err, errJournalProcessesPending) {
				t.Fatal("monitor stopped before process receipt", err)
			}
			if err := monitor.Wait(stopped); !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled monitor observer", err)
			}
			gate, err := g.acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			deadline, deadlineCancel := context.WithTimeout(ctx, 25*time.Millisecond)
			err = monitor.Stop(deadline)
			deadlineCancel()
			gate()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("monitor stop ignored busy gate deadline", err)
			}
			deadline, deadlineCancel = context.WithTimeout(ctx, 25*time.Millisecond)
			err = monitor.Wait(deadline)
			deadlineCancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("monitor observer changed its lifecycle", err)
			}
			helper := filepath.Join(p.Directory(), "monitor-helper.exe")
			body, err := os.ReadFile(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(helper, body, 0700); err != nil {
				t.Fatal(err)
			}
			proof := filepath.Join(p.Directory(), "monitor-ready")
			process, err := owner.start(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerWorkspaceChangeMonitorHelper$", "--", root, proof, stage}, p.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_CHANGE_MONITOR_HELPER=1"})
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process.Process)
			defer windows.CloseHandle(process.Thread)
			wait := func(condition func() bool, description string) {
				t.Helper()
				for !condition() {
					select {
					case <-ctx.Done():
						t.Fatal(description, ctx.Err())
					case <-monitor.done:
						t.Fatal(description, "monitor ended", monitor.err)
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			wait(func() bool { data, _ := os.ReadFile(proof); return string(data) == "ready" }, "sandbox readiness")
			wait(func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(j.state.created) == 2 }, "periodic created identities")
			if delayed {
				wait(func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(j.state.deletePending) == 1 }, "periodic kernel pending-delete observation")
				p.mu.Lock()
				for object := range j.state.deletePending {
					if _, deleted := j.state.deleted[object]; deleted || j.state.workspaceReleased {
						p.mu.Unlock()
						t.Fatal("intent was treated as final deletion")
					}
				}
				manifest := recoveryJournalInfo(j)
				if len(manifest.PendingDeletes) != 1 {
					p.mu.Unlock()
					t.Fatal("missing recovery deletion intent")
				}
				manifest.PendingDeletes[0].ID[0] ^= 1
				if err := validateWorkspaceJournalOwnership(j, p.sid); err != nil {
					p.mu.Unlock()
					t.Fatal("detached intent manifest changed owner", err)
				}
				for object := range j.state.deletePending {
					j.state.deletePending[object] = false
					err := validateWorkspaceJournalOwnership(j, p.sid)
					j.state.deletePending[object] = true
					if err == nil {
						p.mu.Unlock()
						t.Fatal("altered intent manifest accepted")
					}
				}
				p.mu.Unlock()
				if stage == "cancel-disposition" {
					if err := os.WriteFile(proof+".clear", []byte("clear"), 0600); err != nil {
						t.Fatal(err)
					}
					wait(func() bool { data, _ := os.ReadFile(proof); return string(data) == "cleared" }, "cleared deletion disposition")
				}
			}
			moved := root + ".moved"
			if err := os.Rename(filepath.Join(root, "new-dir"), moved); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(moved, "late.txt"), []byte("late"), 0600); err != nil {
				t.Fatal(err)
			}
			wait(func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(j.state.created) == 3 }, "moved inheritance source")
			if status, err := windows.WaitForSingleObject(process.Process, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
				t.Fatal("monitor stopped live sandbox", status, err)
			}
			p.mu.Lock()
			cursor := *j.state.changeCursor
			references := p.grants
			admitted, admittedControl := kernelJournalState(t, root)
			switch stage {
			case "history-loss":
				j.state.changeCursor.JournalID ^= 1
				*j.records[len(j.records)-1].ChangeCursor = *j.state.changeCursor
			case "append-failure":
				if err := os.WriteFile(filepath.Join(root, "fault.txt"), []byte("fault"), 0600); err != nil {
					p.mu.Unlock()
					t.Fatal(err)
				}
				_ = j.file.Close()
				j.file, err = os.Open(path)
				if err != nil {
					p.mu.Unlock()
					t.Fatal(err)
				}
			}
			p.mu.Unlock()
			if stage == "periodic" || delayed {
				if err := owner.reap(ctx, 0); err != nil {
					t.Fatal(err)
				}
			} else {
				if stage == "cancel" {
					cancelWatch()
				}
				fault := monitor.Wait(ctx)
				if fault == nil || (stage == "cancel" && !errors.Is(fault, context.Canceled)) || (stage == "history-loss" && !errors.Is(fault, errWorkspaceChangesLost)) {
					t.Fatal("monitor fault was not retained", fault)
				}
				if status, err := windows.WaitForSingleObject(process.Process, 0); err != nil || status != windows.WAIT_OBJECT_0 {
					t.Fatal("monitor did not terminate sandbox", status, err)
				}
				p.mu.Lock()
				if !p.workspaceClosing || p.grants != references || g.tree == nil {
					p.mu.Unlock()
					t.Fatal("monitor fault lost workspace ownership")
				}
				after, afterControl := kernelJournalState(t, root)
				if !bytes.Equal(admitted, after) || admittedControl != afterControl {
					p.mu.Unlock()
					t.Fatal("monitor fault changed root security")
				}
				if stage == "history-loss" {
					*j.state.changeCursor = cursor
					for _, record := range j.records {
						if record.ChangeCursor != nil && record.ChangeCursor.Next == cursor.Next {
							*record.ChangeCursor = cursor
						}
					}
				}
				if stage == "append-failure" {
					if j.blocked == nil || p.processesConfirmed {
						p.mu.Unlock()
						t.Fatal("append failure acknowledged execution")
					}
					_ = j.Close()
					j, err = openBoundGrantJournal(ctx, path, false)
					if err != nil {
						p.mu.Unlock()
						t.Fatal(err)
					}
					p.journal = j
				}
				p.mu.Unlock()
				if err := owner.reap(ctx, 0); err != nil {
					t.Fatal("retry monitor process confirmation", err)
				}
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := newExecutionProcesses(p, 1); err == nil {
				t.Fatal("attached monitor allowed another owner")
			}
			if err := monitor.Stop(ctx); (stage == "periodic" || delayed) != (err == nil) {
				t.Fatal("monitor terminal handoff", err)
			}
			if err := g.Close(); err != nil {
				t.Fatal("monitored workspace cleanup", err)
			}
			if delayed {
				expected := 1
				if stage == "cancel-disposition" {
					expected = 0
				}
				if len(j.state.deletePending) != 1 || len(j.state.deleted) != expected {
					t.Fatal("deletion intent resolution", j.state.deletePending, j.state.deleted)
				}
				_, err := os.Stat(initial)
				if (stage == "delayed-delete" && !os.IsNotExist(err)) || (stage == "cancel-disposition" && err != nil) {
					t.Fatal("initial identity disposition", err)
				}
			}
			after, afterControl := kernelJournalState(t, root)
			if !bytes.Equal(before, after) || control != afterControl || objectLabel(t, root) != label || p.grants != 0 || p.workspaceMonitor != nil {
				t.Fatal("monitor cleanup changed root state or retained references")
			}
			sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(p.sid)), p.sid.Len())
			if err := filepath.WalkDir(moved, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				acl, _ := kernelJournalState(t, path)
				if bytes.Contains(acl, sidBytes) {
					return fmt.Errorf("monitored moved object retains profile ACE")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			_ = j.Close()
			j, err = openBoundGrantJournal(ctx, path, false)
			if err != nil || !j.state.complete || len(j.state.executions) != 0 || len(j.state.created) < 3 {
				t.Fatal("monitor durable replay", err)
			}
		})
	}
}

func Test_WindowsAppContainerWorkspaceChangeMonitorHelper(t *testing.T) {
	if os.Getenv("XMESH_CHANGE_MONITOR_HELPER") != "1" {
		t.Skip("AppContainer change monitor helper")
	}
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil || container == 0 {
		t.Fatal("monitor helper token", err)
	}
	args := os.Args[len(os.Args)-3:]
	directory := filepath.Join(args[0], "new-dir")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	var pending windows.Handle
	if args[2] == "delayed-delete" || args[2] == "cancel-disposition" {
		name, _ := windows.UTF16PtrFromString(filepath.Join(args[0], "initial.txt"))
		var err error
		pending, err = windows.CreateFile(name, windows.DELETE|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(pending)
		remove := uint32(1)
		if err := windows.SetFileInformationByHandle(pending, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&remove)), 4); err != nil {
			t.Fatal(err)
		}
		// Emit a subsequent data change while the deleting handle remains open.
		var written uint32
		if err := windows.WriteFile(pending, []byte("pending"), &written, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(args[1], []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	if args[2] == "cancel-disposition" {
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(args[1] + ".clear"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("disposition clear request timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
		remove := uint32(0)
		if err := windows.SetFileInformationByHandle(pending, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&remove)), 4); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(args[1], []byte("cleared"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(30 * time.Second)
	t.Fatal("monitor helper was not terminated")
}
