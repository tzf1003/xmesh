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
	input, output *os.File
	console       windows.Handle
	process       windows.ProcessInformation
	exited        chan struct{}
	consoleOnce   sync.Once
}

func start(shell, cwd string, env []string, cols, rows int) (terminal, error) {
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
	if e := windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, inR, outW, 0, &t.console); e != nil {
		t.input.Close()
		t.output.Close()
		return nil, e
	}
	attrs, e := windows.NewProcThreadAttributeList(1)
	if e != nil {
		t.Close()
		return nil, e
	}
	defer attrs.Delete()
	if e = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, unsafe.Pointer(t.console), unsafe.Sizeof(t.console)); e != nil {
		t.Close()
		return nil, e
	}
	si := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attrs.List()}
	command, e := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{shell}))
	if e != nil {
		t.Close()
		return nil, e
	}
	dir, _ := windows.UTF16PtrFromString(cwd)
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP)
	if e = windows.CreateProcess(nil, command, nil, nil, false, flags, &block[0], dir, &si.StartupInfo, &t.process); e != nil {
		t.Close()
		return nil, e
	}
	windows.CloseHandle(t.process.Thread)
	go func() {
		_, _ = windows.WaitForSingleObject(t.process.Process, windows.INFINITE)
		close(t.exited)
		t.consoleOnce.Do(func() { windows.ClosePseudoConsole(t.console) })
	}()
	return t, nil
}
func (t *windowsTerminal) Read(b []byte) (int, error)  { return t.output.Read(b) }
func (t *windowsTerminal) Write(b []byte) (int, error) { return t.input.Write(b) }
func (t *windowsTerminal) Close() error {
	t.consoleOnce.Do(func() { windows.ClosePseudoConsole(t.console) })
	t.input.Close()
	e := t.output.Close()
	if t.process.Process != 0 {
		windows.CloseHandle(t.process.Process)
	}
	return e
}
func (t *windowsTerminal) Resize(c, r int) error {
	return windows.ResizePseudoConsole(t.console, windows.Coord{X: int16(c), Y: int16(r)})
}
func (t *windowsTerminal) Signal(s string) error {
	switch s {
	case "KILL":
		return windows.TerminateProcess(t.process.Process, 1)
	case "INT":
		return windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, t.process.ProcessId)
	default:
		e := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, t.process.ProcessId)
		go func() {
			select {
			case <-t.exited:
				return
			case <-time.After(250 * time.Millisecond):
				_ = windows.TerminateProcess(t.process.Process, 1)
			}
		}()
		return e
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
