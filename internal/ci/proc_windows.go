//go:build windows

package ci

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/tzf1003/xmesh/internal/winjob"
)

var (
	jobOnce sync.Once
	job     windows.Handle
)

// runnerJob is one job for every runner process of this kernel: it kills them all, with their
// children, when the kernel goes away however it goes.
func runnerJob() windows.Handle {
	jobOnce.Do(func() {
		if h, err := winjob.New(); err == nil {
			job = h
		}
	})
	return job
}

func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// adopt puts the new process in the kernel's job. A process that cannot be assigned still runs;
// it is then stopped with taskkill.
func adopt(p *process) {
	h := runnerJob()
	if h == 0 {
		return
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.pid()))
	if err != nil {
		return
	}
	defer windows.CloseHandle(proc)
	_ = windows.AssignProcessToJobObject(h, proc)
}

// Windows has no graceful signal for a console process tree, so both stops end the tree. Callers
// only stop an instance that is idle, or after the stop timeout.
func (p *process) terminate() { p.kill() }
func (p *process) kill() {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.pid())).Run()
}

func processExit(state *os.ProcessState) int { return state.ExitCode() }
