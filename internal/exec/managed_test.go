//go:build !windows

package exec

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/sandbox"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// fakeRunner stands in for a managed isolation runner by starting the command
// directly. It records what the tool layer asked for and how the execution ended.
type fakeRunner struct {
	mu         sync.Mutex
	workspaces []string
	specs      []sandbox.Spec
	closes     int
	closeErr   error
	startErr   error
}

type fakeProcess struct {
	r    *fakeRunner
	cmd  *osexec.Cmd
	done chan struct{}
	code int
}

func (r *fakeRunner) Start(ctx context.Context, workspace string, spec sandbox.Spec, streams sandbox.Streams) (sandbox.Process, error) {
	r.mu.Lock()
	r.workspaces = append(r.workspaces, workspace)
	r.specs = append(r.specs, spec)
	startErr := r.startErr
	r.mu.Unlock()
	if startErr != nil {
		return nil, startErr
	}
	cmd := osexec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir, cmd.Env = spec.Dir, spec.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = streams.Stdin, streams.Stdout, streams.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &fakeProcess{r: r, cmd: cmd, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		err := cmd.Wait()
		p.code = cmd.ProcessState.ExitCode()
		_ = err
	}()
	return p, nil
}

func (r *fakeRunner) StartTerminal(context.Context, string, sandbox.Spec, int, int) (sandbox.Terminal, error) {
	return nil, errors.New("not used")
}

func (p *fakeProcess) PID() int { return p.cmd.Process.Pid }
func (p *fakeProcess) Wait() (int, error) {
	<-p.done
	return p.code, nil
}
func (p *fakeProcess) Close(context.Context) error {
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	<-p.done
	p.r.mu.Lock()
	defer p.r.mu.Unlock()
	p.r.closes++
	return p.r.closeErr
}

func managedFixture(t *testing.T, runner sandbox.Runner) (*Manager, *cwd.Resolver, *policy.Engine) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	r, _ := cwd.New(root, nil, cwd.Policy{Allow: []string{root}})
	p, _ := policy.New(config.Policy{Isolation: "L2"})
	p.UseSandboxRunner(runner)
	m, e := New(config.Exec{MaxConcurrent: 2, ExclusiveSlots: 1, DefaultTimeout: "5s", IdempotencyCache: 10, InlineOutputBytes: "1KiB"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(m.Close)
	return m, r, p
}

func Test_ManagedRunnerOwnsExecution(t *testing.T) {
	runner := &fakeRunner{}
	m, r, p := managedFixture(t, runner)
	o, e := run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": []string{"-c", "cat; printf err >&2; exit 7"}, "stdin": "input-through-runner"})
	if e != nil || o["exit_code"] != 7 || o["stdout"] != "input-through-runner" || o["stderr"] != "err" {
		t.Fatal(o, e)
	}
	if len(runner.specs) != 1 || runner.closes != 1 || runner.workspaces[0] != r.Default || runner.specs[0].Dir != r.Default {
		t.Fatal("runner ownership", runner.workspaces, runner.specs, runner.closes)
	}
	if got := runner.specs[0].Args; got[0] != "/bin/sh" || got[1] != "-c" || len(got) != 3 {
		t.Fatal("resolved command", got)
	}
}

func Test_ManagedRunnerResolvesBeforeStart(t *testing.T) {
	runner := &fakeRunner{}
	m, r, p := managedFixture(t, runner)
	if _, e := run(m, r, p, "n/a", Object{"cmd": "xmesh-command-that-does-not-exist"}); e == nil || e.(*toolerr.Error).Code != "invalid_args" {
		t.Fatal(e)
	}
	if len(runner.specs) != 0 {
		t.Fatal("unresolved command reached the runner")
	}
	tool := filepath.Join(r.Default, "tool.sh")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf workspace-tool"), 0700); err != nil {
		t.Fatal(err)
	}
	o, e := run(m, r, p, "n/a", Object{"cmd": "./tool.sh"})
	if e != nil || o["stdout"] != "workspace-tool" || runner.specs[0].Args[0] != tool {
		t.Fatal(o, e, runner.specs)
	}
}

func Test_ManagedRunnerTimeoutClosesExecution(t *testing.T) {
	runner := &fakeRunner{}
	m, r, p := managedFixture(t, runner)
	_, e := run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": []string{"-c", "sleep 30"}, "timeout": 200})
	if e == nil || e.(*toolerr.Error).Code != "timeout" {
		t.Fatal(e)
	}
	if runner.closes != 1 {
		t.Fatal("timed out execution was not closed", runner.closes)
	}
}

func Test_ManagedRunnerCleanupFailureIsReported(t *testing.T) {
	runner := &fakeRunner{closeErr: errors.New("reclaim incomplete")}
	m, r, p := managedFixture(t, runner)
	o, e := run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "args": []string{"-c", "printf done"}})
	if e == nil || o != nil || e.(*toolerr.Error).Code != "internal" {
		t.Fatal("incomplete cleanup was hidden behind the command result", o, e)
	}
}

func Test_ManagedRunnerStartFailure(t *testing.T) {
	runner := &fakeRunner{startErr: errors.New("workspace admission refused")}
	m, r, p := managedFixture(t, runner)
	if _, e := run(m, r, p, "n/a", Object{"cmd": "/bin/sh"}); e == nil || e.(*toolerr.Error).Code != "internal" {
		t.Fatal(e)
	}
}

func Test_ManagedRunnerDeadlineDuringStart(t *testing.T) {
	blocked := make(chan struct{})
	runner := &blockingStartRunner{blocked: blocked}
	m, r, p := managedFixture(t, runner)
	start := time.Now()
	_, e := run(m, r, p, "n/a", Object{"cmd": "/bin/sh", "timeout": 100})
	if e == nil || e.(*toolerr.Error).Code != "timeout" || time.Since(start) > 3*time.Second {
		t.Fatal(e)
	}
}

type blockingStartRunner struct {
	fakeRunner
	blocked chan struct{}
}

func (b *blockingStartRunner) Start(ctx context.Context, _ string, _ sandbox.Spec, _ sandbox.Streams) (sandbox.Process, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func Test_SandboxRunnerOnlyForL2(t *testing.T) {
	p, _ := policy.New(config.Policy{Isolation: "L1"})
	p.UseSandboxRunner(&fakeRunner{})
	if p.SandboxRunner() != nil {
		t.Fatal("runner selected without L2")
	}
}
