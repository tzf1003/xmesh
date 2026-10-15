package winbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func supervisorTestCommand(t *testing.T, cli bool) []string {
	t.Helper()
	if cli {
		path := os.Getenv("XMESH_TEST_WINDOWS_BINARY")
		if path == "" {
			t.Skip("set XMESH_TEST_WINDOWS_BINARY to the built xmesh executable")
		}
		return []string{path, "__sandbox-supervisor"}
	}
	return []string{os.Args[0], "-test.run=^Test_WindowsAppContainerSupervisorHelper$", "--", "supervisor"}
}

func Test_WindowsAppContainerSupervisorHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-4] != "supervisor" {
		t.Skip("inherited-handle supervisor helper")
	}
	if err := RunProcessSupervisor(os.Args[len(os.Args)-3:]); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerSupervisorLifecycle(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, cli := range []bool{false, true} {
		t.Run(fmt.Sprintf("cli-%t", cli), func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			privateJournalDirectory(t, state)
			path := filepath.Join(state, "normal.journal")
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
					t.Error(err)
				}
			}()
			owner, err := newSupervisedExecutionProcesses(context.Background(), p, 2, supervisorTestCommand(t, cli))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if !owner.closed {
					if err := owner.reap(context.Background(), 82); err != nil {
						t.Error(err)
					}
					if err := owner.Close(); err != nil {
						t.Error(err)
					}
				}
			}()
			s := owner.supervisor
			if err := owner.Close(); err == nil {
				t.Fatal("unreaped supervisor released")
			}
			// Cancellation before request admission leaves the sequence and handle set intact.
			s.operation <- struct{}{}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			_, err = s.call(ctx, supervisorRequest{Operation: "new-job"})
			cancel()
			<-s.operation
			if !errors.Is(err, context.DeadlineExceeded) || s.sequence != 1 || len(s.jobs) != 0 {
				t.Fatal("cancelled RPC advanced admission", err)
			}
			if _, err := s.call(context.Background(), supervisorRequest{Operation: "unsupported"}); err == nil || s.sequence != 1 {
				t.Fatal("invalid request advanced sequence", err)
			}
			// A completed exchange remains serialized after the initiating caller cancels.
			ctx, cancel = context.WithCancel(context.Background())
			reply, err := s.callPrepared(ctx, supervisorRequest{Operation: "new-job"}, func(*supervisorRequest) error { cancel(); return nil })
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(reply, err)
			}
			// This request waits for the preceding response, including its borrowed handle.
			if err := s.retain(context.Background(), 1, windows.CurrentProcess()); err == nil {
				t.Fatal("foreign root accepted")
			}
			reply, err = s.call(context.Background(), supervisorRequest{Operation: "new-job"})
			if err != nil || reply.Job != 2 {
				t.Fatal(reply, err)
			}
			if _, err := s.call(context.Background(), supervisorRequest{Operation: "new-job"}); err == nil {
				t.Fatal("supervisor Job limit bypassed")
			}
			if _, err := s.call(context.Background(), supervisorRequest{Operation: "close"}); err == nil {
				t.Fatal("close bypassed reaping")
			}
			for i := 0; i < 2; i++ {
				if err := owner.reap(context.Background(), 82); err != nil {
					t.Fatal(err)
				}
			}
			if len(j.state.executions) != 0 || !owner.acknowledged {
				t.Fatal("missing durable confirmation")
			}
			ctx, cancel = context.WithCancel(context.Background())
			_, err = s.callPrepared(ctx, supervisorRequest{Operation: "close"}, func(*supervisorRequest) error { cancel(); return nil })
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if !s.closed || len(s.jobs) != 0 {
				t.Fatal("supervisor handles retained after close")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func Test_WindowsAppContainerSupervisorBootstrapFailure(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	state := filepath.Join(t.TempDir(), "state")
	privateJournalDirectory(t, state)
	j, err := openBoundGrantJournal(context.Background(), filepath.Join(state, "bootstrap.journal"), true)
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
			t.Error(err)
		}
	}()
	// This executable exits without adopting the pipe protocol. EOF must be
	// visible even while launch still owns the original inherited handles.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner, err := newSupervisedExecutionProcesses(ctx, p, 1, []string{os.Args[0], "-test.run=^Test_NoSupervisorBootstrap$"})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || owner == nil || !owner.sealed || owner.supervisor != nil {
		t.Fatal("bootstrap failure lost empty-owner rollback", err)
	}
	if err := owner.reap(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

type supervisorCrashProof struct {
	Second                                uint64
	SecondPID                             uint32
	Supervisor, Root, Descendant          uint64
	SupervisorPID, RootPID, DescendantPID uint32
	Intent                                uint64
	Error                                 string
}

func Test_WindowsAppContainerSupervisorHostCrash(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, mode := range []string{"selected", "workspace"} {
		t.Run(mode, func(t *testing.T) {
			command := supervisorTestCommand(t, true)
			controlProcess := exec.Command(os.Args[0], "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", "member", "unused")
			controlProcess.Env = append(os.Environ(), "XMESH_PROCESS_OWNER_HELPER=1")
			if err := controlProcess.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = controlProcess.Process.Kill(); _ = controlProcess.Wait() }()
			var controlHandle windows.Handle
			if err := controlProcess.Process.WithHandle(func(h uintptr) {
				err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(h), windows.CurrentProcess(), &controlHandle, windows.SYNCHRONIZE, false, 0)
				if err != nil {
					t.Error(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(controlHandle)
			dir := t.TempDir()
			root := filepath.Join(dir, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "file.txt")
			if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			setMediumLabel(t, root)
			setMediumLabel(t, file)
			original, control := kernelJournalState(t, file)
			label := objectLabel(t, file)
			state := filepath.Join(dir, "state")
			privateJournalDirectory(t, state)
			path := filepath.Join(state, "crash.journal")
			var controller windows.Handle
			if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.CurrentProcess(), windows.CurrentProcess(), &controller, windows.PROCESS_DUP_HANDLE|windows.SYNCHRONIZE, false, 0); err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(controller)
			if err := windows.SetHandleInformation(controller, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^Test_WindowsAppContainerSupervisorHostHelper$")
			cmd.Env = append(os.Environ(), "XMESH_SUPERVISOR_HOST="+path, "XMESH_SUPERVISOR_ROOT="+root, "XMESH_SUPERVISOR_MODE="+mode, "XMESH_SUPERVISOR_CONTROLLER="+strconv.FormatUint(uint64(controller), 10), "XMESH_SUPERVISOR_BINARY="+command[0])
			cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(controller)}}
			cmd.Stderr = os.Stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			type proofResult struct {
				proof supervisorCrashProof
				err   error
			}
			ready := make(chan proofResult, 1)
			go func() {
				var p supervisorCrashProof
				line, err := bufio.NewReader(stdout).ReadBytes('\n')
				if err == nil {
					err = json.Unmarshal(line, &p)
				}
				ready <- proofResult{p, err}
			}()
			var proof supervisorCrashProof
			select {
			case r := <-ready:
				if r.err != nil || r.proof.Error != "" {
					t.Fatal("host readiness", r)
				}
				proof = r.proof
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			handles := []windows.Handle{windows.Handle(proof.Supervisor), windows.Handle(proof.Root), windows.Handle(proof.Descendant), windows.Handle(proof.Second)}
			for i, h := range handles {
				defer windows.CloseHandle(h)
				pid, err := windows.GetProcessId(h)
				want := []uint32{proof.SupervisorPID, proof.RootPID, proof.DescendantPID, proof.SecondPID}[i]
				if err != nil || pid != want {
					t.Fatal("transferred identity", i, pid, want, err)
				}
			}
			if result, err := windows.WaitForSingleObject(handles[1], 0); err != nil || result != windows.WAIT_OBJECT_0 {
				t.Fatal("root still alive", result, err)
			}
			for _, h := range []windows.Handle{handles[0], handles[2], handles[3]} {
				if result, err := windows.WaitForSingleObject(h, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
					t.Fatal("supervisor/descendant not live", result, err)
				}
			}
			// The inherited file object keeps the journal exclusively owned before host exit.
			if j, err := openBoundGrantJournal(context.Background(), path, false); j != nil || err == nil {
				if j != nil {
					j.Close()
				}
				t.Fatal("live supervision lost journal exclusivity", err)
			}
			admitted, admittedControl := kernelJournalState(t, file)
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			if result, err := windows.WaitForSingleObject(handles[0], 30000); err != nil || result != windows.WAIT_OBJECT_0 {
				t.Fatal("supervisor did not finish after host exit", result, err)
			}
			var code uint32
			if err := windows.GetExitCodeProcess(handles[0], &code); err != nil || code != 0 {
				t.Fatal("supervisor recovery failed", code, err)
			}
			if result, err := windows.WaitForSingleObject(handles[2], 0); err != nil || result != windows.WAIT_OBJECT_0 {
				t.Fatal("surviving descendant not confirmed dead", result, err)
			}
			if result, err := windows.WaitForSingleObject(handles[3], 0); err != nil || result != windows.WAIT_OBJECT_0 {
				t.Fatal("second Job member not confirmed dead", result, err)
			}
			if result, err := windows.WaitForSingleObject(controlHandle, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
				t.Fatal("independent control terminated", result, err)
			}

			after, afterControl := kernelJournalState(t, file)
			if !bytes.Equal(after, admitted) || afterControl != admittedControl || objectLabel(t, file) != label {
				t.Fatal("process supervision changed file security")
			}
			j, err := openBoundGrantJournal(context.Background(), path, false)
			if err != nil {
				t.Fatal(err)
			}
			receipts := 0
			for _, r := range j.records {
				if r.Kind == "execution-reaped" {
					receipts++
					if r.ExecutionSequence != proof.Intent {
						t.Fatal("wrong execution receipt")
					}
				}
			}
			if receipts != 1 || len(j.state.executions) != 0 || j.state.complete {
				t.Fatal("inaccurate surviving-supervisor receipt", receipts)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			tree, err := openWorkspaceRoot(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer tree.Close()
			prepared := 0
			report, err := recoverJournalDirectory(context.Background(), state, 4, 32, func(context.Context, journalRecoveryInfo) (*journalRecoveryLease, error) {
				prepared++
				return &journalRecoveryLease{Guard: recoveryTestGuard(func() error { return nil }), Volumes: map[uint64]windows.Handle{tree.objects[0].id.Volume: tree.ancestors[0]}}, nil
			})
			if err != nil || prepared != 1 || len(report) != 1 || report[0].State != "recovered" {
				t.Fatal("post-crash recovery", report, err)
			}
			after, afterControl = kernelJournalState(t, file)
			if !bytes.Equal(after, original) || afterControl != control || objectLabel(t, file) != label {
				t.Fatal("post-crash recovery changed original security")
			}
			j, err = openBoundGrantJournal(context.Background(), path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if !j.state.complete || len(j.state.executions) != 0 {
				t.Fatal("post-crash completion did not persist")
			}
		})
	}
}

func Test_WindowsAppContainerSupervisorHostHelper(t *testing.T) {
	path := os.Getenv("XMESH_SUPERVISOR_HOST")
	if path == "" {
		t.Skip("forced-exit execution host")
	}
	fail := func(err error) {
		json.NewEncoder(os.Stdout).Encode(supervisorCrashProof{Error: err.Error()})
		t.Fatal(err)
	}
	j, err := openBoundGrantJournal(context.Background(), path, true)
	if err != nil {
		fail(err)
	}
	p, err := newProfile(j)
	if err != nil {
		fail(err)
	}
	root := os.Getenv("XMESH_SUPERVISOR_ROOT")
	if os.Getenv("XMESH_SUPERVISOR_MODE") == "workspace" {
		_, err = p.grantWorkspace(context.Background(), root, 32)
	} else {
		_, err = p.grantObject(filepath.Join(root, "file.txt"))
	}
	if err != nil {
		fail(err)
	}
	owner, err := newSupervisedExecutionProcesses(context.Background(), p, 2, []string{os.Getenv("XMESH_SUPERVISOR_BINARY"), "__sandbox-supervisor"})
	if err != nil {
		fail(err)
	}
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		fail(err)
	}
	helper := filepath.Join(p.Directory(), "supervised-member.exe")
	if err := os.WriteFile(helper, body, 0700); err != nil {
		fail(err)
	}
	proofPath := filepath.Join(p.Directory(), "descendant.json")
	member, err := owner.start(context.Background(), []string{helper, "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", "root", proofPath}, p.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_PROCESS_OWNER_HELPER=1"})
	if err != nil {
		fail(err)
	}
	var childProof processOwnerProof
	for deadline := time.Now().Add(15 * time.Second); ; {
		body, err := os.ReadFile(proofPath)
		if err == nil && json.Unmarshal(body, &childProof) == nil {
			if childProof.Error != "" {
				fail(fmt.Errorf("descendant %s: %s", childProof.Stage, childProof.Error))
			}
			if childProof.PID != 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			fail(fmt.Errorf("descendant proof deadline"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, childProof.PID)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(proofPath+".captured", []byte("ready"), 0600); err != nil {
		fail(err)
	}
	if result, err := windows.WaitForSingleObject(member.Process, 15000); err != nil || result != windows.WAIT_OBJECT_0 {
		fail(fmt.Errorf("root exit: %d %v", result, err))
	}
	n, err := strconv.ParseUint(os.Getenv("XMESH_SUPERVISOR_CONTROLLER"), 10, 64)
	if err != nil {
		fail(err)
	}
	controller := windows.Handle(n)
	second, err := owner.start(context.Background(), []string{helper, "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", "member", "unused"}, p.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_PROCESS_OWNER_HELPER=1"})
	if err != nil {
		fail(err)
	}
	var transferred [4]windows.Handle
	for i, source := range []windows.Handle{owner.supervisor.process, member.Process, child, second.Process} {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), source, controller, &transferred[i], windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 0); err != nil {
			fail(err)
		}
	}
	pid, err := windows.GetProcessId(owner.supervisor.process)
	if err != nil {
		fail(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(supervisorCrashProof{Second: uint64(transferred[3]), SecondPID: second.ProcessId, Supervisor: uint64(transferred[0]), Root: uint64(transferred[1]), Descendant: uint64(transferred[2]), SupervisorPID: pid, RootPID: member.ProcessId, DescendantPID: childProof.PID, Intent: owner.intent}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Second)
	t.Fatal("controller did not terminate host")
}
