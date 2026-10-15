//go:build windows

package exec

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var processIDOfThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

func prepare(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED}
}
func track(c *exec.Cmd) (func(), error) {
	job, e := windows.CreateJobObject(nil, nil)
	if e != nil {
		return nil, e
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, e = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); e != nil {
		windows.CloseHandle(job)
		return nil, e
	}
	process, e := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(c.Process.Pid))
	if e != nil {
		windows.CloseHandle(job)
		return nil, e
	}
	if e = windows.AssignProcessToJobObject(job, process); e != nil {
		windows.CloseHandle(process)
		windows.CloseHandle(job)
		return nil, e
	}
	// os/exec closes the primary thread handle returned by CreateProcess. The
	// suspended process has not executed its loader, so it must have one thread.
	if e = resumeSuspendedProcess(uint32(c.Process.Pid)); e != nil {
		windows.CloseHandle(job)
		windows.CloseHandle(process)
		return nil, e
	}
	var once sync.Once
	closeJob := func() { once.Do(func() { _ = windows.CloseHandle(job) }) }
	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		defer windows.CloseHandle(process)
		_, _ = windows.WaitForSingleObject(process, windows.INFINITE)
		// Reap descendants as soon as the root exits. In particular, a child
		// holding stdin open must not keep Cmd.Wait's copy goroutine alive.
		closeJob()
	}()
	return func() { closeJob(); <-reaped }, nil
}

func resumeSuspendedProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	var threadID uint32
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == pid {
			if threadID != 0 {
				return fmt.Errorf("suspended process %d has multiple threads", pid)
			}
			threadID = entry.ThreadID
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return err
	}
	if threadID == 0 {
		return fmt.Errorf("suspended process %d has no primary thread", pid)
	}
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_LIMITED_INFORMATION, false, threadID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(thread)
	owner, _, callErr := processIDOfThread.Call(uintptr(thread))
	if owner == 0 {
		return callErr
	}
	if uint32(owner) != pid {
		return fmt.Errorf("primary thread process identity changed")
	}
	previous, err := windows.ResumeThread(thread)
	if err != nil {
		return err
	}
	if previous != 1 {
		return fmt.Errorf("unexpected primary thread suspend count: %d", previous)
	}
	return nil
}
func terminate(c *exec.Cmd) {
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(c.Process.Pid))
}
func kill(c *exec.Cmd) { _ = c.Process.Kill() }
func exitCode(c *exec.Cmd, e error) int {
	if e == nil {
		return 0
	}
	if c.ProcessState != nil {
		return c.ProcessState.ExitCode()
	}
	return 1
}
