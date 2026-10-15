//go:build !windows

package pty

import (
	unixpty "github.com/creack/pty"
	"os"
	"os/exec"
	"syscall"
)

type unixTerminal struct {
	file *os.File
	cmd  *exec.Cmd
}

func start(shell, cwd string, env []string, cols, rows int) (terminal, error) {
	cmd := exec.Command(shell)
	cmd.Dir = cwd
	cmd.Env = append(env, "TERM=xterm-256color")
	f, e := unixpty.StartWithSize(cmd, &unixpty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if e != nil {
		return nil, e
	}
	return &unixTerminal{f, cmd}, nil
}
func (t *unixTerminal) Read(b []byte) (int, error)  { return t.file.Read(b) }
func (t *unixTerminal) Write(b []byte) (int, error) { return t.file.Write(b) }
func (t *unixTerminal) Close() error                { return t.file.Close() }
func (t *unixTerminal) Resize(c, r int) error {
	return unixpty.Setsize(t.file, &unixpty.Winsize{Cols: uint16(c), Rows: uint16(r)})
}
func (t *unixTerminal) Signal(s string) error {
	sig := syscall.SIGTERM
	if s == "INT" {
		sig = syscall.SIGINT
	}
	if s == "KILL" {
		sig = syscall.SIGKILL
	}
	e := syscall.Kill(-t.cmd.Process.Pid, sig)
	if e == syscall.ESRCH {
		return nil
	}
	return e
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
