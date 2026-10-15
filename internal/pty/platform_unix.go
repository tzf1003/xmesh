//go:build !windows

package pty

import (
	unixpty "github.com/creack/pty"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type unixTerminal struct {
	mu   sync.Mutex // Serializes descriptor operations with Close.
	file *os.File
	cmd  *exec.Cmd
}

func start(shell string, args []string, cwd string, env []string, cols, rows int) (terminal, error) {
	cmd := exec.Command(shell, args...)
	cmd.Dir = cwd
	cmd.Env = append(env, "TERM=xterm-256color")
	f, e := unixpty.StartWithSize(cmd, &unixpty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if e != nil {
		return nil, e
	}
	return &unixTerminal{file: f, cmd: cmd}, nil
}
func (t *unixTerminal) Read(b []byte) (int, error)  { return t.file.Read(b) }
func (t *unixTerminal) Write(b []byte) (int, error) { return t.file.Write(b) }
func (t *unixTerminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.file.Close()
}
func (t *unixTerminal) Resize(c, r int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return unixpty.Setsize(t.file, &unixpty.Winsize{Cols: uint16(c), Rows: uint16(r)})
}
func (t *unixTerminal) Signal(s string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	sig := syscall.SIGTERM
	if s == "INT" {
		sig = syscall.SIGINT
	}
	if s == "KILL" {
		sig = syscall.SIGKILL
	}
	foreground, _ := unix.IoctlGetInt(int(t.file.Fd()), unix.TIOCGPGRP)
	groups := map[int]bool{}
	if foreground > 1 {
		groups[foreground] = true
	}
	if s != "INT" || foreground <= 1 {
		groups[t.cmd.Process.Pid] = true
	}
	if s != "INT" {
		for _, group := range sessionGroups(t.cmd.Process.Pid) {
			groups[group] = true
		}
	}
	var result error
	for group := range groups {
		if group <= 1 || group == syscall.Getpgrp() {
			continue
		}
		if e := syscall.Kill(-group, sig); e != nil && e != syscall.ESRCH {
			result = e
		}
	}
	return result
}
func (t *unixTerminal) Wait() error { return t.cmd.Wait() }
func (t *unixTerminal) PID() int    { return t.cmd.Process.Pid }
func defaultShell() (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return exec.LookPath(shell)
}
