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

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerExecutionJournalGate(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, workspace := range []bool{false, true} {
		name := "selected"
		if workspace {
			name = "workspace"
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "file.txt")
			if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			setMediumLabel(t, root)
			setMediumLabel(t, file)
			original, originalControl := kernelJournalState(t, file)
			label := objectLabel(t, file)
			state := filepath.Join(filepath.Dir(root), "state")
			privateJournalDirectory(t, state)
			path := filepath.Join(state, "execution.journal")
			j, err := openBoundGrantJournal(context.Background(), path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			p, err := newProfile(j)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := p.Close(); err != nil {
					t.Error("profile cleanup", err)
				}
			}()
			var closeGrant func() error
			var closeSelected func() error
			if workspace {
				g, err := p.grantWorkspace(context.Background(), root, 16)
				if err != nil {
					t.Fatal(err)
				}
				closeGrant, closeSelected = g.Close, g.selected.Close
			} else {
				g, err := p.grantObject(file)
				if err != nil {
					t.Fatal(err)
				}
				closeGrant, closeSelected = g.Close, g.Close
			}
			defer func() {
				if err := closeGrant(); err != nil {
					t.Error("grant cleanup", err)
				}
			}()
			attrs, err := windows.NewProcThreadAttributeList(1)
			if err != nil {
				t.Fatal(err)
			}
			defer attrs.Delete()
			if err := p.Apply(attrs); err == nil {
				t.Fatal("journaled standalone launch bypassed durable intent")
			}
			owner, err := newExecutionProcesses(p, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if !owner.closed {
					if err := owner.reap(context.Background(), 83); err != nil {
						t.Error(err)
					}
					if err := owner.Close(); err != nil {
						t.Error(err)
					}
				}
			}()
			if !j.state.executions[owner.intent] || j.records[len(j.records)-1].Kind != "execution" {
				t.Fatal("owner published before its durable execution intent")
			}
			body, err := os.ReadFile(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(p.Directory(), "execution-journal-helper.exe")
			if err := os.WriteFile(helper, body, 0700); err != nil {
				t.Fatal(err)
			}
			process, err := owner.start(context.Background(), []string{helper, "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", "member", "unused"}, p.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_PROCESS_OWNER_HELPER=1"})
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process.Process)
			defer windows.CloseHandle(process.Thread)
			admitted, control := kernelJournalState(t, file)
			records := len(j.records)
			for _, cleanup := range []func() error{closeGrant, closeSelected} {
				if err := cleanup(); !errors.Is(err, errJournalProcessesPending) {
					t.Fatal("live execution allowed ACL reclamation", err)
				}
			}
			if err := p.Close(); err == nil {
				t.Fatal("live execution allowed profile deletion")
			}
			after, afterControl := kernelJournalState(t, file)
			if !bytes.Equal(admitted, after) || control != afterControl || len(j.records) != records || objectLabel(t, file) != label {
				t.Fatal("blocked reclamation changed security or journal")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := owner.reap(ctx, 83); !errors.Is(err, context.Canceled) || len(j.records) != records || !j.state.executions[owner.intent] {
				t.Fatal("cancelled reap acknowledged execution", err)
			}
			job := owner.jobs[0].handle
			owner.jobs[0].handle = windows.InvalidHandle
			ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			err = owner.reap(ctx, 83)
			cancel()
			owner.jobs[0].handle = job
			if !errors.Is(err, windows.ERROR_INVALID_HANDLE) || !errors.Is(err, context.DeadlineExceeded) || len(j.records) != records || !j.state.executions[owner.intent] || p.processesConfirmed {
				t.Fatal("failed reap acknowledged execution", err)
			}
			if result, err := windows.WaitForSingleObject(process.Process, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
				t.Fatal("failed termination changed live member", result, err)
			}
			for i := 0; i < 2; i++ {
				if err := owner.reap(context.Background(), 83); err != nil {
					t.Fatal("reap and durable acknowledgement", err)
				}
			}
			if len(j.records) != records+1 || j.records[records].Kind != "execution-reaped" || j.records[records].ExecutionSequence != owner.intent || len(j.state.executions) != 0 || !p.processesConfirmed {
				t.Fatal("execution receipt was missing or duplicated")
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := closeGrant(); err != nil {
				t.Fatal("confirmed execution blocked ACL reclamation", err)
			}
			after, afterControl = kernelJournalState(t, file)
			if !bytes.Equal(original, after) || originalControl != afterControl || objectLabel(t, file) != label {
				t.Fatal("confirmed reclamation changed original descriptor or label")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := openBoundGrantJournal(context.Background(), path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if !reopened.state.complete || len(reopened.state.executions) != 0 || len(reopened.state.executionIDs) != 1 {
				t.Fatal("durable execution receipt/completion did not replay")
			}
		})
	}
}

func Test_WindowsAppContainerExecutionIntentCrashGate(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, mode := range []string{"selected", "workspace"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "file.txt")
			if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(filepath.Dir(root), "state")
			privateJournalDirectory(t, state)
			path := filepath.Join(state, "intent.journal")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^Test_WindowsAppContainerExecutionIntentCrashHelper$")
			cmd.Env = append(os.Environ(), "XMESH_EXECUTION_JOURNAL="+path, "XMESH_EXECUTION_ROOT="+root, "XMESH_EXECUTION_MODE="+mode)
			cmd.Stderr = os.Stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			ready := make(chan string, 1)
			go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
			select {
			case line := <-ready:
				if line != "ready\n" && line != "ready\r\n" {
					t.Fatal("execution intent helper readiness", line)
				}
			case <-ctx.Done():
				t.Fatal("execution intent helper deadline")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			j, err := openBoundGrantJournal(context.Background(), path, false)
			if err != nil {
				t.Fatal(err)
			}
			// This fixture crashes after intent and before any process admission.
			// Its known-empty execution can be acknowledged for test teardown;
			// production recovery must obtain independent process confirmation.
			defer func() {
				cleanup, err := openBoundGrantJournal(context.Background(), path, false)
				if err != nil {
					t.Error(err)
					return
				}
				defer cleanup.Close()
				for sequence := range cleanup.state.executions {
					if _, err := cleanup.append(journalRecord{Kind: "execution-reaped", ExecutionSequence: sequence}); err != nil {
						t.Error(err)
						return
					}
				}
				if mode == "workspace" {
					err = recoverWorkspaceJournal(cleanup, 16)
				} else {
					name, _ := windows.UTF16PtrFromString(root)
					volume, openErr := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
					if openErr != nil {
						t.Error(openErr)
						return
					}
					defer windows.CloseHandle(volume)
					err = recoverSelectedJournal(cleanup, volume)
				}
				if err != nil {
					t.Error("intent fixture cleanup", err)
				}
			}()
			defer j.Close()
			if len(j.state.executions) != 1 || j.state.complete {
				t.Fatal("host exit discarded unconfirmed execution")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			acl, control := kernelJournalState(t, file)
			label := objectLabel(t, file)
			prepared := 0
			report, err := recoverJournalDirectory(context.Background(), state, 4, 16, func(context.Context, journalRecoveryInfo) (*journalRecoveryLease, error) {
				prepared++
				return &journalRecoveryLease{Guard: recoveryTestGuard(func() error { return nil })}, nil
			})
			if !errors.Is(err, errJournalProcessesPending) || prepared != 0 || len(report) != 1 || report[0].State != "retained" || !errors.Is(report[0].Err, errJournalProcessesPending) {
				t.Fatal("unconfirmed execution reached recovery coordination", report, prepared, err)
			}
			after, err := os.ReadFile(path)
			afterACL, afterControl := kernelJournalState(t, file)
			if err != nil || !bytes.Equal(before, after) || !bytes.Equal(acl, afterACL) || control != afterControl || label != objectLabel(t, file) {
				t.Fatal("retained execution changed journal or security", err)
			}
		})
	}
}

func Test_WindowsAppContainerExecutionIntentCrashHelper(t *testing.T) {
	path := os.Getenv("XMESH_EXECUTION_JOURNAL")
	if path == "" {
		t.Skip("execution intent forced-exit helper")
	}
	j, err := openBoundGrantJournal(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newProfile(j)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("XMESH_EXECUTION_ROOT")
	if os.Getenv("XMESH_EXECUTION_MODE") == "workspace" {
		_, err = p.grantWorkspace(context.Background(), root, 16)
	} else {
		_, err = p.grantObject(filepath.Join(root, "file.txt"))
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newExecutionProcesses(p, 1); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	time.Sleep(30 * time.Second)
	t.Fatal("execution intent host was not terminated")
}

func Test_WindowsAppContainerExecutionJournalAvailability(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	j, _ := journalFixture(t)
	p, err := newProfile(j)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	// Model the profile lock's state while workspace cleanup temporarily calls
	// helpers outside it. Admission must stop before publishing another intent.
	p.reclaiming = true
	if owner, err := newExecutionProcesses(p, 1); err == nil || owner != nil || len(j.records) != 1 {
		t.Fatal("workspace reclamation admitted an execution owner", err)
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	if err := p.Apply(attrs); err == nil {
		t.Fatal("workspace reclamation admitted standalone attributes")
	}
	p.reclaiming = false
	owner, err := newExecutionProcesses(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !owner.closed {
			if err := owner.reap(context.Background(), 1); err != nil {
				t.Error(err)
			}
			if err := owner.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	// Inject only an availability guard, before any write. Clearing this test
	// guard does not repair or retry an uncertain filesystem write.
	for _, acknowledged := range []bool{false, true} {
		j.blocked = errors.New("test journal availability guard")
		err := owner.reap(context.Background(), 1)
		j.blocked = nil
		if err == nil || owner.reaped || owner.acknowledged != acknowledged {
			t.Fatal("unavailable journal allowed execution confirmation", acknowledged, err)
		}
		if !acknowledged && (!j.state.executions[owner.intent] || p.processesConfirmed) {
			t.Fatal("unavailable journal discarded pending execution")
		}
		if err := owner.Close(); err == nil {
			t.Fatal("unconfirmed owner released after journal failure")
		}
		if err := owner.reap(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(j.records) != 3 || len(j.state.executions) != 0 {
		t.Fatal("journal availability retry duplicated the receipt")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}
