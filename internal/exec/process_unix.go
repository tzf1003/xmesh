//go:build !windows

package exec

import (
	"os/exec"
	"syscall"

	"github.com/tzf1003/xmesh/internal/delegateguard"
)

func startCommand(c *exec.Cmd) (func() error, func(), error) {
	return startCommandWithGuard(c, nil)
}

func startCommandWithGuard(c *exec.Cmd, guard *delegateguard.Guard) (func() error, func(), error) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	doneStart := guard.BeginStart()
	if err := c.Start(); err != nil {
		doneStart()
		return nil, nil, err
	}
	untrack := guard.Track(c.Process.Pid, 0)
	doneStart()
	return c.Wait, untrack, nil
}
func terminate(c *exec.Cmd) { _ = syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }
func kill(c *exec.Cmd)      { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
func exitCode(c *exec.Cmd, e error) int {
	if e == nil {
		return 0
	}
	if c.ProcessState != nil {
		if s, ok := c.ProcessState.Sys().(syscall.WaitStatus); ok && s.Signaled() {
			return 128 + int(s.Signal())
		}
		return c.ProcessState.ExitCode()
	}
	return 1
}
