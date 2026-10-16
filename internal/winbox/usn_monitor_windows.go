package winbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var errWorkspaceMonitorActive = errors.New("AppContainer workspace retains a change monitor")

// The caller retains its external namespace/reparse/ACL writer guard throughout
// monitoring and reclamation. This owner serializes journal checkpoints with the
// live profile. It is attached before the execution admits its first Job.
type workspaceChangeMonitor struct {
	workspace *workspaceGrant
	owner     *executionProcesses
	interval  time.Duration
	maxEvents int
	cancel    context.CancelFunc
	stop      chan struct{}
	stopOnce  sync.Once
	done      chan struct{}
	err       error // Published by closing done.
}

func (g *workspaceGrant) monitorChanges(ctx context.Context, owner *executionProcesses, interval time.Duration, maxEvents int) (*workspaceChangeMonitor, error) {
	if owner == nil || interval < 10*time.Millisecond || interval > time.Minute || maxEvents < 1 || maxEvents > maxWorkspaceChangeEvents {
		return nil, fmt.Errorf("AppContainer change monitor requires an execution and bounded interval/events")
	}
	releaseOwner, err := owner.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseOwner()
	release, err := g.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	p := g.profile
	p.mu.Lock()
	defer p.mu.Unlock()
	if g.tree == nil || g.monitor != nil || owner.profile != p || p.processes != owner || p.processesConfirmed || owner.closed || owner.sealed || len(owner.jobs) != 0 || p.workspaceClosing || p.reclaiming || p.workspaceMonitor != nil || p.journal == nil || p.journal.state.changeCursor == nil {
		return nil, fmt.Errorf("AppContainer change monitor requires an active tracked workspace before process admission")
	}
	if err := validateWorkspaceJournalOwnership(p.journal, p.sid); err != nil {
		return nil, err
	}
	// A readable baseline precedes publication and any launch. A failed baseline
	// permanently closes this execution's admission; its intent still needs reap.
	if err := g.checkpointChanges(ctx, maxEvents); err != nil {
		p.workspaceClosing, owner.sealed = true, true
		return nil, err
	}
	watch, cancel := context.WithCancel(ctx)
	m := &workspaceChangeMonitor{workspace: g, owner: owner, interval: interval, maxEvents: maxEvents,
		cancel: cancel, stop: make(chan struct{}), done: make(chan struct{})}
	g.monitor, p.workspaceMonitor = m, m
	go m.run(watch)
	return m, nil
}

func (m *workspaceChangeMonitor) checkpoint(ctx context.Context) error {
	if check := m.workspace.writerCheck; check != nil {
		if err := check(ctx); err != nil {
			return err
		}
	}
	release, err := m.workspace.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	p := m.workspace.profile
	p.mu.Lock()
	defer p.mu.Unlock()
	if m.workspace.monitor != m || p.workspaceMonitor != m || m.workspace.tree == nil || p.workspaceClosing || p.reclaiming || (p.processes != m.owner && p.processes != nil) {
		return fmt.Errorf("AppContainer change monitor lost its workspace ownership")
	}
	return m.workspace.checkpointChanges(ctx, m.maxEvents)
}

func (m *workspaceChangeMonitor) run(ctx context.Context) {
	defer close(m.done)
	defer m.cancel()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		var err error
		select {
		case <-m.stop:
			return
		case <-ctx.Done():
			err = ctx.Err()
		case <-ticker.C:
			call, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = m.checkpoint(call)
			cancel()
		}
		if err == nil {
			continue
		}
		select {
		case <-m.stop:
			return
		default:
		}
		// Seal admission before waiting for the execution operation gate. A start
		// already past token setup is included when reap acquires that gate.
		p := m.workspace.profile
		p.mu.Lock()
		p.workspaceClosing = true
		p.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		reapErr := m.owner.reap(cleanup, 1)
		cancel()
		m.err = errors.Join(err, reapErr)
		return
	}
}

// Wait observes the terminal result; a timeout leaves the monitor owned and live.
func (m *workspaceChangeMonitor) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-m.done:
		return m.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop requires durable process confirmation, or an already terminal monitor.
// A timeout retains the attachment. Even after a fault, the caller must retry
// process confirmation and retain every guard until workspace/profile cleanup.
func (m *workspaceChangeMonitor) Stop(ctx context.Context) error {
	release, err := m.workspace.acquire(ctx)
	if err != nil {
		return err
	}
	p := m.workspace.profile
	p.mu.Lock()
	terminal := false
	select {
	case <-m.done:
		terminal = true
	default:
	}
	if !terminal && !p.processesConfirmed {
		p.mu.Unlock()
		release()
		return errJournalProcessesPending
	}
	m.stopOnce.Do(func() { close(m.stop); m.cancel() })
	p.mu.Unlock()
	release()
	result := m.Wait(ctx)
	select {
	case <-m.done:
	default:
		return result
	}
	release, err = m.workspace.acquire(ctx)
	if err != nil {
		return errors.Join(result, err)
	}
	defer release()
	p.mu.Lock()
	defer p.mu.Unlock()
	if m.workspace.monitor == m {
		m.workspace.monitor = nil
	}
	if p.workspaceMonitor == m {
		p.workspaceMonitor = nil
	}
	return result
}

// finalSecurityCheck is the last live checkpoint of an execution. It runs under
// the same gates as a monitor cycle, after the processes are confirmed gone, so
// a descriptor change made since the last cycle cannot be attributed to the
// sandbox. A finished or released workspace has nothing left to prove.
func (g *workspaceGrant) finalSecurityCheck(ctx context.Context, maxEvents int) error {
	return g.verifyDescendantSecurity(ctx, maxEvents, false)
}

// verifyDescendantSecurity reads the change stream up to now and fails if an
// object of the admitted snapshot changed descriptor. Admission calls it with
// the monitor attached so a change between monitor cycles cannot be followed by
// a launch; the final check requires the monitor to be gone.
func (g *workspaceGrant) verifyDescendantSecurity(ctx context.Context, maxEvents int, monitored bool) error {
	release, err := g.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	p := g.profile
	p.mu.Lock()
	defer p.mu.Unlock()
	j := p.journal
	if g.tree == nil || j == nil || j.state.changeCursor == nil || j.state.workspaceReleased || j.state.complete {
		return nil
	}
	if !monitored && (p.workspaceMonitor != nil || g.monitor != nil) {
		return nil
	}
	if err := validateWorkspaceJournalOwnership(j, p.sid); err != nil {
		return err
	}
	if err := g.verifyAllDescriptors(); err != nil {
		return err
	}
	return checkpointWorkspaceChangesPolicy(ctx, j, p.sid, g.maxObjects, maxEvents, g.verifyDescriptors)
}
