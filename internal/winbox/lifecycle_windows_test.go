package winbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func workspaceExecutionTestOptions(t *testing.T) workspaceExecutionOptions {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	setMediumLabel(t, root)
	state := filepath.Join(base, "state")
	privateJournalDirectory(t, state)
	return workspaceExecutionOptions{Workspace: root, Journal: filepath.Join(state, "execution.journal"),
		MaxObjects: 32, MaxJobs: 2, Interval: 20 * time.Millisecond, MaxEvents: maxWorkspaceChangeEvents,
		SupervisorCommand: supervisorTestCommand(t, false)}
}

func Test_WindowsAppContainerWorkspaceExecution(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, mode := range []string{"process", "console", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			options := workspaceExecutionTestOptions(t)
			initial := filepath.Join(options.Workspace, "initial.txt")
			if err := os.WriteFile(initial, []byte("initial"), 0600); err != nil {
				t.Fatal(err)
			}
			before, control := kernelJournalState(t, options.Workspace)
			label := objectLabel(t, options.Workspace)
			var e *workspaceExecution
			guardCloses := 0
			guard := recoveryTestGuard(func() error {
				guardCloses++
				if e.journal != nil || e.profile != nil || e.workspace != nil || e.processes != nil || e.monitor != nil {
					t.Error("writer coordination released before owned resources")
				}
				j, err := openBoundGrantJournal(context.Background(), options.Journal, false)
				if err != nil {
					return err
				}
				defer j.Close()
				if !j.state.complete || len(j.state.executions) != 0 || !j.state.workspaceReleased || len(j.state.pending) != 0 {
					t.Error("writer coordination released before durable completion")
				}
				return nil
			})
			var err error
			e, err = newWorkspaceExecution(ctx, options, func(_ context.Context, observed workspaceExecutionOptions) (io.Closer, error) {
				if _, err := os.Stat(options.Journal); !errors.Is(err, os.ErrNotExist) || observed.Workspace != options.Workspace {
					t.Fatal("coordination did not precede journal admission", err)
				}
				observed.SupervisorCommand[0] = "changed-by-coordinator.exe"
				return guard, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := e.closeContext(context.Background(), 91); err != nil && !errors.Is(err, context.Canceled) {
					t.Error("lifecycle cleanup", err)
				}
			}()
			journal, profile, monitor := e.journal, e.profile, e.monitor
			helper := filepath.Join(profile.Directory(), "lifecycle-helper.exe")
			body, err := sandboxHelperImage()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(helper, body, 0700); err != nil {
				t.Fatal(err)
			}
			proof := filepath.Join(profile.Directory(), "ready")
			var console windows.Handle
			if mode == "console" {
				c := newExecutionTestConsole(t)
				defer func() {
					_ = e.closeContext(context.Background(), 91)
					c.close(t)
				}()
				console = c.handle
			}
			process, err := e.start(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerWorkspaceChangeMonitorHelper$", "--", options.Workspace, proof, "delayed-delete"},
				profile.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_CHANGE_MONITOR_HELPER=1", "GORACE=log_path=lifecycle-race"}, nil, console)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process.Process)
			defer windows.CloseHandle(process.Thread)
			for {
				profile.mu.Lock()
				observed := len(journal.state.created) == 2 && len(journal.state.deletePending) == 1
				profile.mu.Unlock()
				data, _ := os.ReadFile(proof)
				if observed && string(data) == "ready" {
					break
				}
				// A helper that exits before it reports ready can never recover;
				// say why at once instead of waiting for the deadline.
				if string(data) != "ready" {
					if status, _ := windows.WaitForSingleObject(process.Process, 0); status == windows.WAIT_OBJECT_0 {
						t.Fatal("lifecycle helper exited before it was ready: " + describeHelperExit(process.Process, profile.Directory(), "lifecycle-race", proof))
					}
				}
				select {
				case <-ctx.Done():
					t.Fatal("lifecycle helper/tracking readiness", ctx.Err())
				case <-monitor.done:
					t.Fatal("lifecycle monitor ended", monitor.err)
				case <-time.After(10 * time.Millisecond):
				}
			}
			moved := filepath.Join(filepath.Dir(options.Workspace), "moved")
			release, err := e.workspace.acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = os.Rename(filepath.Join(options.Workspace, "new-dir"), moved)
			release()
			if err != nil {
				t.Fatal("tracked directory move", err)
			}
			if mode == "cancel" {
				cancel()
				if err := monitor.Wait(context.Background()); !errors.Is(err, context.Canceled) {
					t.Fatal("lifecycle cancellation", err)
				}
				if _, err := e.start(context.Background(), []string{helper}, profile.Directory(), nil, nil, 0); err == nil {
					t.Fatal("cancelled lifecycle admitted a process")
				}
			}
			err = e.closeContext(context.Background(), 91)
			if (mode == "cancel" && !errors.Is(err, context.Canceled)) || (mode != "cancel" && err != nil) {
				t.Fatal("lifecycle close", err)
			}
			if !e.closed || guardCloses != 1 || !journal.state.complete {
				t.Fatal("lifecycle completion/guard ownership", e.closed, guardCloses)
			}
			if result, err := windows.WaitForSingleObject(process.Process, 0); err != nil || result != windows.WAIT_OBJECT_0 {
				t.Fatal("lifecycle retained a live root", result, err)
			}
			if _, err := os.Stat(initial); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("final close did not complete pending deletion", err)
			}
			after, afterControl := kernelJournalState(t, options.Workspace)
			if !bytes.Equal(before, after) || control != afterControl || label != objectLabel(t, options.Workspace) {
				t.Fatal("workspace descriptor/control/label changed after lifecycle")
			}
			for _, path := range []string{moved, filepath.Join(moved, "new.txt")} {
				sid, err := windows.StringToSid(journal.records[0].SID)
				if err != nil {
					t.Fatal(err)
				}
				acl, _ := kernelJournalState(t, path)
				if bytes.Contains(acl, unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())) {
					t.Fatal("moved identity retained profile ACE")
				}
			}
			if err := e.closeContext(context.Background(), 91); err != nil || guardCloses != 1 {
				t.Fatal("lifecycle close retry", err, guardCloses)
			}
			if _, err := e.start(context.Background(), []string{helper}, "", nil, nil, 0); err == nil {
				t.Fatal("closed lifecycle admitted a process")
			}
		})
	}
}

func Test_WindowsAppContainerWorkspaceExecutionFailure(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"prepare", "journal", "workspace", "supervisor", "guard-retry", "cancel-close", "busy-close", "supervisor-close", "concurrent-close"} {
		t.Run(stage, func(t *testing.T) {
			options := workspaceExecutionTestOptions(t)
			before, control := kernelJournalState(t, options.Workspace)
			original := options.Workspace
			exitRelease := filepath.Join(filepath.Dir(options.Workspace), "supervisor-exit")
			switch stage {
			case "journal":
				options.Journal = filepath.Join(filepath.Dir(options.Journal), "absent", "execution.journal")
			case "workspace":
				options.Workspace = filepath.Join(options.Workspace, "absent")
			case "supervisor":
				options.SupervisorCommand = []string{filepath.Join(options.Workspace, "absent.exe")}
			case "supervisor-close":
				options.SupervisorCommand = []string{os.Args[0], "-test.run=^Test_WindowsAppContainerWorkspaceExecutionSupervisorHelper$", "--", exitRelease, "lifecycle-supervisor"}
			}
			closes := 0
			sentinel := errors.New("coordinator test failure")
			e, setupErr := newWorkspaceExecution(context.Background(), options, func(context.Context, workspaceExecutionOptions) (io.Closer, error) {
				guard := recoveryTestGuard(func() error {
					closes++
					if stage == "guard-retry" && closes == 1 {
						return sentinel
					}
					return nil
				})
				if stage == "prepare" {
					return guard, sentinel
				}
				return guard, nil
			})
			if e == nil || closes != 0 {
				t.Fatal("setup lost coordinator ownership", setupErr)
			}
			defer func() {
				if err := e.closeContext(context.Background(), 1); err != nil {
					t.Error("failure cleanup", err)
				}
			}()
			failedSetup := stage == "prepare" || stage == "journal" || stage == "workspace" || stage == "supervisor"
			if (setupErr != nil) != failedSetup || e.ready == failedSetup {
				t.Fatal("setup admission state", setupErr, e.ready)
			}
			if failedSetup {
				if _, err := e.start(context.Background(), []string{"cmd.exe"}, "", nil, nil, 0); err == nil {
					t.Fatal("partial setup admitted a process")
				}
			}
			if stage == "cancel-close" || stage == "busy-close" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				var err error
				if stage == "busy-close" {
					gate, gateErr := e.workspace.acquire(context.Background())
					if gateErr != nil {
						t.Fatal(gateErr)
					}
					deadline, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
					err = e.closeContext(deadline, 1)
					stop()
					gate()
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("busy cleanup ignored deadline", err)
					}
				} else {
					err = e.closeContext(ctx, 1)
					if !errors.Is(err, context.Canceled) || !e.ready {
						t.Fatal("pre-cancelled cleanup changed admission", err)
					}
				}
				if closes != 0 || e.guard == nil || e.workspace == nil || e.journal == nil {
					t.Fatal("cancelled cleanup lost ownership")
				}
			}
			if stage == "guard-retry" {
				if err := e.closeContext(context.Background(), 1); !errors.Is(err, sentinel) || e.closed || e.guard == nil || e.journal != nil {
					t.Fatal("guard close failure lost retry ownership", err)
				}
			}
			if stage == "supervisor-close" {
				// The helper holds its actual process alive after acknowledging
				// close. Cancel precisely that wait; retry must resume release
				// while preserving the durable receipt instead of sending reap.
				defer func() { _ = os.WriteFile(exitRelease, []byte("exit"), 0600) }()
				s := e.processes.supervisor
				ctx, stop := context.WithCancel(context.Background())
				defer stop()
				done := make(chan error, 1)
				go func() { done <- e.closeContext(ctx, 1) }()
				deadline := time.After(5 * time.Second)
				for {
					s.mu.Lock()
					acknowledged := s.closeAcknowledged
					s.mu.Unlock()
					if acknowledged {
						break
					}
					select {
					case err := <-done:
						t.Fatal("supervisor close completed before exit release", err)
					case <-deadline:
						stop()
						<-done
						t.Fatal("supervisor close acknowledgement timed out")
					case <-time.After(5 * time.Millisecond):
					}
				}
				stop()
				if err := <-done; !errors.Is(err, context.Canceled) || e.processes == nil || e.monitor != nil || !e.processes.closing || closes != 0 {
					t.Fatal("supervisor close cancellation lost phase ownership", err)
				}
				if len(e.journal.state.executions) != 0 || !e.profile.processesConfirmed {
					t.Fatal("supervisor close cancellation lost durable confirmation")
				}
				if err := os.WriteFile(exitRelease, []byte("exit"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "concurrent-close" {
				var wg sync.WaitGroup
				results := make(chan error, 4)
				for range 4 {
					wg.Add(1)
					go func() { defer wg.Done(); results <- e.closeContext(context.Background(), 1) }()
				}
				wg.Wait()
				close(results)
				for err := range results {
					if err != nil {
						t.Fatal("concurrent lifecycle close", err)
					}
				}
			} else if err := e.closeContext(context.Background(), 1); err != nil {
				t.Fatal("retry cleanup", err)
			}
			wantCloses := 1
			if stage == "guard-retry" {
				wantCloses++
			}
			if !e.closed || closes != wantCloses || e.guard != nil {
				t.Fatal("cleanup completion", e.closed, closes)
			}
			if _, err := os.Stat(options.Journal); err == nil {
				j, err := openBoundGrantJournal(context.Background(), options.Journal, false)
				if err != nil {
					t.Fatal("completed journal readback", err)
				}
				complete := j.state.complete && len(j.state.executions) == 0 && len(j.state.pending) == 0
				_ = j.Close()
				if !complete {
					t.Fatal("partial setup did not persist completion")
				}
			}
			after, afterControl := kernelJournalState(t, original)
			if !bytes.Equal(before, after) || control != afterControl {
				t.Fatal("setup/cleanup failure changed workspace descriptor")
			}
		})
	}
}

func Test_WindowsAppContainerWorkspaceExecutionSupervisorHelper(t *testing.T) {
	if len(os.Args) < 5 || os.Args[len(os.Args)-4] != "lifecycle-supervisor" {
		t.Skip("workspace lifecycle supervisor helper")
	}
	if err := RunProcessSupervisor(os.Args[len(os.Args)-3:]); err != nil {
		t.Fatal(err)
	}
	release := os.Args[len(os.Args)-5]
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(release); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("workspace lifecycle supervisor exit release timed out")
}

func Test_WindowsAppContainerWorkspaceExecutionAdmission(t *testing.T) {
	// Validation must precede the trusted callback and every filesystem mutation.
	options := workspaceExecutionOptions{Workspace: `C:\workspace`, Journal: `C:\state\execution.journal`,
		MaxObjects: 32, MaxJobs: 2, Interval: 20 * time.Millisecond, MaxEvents: maxWorkspaceChangeEvents}
	called := false
	prepare := func(context.Context, workspaceExecutionOptions) (io.Closer, error) {
		called = true
		return nil, nil
	}
	for _, change := range []func(*workspaceExecutionOptions){
		func(o *workspaceExecutionOptions) { o.MaxObjects = 0 },
		func(o *workspaceExecutionOptions) { o.MaxObjects = 10001 },
		func(o *workspaceExecutionOptions) { o.MaxJobs = 0 },
		func(o *workspaceExecutionOptions) { o.MaxJobs = 257 },
		func(o *workspaceExecutionOptions) { o.Interval = time.Millisecond },
		func(o *workspaceExecutionOptions) { o.Interval = 2 * time.Minute },
		func(o *workspaceExecutionOptions) { o.MaxEvents = 0 },
		func(o *workspaceExecutionOptions) { o.MaxEvents = maxWorkspaceChangeEvents + 1 },
		func(o *workspaceExecutionOptions) { o.Workspace = `relative` },
		func(o *workspaceExecutionOptions) { o.Journal = `C:\state\..\execution.journal` },
	} {
		invalid := options
		change(&invalid)
		if e, err := newWorkspaceExecution(context.Background(), invalid, prepare); e != nil || err == nil || called {
			t.Fatal("invalid admission crossed coordination boundary", err, called)
		}
	}
	if e, err := newWorkspaceExecution(context.Background(), options, nil); e != nil || err == nil {
		t.Fatal("missing coordinator admitted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e, err := newWorkspaceExecution(ctx, options, prepare); e != nil || !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancelled admission crossed coordination boundary", err)
	}
	if e, err := newWorkspaceExecution(context.Background(), options, prepare); e != nil || err == nil || !called {
		t.Fatal("missing guard admitted", err)
	}
	closes := 0
	e, err := newWorkspaceExecution(ctx, options, func(context.Context, workspaceExecutionOptions) (io.Closer, error) {
		t.Fatal("cancelled setup acquired a guard")
		return nil, nil
	})
	if e != nil || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	e, err = newWorkspaceExecution(ctx, options, func(context.Context, workspaceExecutionOptions) (io.Closer, error) {
		cancel()
		return recoveryTestGuard(func() error { closes++; return nil }), nil
	})
	if e == nil || !errors.Is(err, context.Canceled) || e.journal != nil || closes != 0 {
		t.Fatal("cancelled prepared setup lost ownership", err)
	}
	if err := e.closeContext(context.Background(), 1); err != nil || closes != 1 {
		t.Fatal("prepared setup guard cleanup", err, closes)
	}
}
