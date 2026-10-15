//go:build !windows

package exec

import (
	"os/exec"
	"syscall"
)

func startCommand(c *exec.Cmd) (func() error, func(), error) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return nil, nil, err
	}
	return c.Wait, func() {}, nil
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
