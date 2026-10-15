//go:build !windows

package exec

import (
	"os/exec"
	"syscall"
)

func prepare(c *exec.Cmd)               { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func track(c *exec.Cmd) (func(), error) { return func() {}, nil }
func terminate(c *exec.Cmd)             { _ = syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }
func kill(c *exec.Cmd)                  { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
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
