//go:build !windows

package ci

import (
	"os"
	"os/exec"
	"syscall"
)

func prepare(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func adopt(*process)        {}

// terminate asks the process group to stop; kill ends it.
func (p *process) terminate() { _ = syscall.Kill(-p.pid(), syscall.SIGTERM) }
func (p *process) kill()      { _ = syscall.Kill(-p.pid(), syscall.SIGKILL) }

func processExit(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}
