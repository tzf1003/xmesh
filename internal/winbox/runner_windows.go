package winbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/sandbox"
	"golang.org/x/sys/windows"
)

// WorkspaceCoordinator is the trusted writer-coordination boundary. Before it
// returns, external namespace, reparse and ACL writers for the workspace and the
// journal must be frozen, and the returned guard must keep them frozen through a
// successful Close. A guard returned together with an error is still owned and
// must support Close retry. A non-nil closer alone does not establish this
// property; no coordinator is supplied by this package.
type WorkspaceCoordinator func(ctx context.Context, workspace, journal string) (io.Closer, error)

// RunnerConfig configures a managed AppContainer runner.
type RunnerConfig struct {
	// StateDir holds the per-execution journals. It is created as a private
	// directory when absent and verified when present.
	StateDir string
	// Coordinator freezes external writers for each execution. It is required.
	Coordinator WorkspaceCoordinator
	// RequireWriterFence refuses any coordinator guard that cannot verify the
	// external writer boundary before each launch. Production use sets it with
	// NewWorkspaceCoordinator.
	RequireWriterFence bool
	// SupervisorCommand starts the independent process supervisor. Nil runs the
	// current executable with "__sandbox-supervisor".
	SupervisorCommand []string
	// MaxObjects bounds the workspace census (default 10000).
	MaxObjects int
	// Interval is the workspace change observation period (default 500ms).
	Interval time.Duration
}

// Runner implements sandbox.Runner with a fresh AppContainer profile, workspace
// grant, change journal and supervised Job per execution. Executions whose
// cleanup did not complete are retained and retried before any new execution is
// admitted, so a failed reclaim fails closed instead of accumulating profiles.
type Runner struct {
	config  RunnerConfig
	mu      sync.Mutex
	active  map[*managedExecution]struct{}
	pending map[*managedExecution]struct{}
	closed  bool
}

var _ sandbox.Runner = (*Runner)(nil)

const (
	runnerDefaultObjects  = 10000
	runnerDefaultInterval = 500 * time.Millisecond
	runnerCleanupLimit    = 90 * time.Second
)

// NewRunner validates the configuration and prepares the private state directory.
func NewRunner(ctx context.Context, config RunnerConfig) (*Runner, error) {
	if config.Coordinator == nil {
		return nil, fmt.Errorf("AppContainer runner requires a trusted workspace coordinator")
	}
	if config.MaxObjects == 0 {
		config.MaxObjects = runnerDefaultObjects
	}
	if config.Interval == 0 {
		config.Interval = runnerDefaultInterval
	}
	config.SupervisorCommand = append([]string(nil), config.SupervisorCommand...)
	if len(config.SupervisorCommand) == 0 {
		config.SupervisorCommand = nil
	}
	if config.MaxObjects < 1 || config.MaxObjects > 10000 || config.Interval < 10*time.Millisecond || config.Interval > time.Minute {
		return nil, fmt.Errorf("AppContainer runner limits are out of range")
	}
	if _, _, err := workspaceComponents(config.StateDir); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(config.StateDir); os.IsNotExist(err) {
		directory, createErr := openJournalDirectory(ctx, config.StateDir, true)
		if createErr != nil {
			return nil, createErr
		}
		if err := directory.Close(); err != nil {
			return nil, err
		}
	}
	directory, err := openJournalDirectory(ctx, config.StateDir, false)
	if err != nil {
		return nil, err
	}
	if err := directory.Close(); err != nil {
		return nil, err
	}
	return &Runner{config: config, active: map[*managedExecution]struct{}{}, pending: map[*managedExecution]struct{}{}}, nil
}

// managedExecution is one workspaceExecution plus the bookkeeping needed to
// close it exactly once and to retry a failed close.
type managedExecution struct {
	runner  *Runner
	exec    *workspaceExecution
	journal string
	cancel  context.CancelFunc // ends the execution lifetime after a successful close
	mu      sync.Mutex
	done    bool
}

// close terminates and confirms every process, reclaims the workspace, deletes
// the profile and releases the writer guard. A failure keeps all obligations
// and parks the execution for retry.
func (m *managedExecution) close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return nil
	}
	if m.exec != nil {
		if err := m.exec.closeContext(ctx, 1); err != nil {
			m.runner.park(m)
			return err
		}
	}
	m.done = true
	if m.cancel != nil {
		m.cancel()
	}
	m.runner.release(m)
	// A completed journal has no recovery content. Remove it so the bounded
	// state directory does not fill; failure to do so is harmless.
	discardCompletedJournal(ctx, m.journal)
	return nil
}

func (r *Runner) park(m *managedExecution) {
	r.mu.Lock()
	delete(r.active, m)
	r.pending[m] = struct{}{}
	r.mu.Unlock()
}

func (r *Runner) release(m *managedExecution) {
	r.mu.Lock()
	delete(r.active, m)
	delete(r.pending, m)
	r.mu.Unlock()
}

func discardCompletedJournal(ctx context.Context, path string) {
	j, err := openBoundGrantJournal(ctx, path, false)
	if j == nil {
		return
	}
	complete := err == nil && j.blocked == nil && j.state.complete && len(j.state.pending) == 0
	if j.Close() == nil && complete {
		_ = os.Remove(path)
	}
}

// retry closes executions left over from failed cleanups. It returns an error
// while any remains, so new executions are not admitted on top of unreclaimed
// profiles or ACL grants.
func (r *Runner) retry(ctx context.Context) error {
	r.mu.Lock()
	var stale []*managedExecution
	for m := range r.pending {
		stale = append(stale, m)
	}
	r.mu.Unlock()
	var result error
	for _, m := range stale {
		result = errors.Join(result, m.close(ctx))
	}
	if result != nil {
		return fmt.Errorf("AppContainer runner has unfinished sandbox cleanup: %w", result)
	}
	return nil
}

// Close ends every live execution and retries failed cleanups. The runner admits
// no further executions. It returns the joined errors of executions that retain
// obligations.
func (r *Runner) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	var all []*managedExecution
	for m := range r.active {
		all = append(all, m)
	}
	for m := range r.pending {
		all = append(all, m)
	}
	r.mu.Unlock()
	var result error
	for _, m := range all {
		result = errors.Join(result, m.close(ctx))
	}
	return result
}

// open prepares an execution for the workspace. The returned execution's
// lifetime is independent of ctx once preparation finishes; ctx cancellation
// during preparation aborts it and the obligations it created are reclaimed.
func (r *Runner) open(ctx context.Context, workspace string) (*managedExecution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("AppContainer runner is closed")
	}
	if err := r.retry(ctx); err != nil {
		return nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	journal := filepath.Join(r.config.StateDir, hex.EncodeToString(random[:])+".journal")
	options := workspaceExecutionOptions{
		Workspace: workspace, Journal: journal, MaxObjects: r.config.MaxObjects, MaxJobs: 1,
		Interval: r.config.Interval, MaxEvents: maxWorkspaceChangeEvents, SupervisorCommand: r.config.SupervisorCommand,
		RequireWriterFence: r.config.RequireWriterFence,
	}
	// The execution observes this context for as long as it lives. It must not
	// end with the tool call that created it; only a call cancelled during
	// preparation propagates.
	lifetime, cancel := context.WithCancel(context.Background())
	detach := context.AfterFunc(ctx, cancel)
	e, err := newWorkspaceExecution(lifetime, options, func(ctx context.Context, o workspaceExecutionOptions) (io.Closer, error) {
		return r.config.Coordinator(ctx, o.Workspace, o.Journal)
	})
	m := &managedExecution{runner: r, exec: e, journal: journal, cancel: cancel}
	if err != nil || e == nil {
		detach()
		cleanup, release := context.WithTimeout(context.WithoutCancel(ctx), runnerCleanupLimit)
		defer release()
		if e != nil {
			if closeErr := m.close(cleanup); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		} else {
			cancel()
		}
		return nil, err
	}
	if !detach() {
		// The call was cancelled after preparation finished but before the
		// detach; the lifetime context is already cancelled and unusable.
		cleanup, release := context.WithTimeout(context.WithoutCancel(ctx), runnerCleanupLimit)
		defer release()
		err := m.close(cleanup)
		return nil, errors.Join(ctx.Err(), err)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cleanup, release := context.WithTimeout(context.WithoutCancel(ctx), runnerCleanupLimit)
		defer release()
		err := m.close(cleanup)
		return nil, errors.Join(fmt.Errorf("AppContainer runner is closed"), err)
	}
	r.active[m] = struct{}{}
	r.mu.Unlock()
	return m, nil
}

// Start implements sandbox.Runner.
func (r *Runner) Start(ctx context.Context, workspace string, spec sandbox.Spec, streams sandbox.Streams) (sandbox.Process, error) {
	if streams.Stdout == nil || streams.Stderr == nil {
		return nil, fmt.Errorf("AppContainer execution requires stdout and stderr")
	}
	stdin := streams.Stdin
	if stdin == nil {
		null, err := os.Open(os.DevNull)
		if err != nil {
			return nil, err
		}
		defer null.Close()
		stdin = null
	}
	m, err := r.open(ctx, workspace)
	if err != nil {
		return nil, err
	}
	stdio := &executionStdio{windows.Handle(stdin.Fd()), windows.Handle(streams.Stdout.Fd()), windows.Handle(streams.Stderr.Fd())}
	info, err := m.exec.start(ctx, spec.Args, spec.Dir, spec.Env, stdio, 0)
	if err != nil {
		return nil, m.abort(ctx, err)
	}
	_ = windows.CloseHandle(info.Thread)
	return newManagedProcess(m, info.Process, info.ProcessId), nil
}

// abort closes an execution after a failed start and joins any cleanup error.
func (m *managedExecution) abort(ctx context.Context, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), runnerCleanupLimit)
	defer cancel()
	if err := m.close(cleanup); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

type managedProcess struct {
	m       *managedExecution
	process windows.Handle
	pid     int
	done    chan struct{}
	code    uint32
	err     error
	once    sync.Once
}

func newManagedProcess(m *managedExecution, process windows.Handle, pid uint32) *managedProcess {
	p := &managedProcess{m: m, process: process, pid: int(pid), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		if result, err := windows.WaitForSingleObject(p.process, windows.INFINITE); err != nil || result != windows.WAIT_OBJECT_0 {
			p.err = fmt.Errorf("wait for AppContainer process: result=%d error=%v", result, err)
			return
		}
		p.err = windows.GetExitCodeProcess(p.process, &p.code)
	}()
	return p
}

func (p *managedProcess) PID() int { return p.pid }

func (p *managedProcess) Wait() (int, error) {
	<-p.done
	return int(p.code), p.err
}

// Close terminates the whole execution, then releases the root handle once the
// wait has observed the exit.
func (p *managedProcess) Close(ctx context.Context) error {
	err := p.m.close(ctx)
	select {
	case <-p.done:
		p.once.Do(func() { _ = windows.CloseHandle(p.process) })
	case <-ctx.Done():
		err = errors.Join(err, ctx.Err())
	}
	return err
}
