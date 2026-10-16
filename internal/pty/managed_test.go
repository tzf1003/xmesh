//go:build !windows

package pty

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/sandbox"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type fakeTerminalRunner struct {
	mu         sync.Mutex
	workspace  string
	spec       sandbox.Spec
	cols, rows int
	terminal   *fakeManagedTerminal
	err        error
}

func (r *fakeTerminalRunner) Start(context.Context, string, sandbox.Spec, sandbox.Streams) (sandbox.Process, error) {
	return nil, errors.New("not used")
}

func (r *fakeTerminalRunner) StartTerminal(_ context.Context, workspace string, spec sandbox.Spec, cols, rows int) (sandbox.Terminal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	r.workspace, r.spec, r.cols, r.rows = workspace, spec, cols, rows
	reader, writer := io.Pipe()
	r.terminal = &fakeManagedTerminal{reader: reader, writer: writer, exited: make(chan struct{})}
	return r.terminal, nil
}

type fakeManagedTerminal struct {
	reader     *io.PipeReader
	writer     *io.PipeWriter
	exited     chan struct{}
	once       sync.Once
	mu         sync.Mutex
	resized    [2]int
	signals    []string
	closeCalls int
}

func (f *fakeManagedTerminal) Read(b []byte) (int, error) { return f.reader.Read(b) }
func (f *fakeManagedTerminal) Write(b []byte) (int, error) {
	// Echo with a marker so the test can see input reached the terminal.
	go func() { _, _ = f.writer.Write(append([]byte("echo:"), b...)) }()
	return len(b), nil
}
func (f *fakeManagedTerminal) Resize(c, r int) error {
	f.mu.Lock()
	f.resized = [2]int{c, r}
	f.mu.Unlock()
	return nil
}
func (f *fakeManagedTerminal) Signal(s string) error {
	f.mu.Lock()
	f.signals = append(f.signals, s)
	f.mu.Unlock()
	if s == "KILL" || s == "TERM" {
		f.end()
	}
	return nil
}
func (f *fakeManagedTerminal) end() {
	f.once.Do(func() { close(f.exited); _ = f.writer.Close() })
}
func (f *fakeManagedTerminal) Wait() error { <-f.exited; return nil }
func (f *fakeManagedTerminal) PID() int    { return 4242 }
func (f *fakeManagedTerminal) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	f.end()
	return nil
}

func managedPTYFixture(t *testing.T, runner sandbox.Runner) (*Manager, *cwd.Resolver, *policy.Engine) {
	t.Helper()
	m, r, _, _ := fixture(t)
	p, _ := policy.New(config.Policy{Isolation: "L2"})
	p.UseSandboxRunner(runner)
	return m, r, p
}

func Test_ManagedRunnerOwnsTerminal(t *testing.T) {
	runner := &fakeTerminalRunner{}
	m, r, p := managedPTYFixture(t, runner)
	c := identity.Caller{ID: "node/session"}
	o, e := invoke(m, r, p, c, "pty.open", Object{"cols": 100, "rows": 30, "shell": "/bin/sh"})
	if e != nil || o["pid"] != 4242 {
		t.Fatal(o, e)
	}
	if runner.workspace != r.Default || runner.spec.Dir != r.Default || len(runner.spec.Args) != 1 || runner.spec.Args[0] != "/bin/sh" || runner.cols != 100 || runner.rows != 30 {
		t.Fatal("terminal request", runner.workspace, runner.spec, runner.cols, runner.rows)
	}
	id := o["session_id"].(string)
	if _, e = invoke(m, r, p, c, "pty.write", Object{"session_id": id, "data": "ls\n"}); e != nil {
		t.Fatal(e)
	}
	if _, e = invoke(m, r, p, c, "pty.resize", Object{"session_id": id, "cols": 120, "rows": 40}); e != nil {
		t.Fatal(e)
	}
	var text strings.Builder
	cursor := uint64(0)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !strings.Contains(text.String(), "echo:ls"); {
		o, e = invoke(m, r, p, c, "pty.read", Object{"session_id": id, "cursor": cursor, "wait_ms": 50})
		if e != nil {
			t.Fatal(e)
		}
		text.WriteString(o["data"].(string))
		cursor = o["next_cursor"].(uint64)
	}
	if !strings.Contains(text.String(), "echo:ls") || runner.terminal.resized != [2]int{120, 40} {
		t.Fatal("terminal i/o", text.String(), runner.terminal.resized)
	}
	if _, e = invoke(m, r, p, c, "pty.close", Object{"session_id": id}); e != nil {
		t.Fatal(e)
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		runner.terminal.mu.Lock()
		closed := runner.terminal.closeCalls
		runner.terminal.mu.Unlock()
		if closed > 0 {
			return
		}
	}
	t.Fatal("session end did not close the managed terminal")
}

func Test_ManagedRunnerTerminalStartFailure(t *testing.T) {
	runner := &fakeTerminalRunner{err: errors.New("workspace admission refused")}
	m, r, p := managedPTYFixture(t, runner)
	c := identity.Caller{ID: "node/session"}
	_, e := invoke(m, r, p, c, "pty.open", Object{"cols": 80, "rows": 24, "shell": "/bin/sh"})
	if e == nil || e.(*toolerr.Error).Code != "internal" {
		t.Fatal(e)
	}
	m.mu.Lock()
	sessions, owned := len(m.sessions), m.byCaller[c.ID]
	m.mu.Unlock()
	if sessions != 0 || owned != 0 {
		t.Fatal("failed start retained quota", sessions, owned)
	}
}
