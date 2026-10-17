package main

import (
	"os/exec"
	"testing"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/control"
)

// sleeper starts a process that outlives the test's waits, so the wait has something to block on.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("cmd.exe", "/C", "ping -n 60 127.0.0.1 >nul")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func endpointOf(pid int, started time.Time) control.Endpoint {
	return control.Endpoint{ControlEndpointFile: v1.ControlEndpointFile{PID: pid, StartedAt: v1.Time{Time: started}}}
}

func Test_WaitKernelExitBlocksWhileKernelRuns(t *testing.T) {
	cmd := sleeper(t)
	e := endpointOf(cmd.Process.Pid, time.Now().Add(-time.Second))
	if err := waitKernelExit(e, 300*time.Millisecond); err == nil {
		t.Fatal("a running kernel must not count as exited")
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if err := waitKernelExit(e, 10*time.Second); err != nil {
		t.Fatal("exited kernel should release the restart:", err)
	}
}

func Test_WaitKernelExitIgnoresReusedPID(t *testing.T) {
	cmd := sleeper(t)
	// The kernel that wrote this control.json started an hour before the process above was created,
	// so the PID now belongs to a different process and the old kernel is gone.
	e := endpointOf(cmd.Process.Pid, time.Now().Add(-time.Hour))
	if err := waitKernelExit(e, 300*time.Millisecond); err != nil {
		t.Fatal("a reused PID must not hold up the restart:", err)
	}
}
