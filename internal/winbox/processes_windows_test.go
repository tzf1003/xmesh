package winbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type processOwnerProof struct {
	PID   uint32
	Stage string
	Error string
}

func Test_WindowsAppContainerProcessOwner(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error("profile cleanup", err)
		}
	}()
	owner, err := newExecutionProcesses(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.reap(context.Background(), 81); err != nil && !owner.closed {
			t.Error("process owner cleanup", err)
		}
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	if other, err := newExecutionProcesses(p, 2); other != nil || err == nil {
		t.Fatal("duplicate execution owner accepted", err)
	}
	if err := owner.Close(); err == nil {
		t.Fatal("unconfirmed owner released")
	}
	if err := p.Close(); err == nil {
		t.Fatal("profile deleted while execution owner retained")
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	if err := p.Apply(attrs); err == nil {
		t.Fatal("standalone launch bypassed execution owner")
	}
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(p.Directory(), "process-owner-helper.exe")
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(helper)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	if err := errors.Join(copyErr, out.Close()); err != nil {
		t.Fatal(err)
	}
	env := []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_PROCESS_OWNER_HELPER=1"}
	args := func(mode, proof string) []string {
		return []string{helper, "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", mode, proof}
	}
	// A cancelled gate waiter must return while creation remains serialized.
	owner.operation <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = owner.start(ctx, args("member", "unused"), p.Directory(), env)
	cancel()
	<-owner.operation
	if !errors.Is(err, context.DeadlineExceeded) || len(owner.jobs) != 0 {
		t.Fatal("cancelled admission changed the owner", err, len(owner.jobs))
	}
	proof := filepath.Join(p.Directory(), "descendant.json")
	root, err := owner.start(context.Background(), args("root", proof), p.Directory(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(root.Process)
	defer windows.CloseHandle(root.Thread)
	var observed processOwnerProof
	for deadline := time.Now().Add(15 * time.Second); ; {
		body, err := os.ReadFile(proof)
		if err == nil && json.Unmarshal(body, &observed) == nil {
			if observed.Error != "" {
				t.Fatal("AppContainer descendant setup", observed.Stage, observed.Error)
			}
			if observed.PID != 0 {
				break
			}
		}
		if result, err := windows.WaitForSingleObject(root.Process, 0); err != nil || result == windows.WAIT_OBJECT_0 {
			var exit uint32
			query := windows.GetExitCodeProcess(root.Process, &exit)
			t.Fatal("AppContainer root exited before descendant proof", observed, result, err, exit, query)
		}
		if time.Now().After(deadline) {
			t.Fatal("AppContainer root did not publish its descendant", observed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, observed.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	if err := os.WriteFile(proof+".captured", []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := windows.WaitForSingleObject(root.Process, 15000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("root did not exit", result, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(root.Process, &code); err != nil || code != 0 {
		t.Fatal("root helper failed", code, err)
	}
	member, err := owner.start(context.Background(), args("member", "unused"), p.Directory(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(member.Process)
	defer windows.CloseHandle(member.Thread)
	if _, err := owner.start(context.Background(), args("member", "unused"), p.Directory(), env); err == nil || len(owner.jobs) != 2 {
		t.Fatal("Job limit admitted another process", err)
	}
	control := exec.Command(source, "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", "member", "unused")
	control.Env = append(os.Environ(), "XMESH_PROCESS_OWNER_HELPER=1")
	if err := control.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = control.Process.Kill(); _ = control.Wait() }()
	controlHandle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(control.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(controlHandle)
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if err := owner.reap(ctx, 81); !errors.Is(err, context.Canceled) || owner.sealed {
		t.Fatal("cancelled reap changed admission", err)
	}
	for _, handle := range []windows.Handle{child, member.Process, controlHandle} {
		if result, err := windows.WaitForSingleObject(handle, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("cancelled reap terminated a member/control", result, err)
		}
	}
	// A failed termination of one Job must still reap the other Job, seal
	// admission, and retain the profile reference for a later retry.
	secondJob := owner.jobs[1].handle
	owner.jobs[1].handle = windows.InvalidHandle
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = owner.reap(ctx, 81)
	cancel()
	owner.jobs[1].handle = secondJob
	if !errors.Is(err, windows.ERROR_INVALID_HANDLE) || !errors.Is(err, context.DeadlineExceeded) || !owner.sealed || owner.reaped {
		t.Fatal("partial reap lost errors or released obligations", err)
	}
	if result, err := windows.WaitForSingleObject(child, 10000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("failed Job blocked independent reaping", result, err)
	}
	if result, err := windows.WaitForSingleObject(member.Process, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("unavailable Job unexpectedly terminated", result, err)
	}
	if err := owner.Close(); err == nil {
		t.Fatal("failed reap released owner")
	}
	if err := p.Close(); err == nil {
		t.Fatal("failed reap released profile")
	}
	if err := owner.reap(context.Background(), 81); err != nil {
		t.Fatal("partial reap retry", err)
	}
	for _, handle := range []windows.Handle{child, member.Process} {
		if result, err := windows.WaitForSingleObject(handle, 0); err != nil || result != windows.WAIT_OBJECT_0 {
			t.Fatal("successful reap preceded a member exit signal", result, err)
		}
		if err := windows.GetExitCodeProcess(handle, &code); err != nil || code != 81 {
			t.Fatal("member termination code", code, err)
		}
	}
	if result, err := windows.WaitForSingleObject(controlHandle, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("independent control terminated", result, err)
	}
	if _, err := owner.start(context.Background(), args("member", "unused"), p.Directory(), env); err == nil {
		t.Fatal("sealed owner admitted a process")
	}
	if err := owner.reap(context.Background(), 81); err != nil {
		t.Fatal("reap retry", err)
	}
	if err := p.Close(); err == nil {
		t.Fatal("profile deleted before owner handle release")
	}
	for i := 0; i < 2; i++ {
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.start(context.Background(), args("member", "unused"), p.Directory(), env); err == nil {
		t.Fatal("closed owner admitted a process")
	}
}

func Test_WindowsAppContainerProcessOwnerHelper(t *testing.T) {
	if os.Getenv("XMESH_PROCESS_OWNER_HELPER") != "1" {
		t.Skip("process owner helper")
	}
	args := os.Args[len(os.Args)-2:]
	if args[0] == "root" {
		report := func(stage string, pid uint32, failure error) {
			proof := processOwnerProof{PID: pid, Stage: stage}
			if failure != nil {
				proof.Error = failure.Error()
			}
			body, err := json.Marshal(proof)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(args[1], body, 0600); err != nil {
				t.Fatal("publish process owner proof", err)
			}
		}
		report("root-started", 0, nil)
		executable, err := os.Executable()
		if err != nil {
			report("executable", 0, err)
			t.Fatal(err)
		}
		child := exec.Command(executable, "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", "member", "unused")
		// The capability-free token cannot open NUL for exec.Cmd's default
		// streams. Supply ordinary profile-owned file handles explicitly.
		stdio, err := os.OpenFile(args[1]+".stdio", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			report("child-stdio", 0, err)
			t.Fatal(err)
		}
		defer stdio.Close()
		child.Stdin, child.Stdout, child.Stderr = stdio, stdio, stdio
		if err := child.Start(); err != nil {
			report("child-start", 0, err)
			t.Fatal(err)
		}
		defer child.Process.Release()
		report("child-started", uint32(child.Process.Pid), nil)
		for deadline := time.Now().Add(15 * time.Second); ; {
			if _, err := os.Stat(args[1] + ".captured"); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("controller did not retain descendant identity")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	time.Sleep(5 * time.Minute)
}
