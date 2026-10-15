//go:build windows

package pty

import (
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"
)

type windowsTerminal struct {
	mu            sync.Mutex // Protects the Job Object handle during cleanup.
	consoleMu     sync.Mutex
	input, output *os.File
	console       windows.Handle
	process       windows.ProcessInformation
	job           windows.Handle
	exited        chan struct{}
	consoleOnce   sync.Once
	closeOnce     sync.Once
}

var updateConsoleAttribute = windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")

func start(shell string, args []string, cwd string, env []string, cols, rows int, _ bool) (terminal, error) {
	var inR, inW, outR, outW windows.Handle
	if e := windows.CreatePipe(&inR, &inW, nil, 0); e != nil {
		return nil, e
	}
	defer windows.CloseHandle(inR)
	if e := windows.CreatePipe(&outR, &outW, nil, 0); e != nil {
		windows.CloseHandle(inW)
		return nil, e
	}
	defer windows.CloseHandle(outW)
	t := &windowsTerminal{input: os.NewFile(uintptr(inW), "conpty-input"), output: os.NewFile(uintptr(outR), "conpty-output"), exited: make(chan struct{})}
	started := false
	defer func() {
		if !started {
			t.closeJob()
			if t.process.Process != 0 {
				_ = windows.TerminateProcess(t.process.Process, 1)
				_ = windows.CloseHandle(t.process.Process)
			}
			if t.process.Thread != 0 {
				_ = windows.CloseHandle(t.process.Thread)
			}
			_ = t.Close()
		}
	}()
	var e error
	t.job, e = windows.CreateJobObject(nil, nil)
	if e != nil {
		return nil, e
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, e = windows.SetInformationJobObject(t.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); e != nil {
		return nil, e
	}
	if e := windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, inR, outW, 0, &t.console); e != nil {
		return nil, e
	}
	attrs, e := windows.NewProcThreadAttributeList(1)
	if e != nil {
		return nil, e
	}
	defer attrs.Delete()
	// This attribute takes the HPCON value itself, unlike attributes that take
	// a pointer to a value. Pass it as an opaque Win32 handle at the syscall boundary.
	if ok, _, callErr := updateConsoleAttribute.Call(uintptr(unsafe.Pointer(attrs.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(t.console), unsafe.Sizeof(t.console), 0, 0); ok == 0 {
		return nil, callErr
	}
	si := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attrs.List()}
	command, e := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{shell}, args...)))
	if e != nil {
		return nil, e
	}
	dir, e := windows.UTF16PtrFromString(cwd)
	if e != nil {
		return nil, e
	}
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	// Assign while suspended so every descendant inherits the job before the
	// shell can execute user code or launch background processes.
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	if e = windows.CreateProcess(nil, command, nil, nil, false, flags, &block[0], dir, &si.StartupInfo, &t.process); e != nil {
		return nil, e
	}
	if e = windows.AssignProcessToJobObject(t.job, t.process.Process); e != nil {
		return nil, e
	}
	if _, e = windows.ResumeThread(t.process.Thread); e != nil {
		return nil, e
	}
	windows.CloseHandle(t.process.Thread)
	t.process.Thread = 0
	started = true
	go func() {
		_, _ = windows.WaitForSingleObject(t.process.Process, windows.INFINITE)
		// A shell can exit while background children still hold the console.
		t.closeJob()
		_ = windows.CloseHandle(t.process.Process)
		close(t.exited)
		t.closeConsole()
	}()
	return t, nil
}
func (t *windowsTerminal) Read(b []byte) (int, error)  { return t.output.Read(b) }
func (t *windowsTerminal) Write(b []byte) (int, error) { return t.input.Write(b) }
func (t *windowsTerminal) Close() error {
	t.closeOnce.Do(func() {
		t.closeJob()
		t.closeConsole()
		_ = t.input.Close()
		_ = t.output.Close()
	})
	return nil
}
func (t *windowsTerminal) Resize(c, r int) error {
	t.consoleMu.Lock()
	defer t.consoleMu.Unlock()
	if t.console == 0 {
		return windows.ERROR_INVALID_HANDLE
	}
	return windows.ResizePseudoConsole(t.console, windows.Coord{X: int16(c), Y: int16(r)})
}
func (t *windowsTerminal) Signal(s string) error {
	switch s {
	case "KILL":
		return t.terminateJob()
	case "INT":
		// The Agent may run in a different console or login task. ConPTY input
		// delivers Ctrl-C to the foreground application in its own console.
		_, err := t.input.Write([]byte{3})
		return err
	default:
		// Graceful input must not delay the termination deadline if the pipe is
		// full or the foreground application is not accepting terminal input.
		go func() { _, _ = t.input.Write([]byte{3}) }()
		go func() {
			select {
			case <-t.exited:
				return
			case <-time.After(250 * time.Millisecond):
				_ = t.terminateJob()
			}
		}()
		return nil
	}
}

func (t *windowsTerminal) closeConsole() {
	t.consoleOnce.Do(func() {
		t.consoleMu.Lock()
		defer t.consoleMu.Unlock()
		if t.console != 0 {
			windows.ClosePseudoConsole(t.console)
			t.console = 0
		}
	})
}

func (t *windowsTerminal) terminateJob() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(t.job, 1)
}

func (t *windowsTerminal) closeJob() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}
func (t *windowsTerminal) Wait() error { <-t.exited; return nil }
func (t *windowsTerminal) PID() int    { return int(t.process.ProcessId) }
func defaultShell() (string, error) {
	if p, e := exec.LookPath("pwsh"); e == nil {
		return p, nil
	}
	return exec.LookPath("powershell")
}
