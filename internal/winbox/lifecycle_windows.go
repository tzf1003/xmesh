package winbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/sys/windows"
)

type workspaceExecutionOptions struct {
	Workspace, Journal  string
	MaxObjects, MaxJobs int
	Interval            time.Duration
	MaxEvents           int
	SupervisorCommand   []string
	// RequireWriterFence rejects any guard that cannot prove, through Verify,
	// that the pinned namespace/reparse/ACL boundary is unchanged. The guard is
	// still released by closeContext.
	RequireWriterFence bool
}

// workspaceExecutionPrepare is a trusted integration boundary. Before returning,
// it must coordinate external namespace/reparse/ACL writers for this workspace
// and its journal, and retain that coordination through successful Guard.Close.
// A nonnil closer alone does not establish this property. Host-crash protection
// requires an independently surviving guard; this in-host owner supplies none.
// A guard returned with an error is still owned and must support Close retry.
type workspaceExecutionPrepare func(context.Context, workspaceExecutionOptions) (io.Closer, error)

// workspaceImportPrepare freezes external source namespace/reparse/ACL writers
// before the lifecycle scans or moves the source. Its guard must also cover the
// imported identities after the move, through durable execution cleanup. The
// workspace's original guard continues to cover destination writers. Callbacks
// run under the lifecycle operation gate and must not reenter this owner.
type workspaceImportPrepare func(context.Context, string, string) (io.Closer, error)

// workspaceExecution composes the private journal, identity, workspace tracking,
// supervised process owner and monitor. A nonnil result accompanying an admission
// error retains all obligations for closeContext retry. It never admits a process
// before setup completes. Production L2 integration has a separate admission gate.
type workspaceExecution struct {
	operation chan struct{}
	lifetime  context.Context
	guard     io.Closer
	fence     writerVerifier // guard, when it proves the writer boundary.
	imports   []io.Closer
	maxEvents int
	journal   *grantJournal
	profile   *Profile
	workspace *workspaceGrant
	processes *executionProcesses
	monitor   *workspaceChangeMonitor
	ready     bool
	closed    bool
	unknown   int // Uncoordinated imports recorded durably before the journal closed.
	// checked and observed hold the one final descendant-security proof, taken
	// after reaping and before the first descriptor edit of cleanup. Retries keep
	// reporting its result without re-reading a stream that cleanup itself wrote.
	checked  bool
	observed error
}

func newWorkspaceExecution(ctx context.Context, options workspaceExecutionOptions, prepare workspaceExecutionPrepare) (*workspaceExecution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prepare == nil || options.MaxObjects < 1 || options.MaxObjects > 10000 || options.MaxJobs < 1 || options.MaxJobs > 256 ||
		options.Interval < 10*time.Millisecond || options.Interval > time.Minute || options.MaxEvents < 1 || options.MaxEvents > maxWorkspaceChangeEvents {
		return nil, fmt.Errorf("AppContainer workspace execution requires a coordinator and bounded tracking limits")
	}
	for _, path := range []string{options.Workspace, options.Journal} {
		if _, _, err := workspaceComponents(path); err != nil {
			return nil, err
		}
	}
	// The callback receives a copy so retaining or changing its command slice
	// cannot change the supervisor command subsequently used by this owner.
	prepared := options
	prepared.SupervisorCommand = append([]string(nil), options.SupervisorCommand...)
	guard, err := prepare(ctx, prepared)
	if guard == nil {
		return nil, errors.Join(err, fmt.Errorf("AppContainer workspace coordinator did not retain a guard"))
	}
	e := &workspaceExecution{operation: make(chan struct{}, 1), lifetime: ctx, guard: guard, maxEvents: options.MaxEvents}
	e.fence, _ = guard.(writerVerifier)
	if err == nil && options.RequireWriterFence && e.fence == nil {
		err = fmt.Errorf("AppContainer workspace execution requires a verified external writer fence")
	}
	if err != nil {
		return e, err
	}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	if err := e.verifyFence(ctx); err != nil {
		return e, err
	}
	e.journal, err = openBoundGrantJournal(ctx, options.Journal, true)
	if err != nil {
		return e, err
	}
	// An independent keeper verifies this exact journal's completion before it
	// agrees to release, so it must learn the journal's identity once.
	if binder, ok := guard.(interface {
		bindJournal(context.Context, *grantJournal) error
	}); ok {
		if err := binder.bindJournal(ctx, e.journal); err != nil {
			return e, err
		}
	}
	e.profile, err = newProfile(e.journal)
	if err != nil {
		return e, err
	}
	e.workspace, err = e.profile.grantWorkspace(ctx, options.Workspace, options.MaxObjects)
	if err != nil {
		return e, err
	}
	if err := beginWorkspaceChangeTracking(ctx, e.journal, e.profile.sid, options.MaxObjects); err != nil {
		return e, err
	}
	if err := e.sealFence(ctx); err != nil {
		return e, err
	}
	e.processes, err = newSupervisedExecutionProcesses(ctx, e.profile, options.MaxJobs, options.SupervisorCommand)
	if err != nil {
		return e, err
	}
	e.monitor, err = e.workspace.monitorChanges(ctx, e.processes, options.Interval, options.MaxEvents)
	if err != nil {
		return e, err
	}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	e.ready = true
	return e, nil
}

func (e *workspaceExecution) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil || e.operation == nil {
		return nil, fmt.Errorf("AppContainer workspace execution is unavailable")
	}
	select {
	case e.operation <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-e.operation
			return nil, err
		}
		return func() { <-e.operation }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Stream and console capabilities remain caller-owned, as do the returned
// process/thread handles. Both forms use the same serialized admission boundary.
func (e *workspaceExecution) start(ctx context.Context, args []string, directory string, environment []string, stdio *executionStdio, console windows.Handle) (windows.ProcessInformation, error) {
	release, err := e.acquire(ctx)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	defer release()
	if !e.ready || e.closed || (stdio != nil && console != 0) {
		return windows.ProcessInformation{}, fmt.Errorf("AppContainer workspace execution admission is unavailable")
	}
	if err := e.lifetime.Err(); err != nil {
		return windows.ProcessInformation{}, err
	}
	if err := e.verifyFence(ctx); err != nil {
		return windows.ProcessInformation{}, err
	}
	if err := e.workspace.verifyDescendantSecurity(ctx, e.maxEvents, true); err != nil {
		return windows.ProcessInformation{}, err
	}
	return e.processes.startWithIO(ctx, args, directory, environment, stdio, console)
}

// moveImport shares launch/close admission and retains every returned source
// guard, including one accompanying a preparation error. A failed operation can
// already own a durable import census even when no rename occurred. Cleanup
// releases those guards only after all journal/profile obligations complete.
func (e *workspaceExecution) moveImport(ctx context.Context, source, destination string, prepare workspaceImportPrepare) (bool, error) {
	release, err := e.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	if !e.ready || e.closed || e.workspace == nil || prepare == nil || len(e.imports) >= 256 {
		return false, fmt.Errorf("AppContainer execution import admission is unavailable or at its guard limit")
	}
	if err := e.lifetime.Err(); err != nil {
		return false, err
	}
	if err := e.verifyFence(ctx); err != nil {
		return false, err
	}
	for _, path := range []string{source, destination} {
		if _, _, err := workspaceComponents(path); err != nil {
			return false, err
		}
	}
	// Acquire the workspace gate briefly to reject an already faulted monitor
	// before asking the trusted source coordinator to acquire more resources.
	check, err := e.workspace.acquire(ctx)
	if err != nil {
		return false, err
	}
	e.profile.mu.Lock()
	err = e.workspace.importReady(e.maxEvents)
	e.profile.mu.Unlock()
	check()
	if err != nil {
		return false, err
	}
	guard, err := prepare(ctx, source, destination)
	if guard != nil {
		e.imports = append(e.imports, guard)
	}
	if err != nil {
		return false, err
	}
	if guard == nil {
		return false, fmt.Errorf("AppContainer import coordinator did not retain a guard")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := e.lifetime.Err(); err != nil {
		return false, err
	}
	moved, err := e.workspace.moveImport(ctx, source, destination, e.maxEvents)
	if moved && err != nil {
		e.ready = false
	}
	return moved, err
}

// closeContext seals this owner before terminating Jobs, durably confirming the
// complete process census, stopping observation, reclaiming ACLs, deleting the
// profile and completing the journal. Every failed phase retains its owner and
// the external writer guard. A monitor fault is reported even if cleanup succeeds.
// Cancellation reaches the operation gates, census, monitor and ACL reclamation;
// synchronous Windows calls and journal flushes finish once entered.
func (e *workspaceExecution) closeContext(ctx context.Context, exitCode uint32) error {
	release, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if e.closed {
		return nil
	}
	e.ready = false
	if e.processes != nil {
		if err := e.processes.reap(ctx, exitCode); err != nil {
			return err
		}
	}
	// A violated boundary does not stop cleanup: ACLs and the profile must still
	// be reclaimed by identity. The violation is reported with the result.
	var observationErr error
	if err := e.verifyFence(ctx); err != nil {
		if !errors.Is(err, errWriterFenceViolated) {
			return err
		}
		observationErr = err
	}
	if e.monitor != nil {
		observationErr = errors.Join(observationErr, e.monitor.Stop(ctx))
		p := e.monitor.workspace.profile
		p.mu.Lock()
		attached := p.workspaceMonitor == e.monitor || e.monitor.workspace.monitor == e.monitor
		p.mu.Unlock()
		if attached {
			return observationErr
		}
		e.monitor = nil
	}
	// Every process is gone and no cleanup edit has happened yet, so any further
	// descriptor change on the admitted snapshot is an external writer's.
	if e.workspace != nil && !e.checked {
		err := e.workspace.finalSecurityCheck(ctx, e.maxEvents)
		if err != nil && !errors.Is(err, errWriterFenceViolated) && !errors.Is(err, errWorkspaceUnknownImport) {
			return errors.Join(observationErr, err)
		}
		e.checked, e.observed = true, err
	}
	observationErr = errors.Join(observationErr, e.observed)
	// Finish observation before releasing its process owner. Cancellation can
	// make a live monitor request reap; a stopped monitor cannot send a request
	// to a supervisor that is already completing its close acknowledgement.
	if e.processes != nil {
		if err := e.processes.closeContext(ctx); err != nil {
			return errors.Join(observationErr, err)
		}
		e.processes = nil
	}
	if e.workspace != nil {
		// Reclamation edits the root's descriptor; from here a retry must not
		// read this package's own edit as a foreign one.
		if e.fence != nil {
			e.fence.unsealRoot()
		}
		if err := e.workspace.closeContext(ctx); err != nil {
			return errors.Join(observationErr, err)
		}
		e.workspace = nil
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(observationErr, err)
	}
	if e.profile != nil {
		if err := e.profile.Close(); err != nil {
			return errors.Join(observationErr, err)
		}
		e.profile = nil
	}
	if e.journal != nil {
		if e.journal.blocked != nil {
			return errors.Join(observationErr, e.journal.blocked)
		}
		// Profile creation can leave only a durable identity intent. There were
		// no ACL grants or launches; recovery rederives that identity and commits
		// deletion before any journal or external coordination is released.
		if len(e.journal.records) != 0 && !e.journal.state.complete {
			if err := recoverSelectedJournalVolumes(e.journal, nil); err != nil {
				return errors.Join(observationErr, err)
			}
		}
		e.unknown = len(e.journal.state.unknown)
		if err := e.journal.Close(); err != nil {
			return errors.Join(observationErr, err)
		}
		e.journal = nil
	}
	for len(e.imports) != 0 {
		if err := ctx.Err(); err != nil {
			return errors.Join(observationErr, err)
		}
		last := len(e.imports) - 1
		if err := e.imports[last].Close(); err != nil {
			return errors.Join(observationErr, err)
		}
		e.imports[last] = nil
		e.imports = e.imports[:last]
	}
	if e.guard != nil {
		if err := e.guard.Close(); err != nil {
			return errors.Join(observationErr, err)
		}
		e.guard = nil
	}
	e.closed = true
	// Recorded imports were reclaimed by identity, but their prior subtree and
	// writers were never proved. Report them even after durable completion.
	if e.unknown != 0 && !errors.Is(observationErr, errWorkspaceUnknownImport) {
		observationErr = errors.Join(observationErr, fmt.Errorf("%w: %d recorded identities", errWorkspaceUnknownImport, e.unknown))
	}
	return observationErr
}
