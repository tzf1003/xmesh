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
}

// workspaceExecutionPrepare is a trusted integration boundary. Before returning,
// it must coordinate external namespace/reparse/ACL writers for this workspace
// and its journal, and retain that coordination through successful Guard.Close.
// A nonnil closer alone does not establish this property. Host-crash protection
// requires an independently surviving guard; this in-host owner supplies none.
// A guard returned with an error is still owned and must support Close retry.
type workspaceExecutionPrepare func(context.Context, workspaceExecutionOptions) (io.Closer, error)

// workspaceExecution composes the private journal, identity, workspace tracking,
// supervised process owner and monitor. A nonnil result accompanying an admission
// error retains all obligations for closeContext retry. It never admits a process
// before setup completes. Production L2 integration has a separate admission gate.
type workspaceExecution struct {
	operation chan struct{}
	lifetime  context.Context
	guard     io.Closer
	journal   *grantJournal
	profile   *Profile
	workspace *workspaceGrant
	processes *executionProcesses
	monitor   *workspaceChangeMonitor
	ready     bool
	closed    bool
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
	e := &workspaceExecution{operation: make(chan struct{}, 1), lifetime: ctx, guard: guard}
	if err != nil {
		return e, err
	}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	e.journal, err = openBoundGrantJournal(ctx, options.Journal, true)
	if err != nil {
		return e, err
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
	return e.processes.startWithIO(ctx, args, directory, environment, stdio, console)
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
		if err := e.processes.closeContext(ctx); err != nil {
			return err
		}
		e.processes = nil
	}
	var observationErr error
	if e.monitor != nil {
		observationErr = e.monitor.Stop(ctx)
		p := e.monitor.workspace.profile
		p.mu.Lock()
		attached := p.workspaceMonitor == e.monitor || e.monitor.workspace.monitor == e.monitor
		p.mu.Unlock()
		if attached {
			return observationErr
		}
		e.monitor = nil
	}
	if e.workspace != nil {
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
		if err := e.journal.Close(); err != nil {
			return errors.Join(observationErr, err)
		}
		e.journal = nil
	}
	if e.guard != nil {
		if err := e.guard.Close(); err != nil {
			return errors.Join(observationErr, err)
		}
		e.guard = nil
	}
	e.closed = true
	return observationErr
}
