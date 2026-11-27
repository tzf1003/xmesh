// Package delegate runs local agent CLIs for callers and injects messages into live Claude Code sessions
// (docs/contracts/delegate.md, CTR-DLG-01 to CTR-DLG-06). Tasks and their output live in memory only, and
// only the caller that created a task can see it.
package delegate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// Object is one tool result.
type Object = map[string]any

// Request is what one tool call sees: its caller, the configuration in force, the cwd resolver and the policy
// engine of the call. The Manager keeps none of them, so a reload applies to the next call.
type Request struct {
	Caller   string
	Settings config.Delegate
	Resolver *cwd.Resolver
	Policy   *policy.Engine
	// Gen is the value of Manager.Generation read before Settings was read. A run whose switch went off in between
	// registers nothing (CTR-DLG-06).
	Gen uint64
}

// End describes a task that has finished, for the audit log (CTR-DLG-06). It never carries the prompt, the
// result text or the output events.
type End struct {
	Caller       string
	TaskID       string
	Runner       string
	ResolvedPath string
	Mode         string
	State        string
	Duration     time.Duration
}

const (
	stateRunning   = "running"
	stateDone      = "done"
	stateFailed    = "failed"
	stateTimeout   = "timeout"
	stateCancelled = "cancelled"
)

const (
	maxFinished   = 64
	maxPromptSize = 64 << 10
	maxWait       = 60 * time.Second
	maxWaitMS     = 60000
	maxLimit      = 200
	defaultLimit  = 50
	// drainGrace bounds how long a finished command may keep its output open. A child that inherits the
	// pipes and outlives the command must not hold the task open.
	drainGrace = 2 * time.Second
	// cancelWait bounds how long delegate.cancel waits for the process tree to be gone.
	cancelWait = 10 * time.Second
)

// Manager holds the task table of one Agent: the running tasks, the finished ones kept for retention,
// the idempotency keys of both, and any active or retained session reply threads.
type Manager struct {
	mu              sync.Mutex
	tasks           map[string]*task
	keys            map[string]map[string]*task
	finished        []*task
	running         int
	replyThreads    map[string]*replyThread
	replyByMsg      map[string]*replyThread
	replies         map[string]*sessionReply
	finishedReplies []*replyThread
	nowFn           func() time.Time
	replyWindow     time.Duration
	approvalTTL     time.Duration
	closed          bool
	gen             uint64
	base            context.Context
	stop            context.CancelFunc
	wg              sync.WaitGroup
	onEnd           func(End)
}

// New returns an empty Manager. onEnd, when not nil, is called once for every task that ends.
func New(onEnd func(End)) *Manager {
	base, stop := context.WithCancel(context.Background())
	return &Manager{
		tasks:        map[string]*task{},
		keys:         map[string]map[string]*task{},
		replyThreads: map[string]*replyThread{},
		replyByMsg:   map[string]*replyThread{},
		replies:      map[string]*sessionReply{},
		nowFn:        time.Now,
		replyWindow:  defaultSessionReplyWindow,
		approvalTTL:  defaultReplyApprovalTTL,
		base:         base,
		stop:         stop,
		onEnd:        onEnd,
	}
}

// Tool serves one of the five delegate tools (CTR-DLG-01 to CTR-DLG-04). The Agent checks the switch,
// draining and authorization before it gets here.
func (m *Manager) Tool(ctx context.Context, r Request, tool string, raw json.RawMessage) (Object, error) {
	if !r.Settings.Enabled {
		return nil, disabledError()
	}
	switch tool {
	case "delegate.list":
		return m.list(r), nil
	case "delegate.run":
		return m.run(ctx, r, raw)
	case "delegate.output":
		return m.output(ctx, r, raw)
	case "delegate.send":
		return m.send(r, raw)
	case "delegate.cancel":
		return m.cancel(ctx, r, raw)
	}
	return nil, toolerr.New("not_found", "工具不存在", "name", tool)
}

// list implements delegate.list (CTR-DLG-01).
func (m *Manager) list(r Request) Object {
	runners := runnerViews(r.Settings, r.Resolver.Default)
	m.mu.Lock()
	now := m.now()
	m.pruneLocked(now)
	tasks := []Object{}
	for _, t := range m.tasks {
		if t.caller == r.Caller {
			tasks = append(tasks, t.summary(now))
		}
	}
	m.mu.Unlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i]["task_id"].(string) < tasks[j]["task_id"].(string) })
	sessions := []Object{}
	if r.Settings.SessionsAllowed() {
		for _, s := range discoverSessions(r.Resolver) {
			sessions = append(sessions, s.view())
		}
	}
	return Object{"runners": runners, "tasks": tasks, "sessions": sessions}
}

// owned returns the task with id when caller created it. Anything else is absent, so another caller
// cannot tell a foreign task from a missing one. An expired finished task is pruned first, so it is
// absent too (CTR-DLG-03). The caller holds m.mu.
func (m *Manager) owned(caller, id string) *task {
	m.pruneLocked(m.now())
	if t := m.tasks[id]; t != nil && t.caller == caller {
		return t
	}
	return nil
}

// CancelAll terminates every running task (CTR-DLG-06). It is used when the switch turns off; the Manager
// keeps accepting calls, which the switch refuses. It also advances the generation, so that a call which read the
// switch before it turned off registers nothing.
func (m *Manager) CancelAll() {
	m.mu.Lock()
	m.gen++
	var cancels []func()
	for _, t := range m.tasks {
		if !t.finished {
			t.markCancelled()
			cancels = append(cancels, t.cancelFn)
		}
	}
	m.cancelAllRepliesLocked(m.now())
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// Generation returns the switch generation, which CancelAll advances. The Agent reads it before it reads the
// configuration of a call, and passes it in Request.Gen (CTR-DLG-06).
func (m *Manager) Generation() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen
}

// disabledError is the refusal of every delegate tool while agent.delegate.enabled is off.
func disabledError() error {
	return toolerr.New("policy_denied", "Agent 委托已关闭", "rule", "delegate.disabled", "reason", "agent.delegate.enabled 为 false")
}

// Close terminates every task, waits until their processes are gone, and refuses new tasks. The Agent calls
// it on shutdown (CTR-DLG-06).
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	m.CancelAll()
	m.stop()
	m.wg.Wait()
}

// newID returns an opaque identifier: prefix followed by 16 random bytes in hex.
func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("delegate: random source failed: " + err.Error())
	}
	return prefix + hex.EncodeToString(b)
}

// pruneLocked drops the finished tasks whose retention has passed, then the oldest ones beyond the cap of
// finished tasks. The caller holds m.mu.
func (m *Manager) pruneLocked(now time.Time) {
	kept := m.finished[:0]
	for _, t := range m.finished {
		if now.Sub(t.ended) >= t.retain {
			m.evictLocked(t)
			continue
		}
		kept = append(kept, t)
	}
	m.finished = kept
	for len(m.finished) > maxFinished {
		m.evictLocked(m.finished[0])
		m.finished = m.finished[1:]
	}
	m.pruneRepliesLocked(now)
}

// evictLocked removes a finished task, its output and its idempotency key. The caller holds m.mu.
func (m *Manager) evictLocked(t *task) {
	delete(m.tasks, t.id)
	if t.key != "" && m.keys[t.caller][t.key] == t {
		delete(m.keys[t.caller], t.key)
	}
}

// finish records the end of a task. It runs after the command and its readers are done, and before the
// concurrency slot is given back.
func (m *Manager) finish(t *task, code int, timedOut bool, runErr error) {
	m.mu.Lock()
	now := m.now()
	t.ended = now
	t.exitCode = code
	switch {
	case t.cancelled:
		t.state = stateCancelled
	case timedOut:
		t.state = stateTimeout
	case runErr != nil || code != 0 || t.failed:
		t.state = stateFailed
	default:
		t.state = stateDone
	}
	t.finished = true
	t.result, t.resultCut = t.finalReply()
	m.running--
	m.finished = append(m.finished, t)
	m.pruneLocked(now)
	t.notifyLocked()
	close(t.done)
	end := End{Caller: t.caller, TaskID: t.id, Runner: t.runner, ResolvedPath: t.cwd, Mode: t.mode, State: t.state, Duration: now.Sub(t.started)}
	m.mu.Unlock()
	t.cancelFn()
	if m.onEnd != nil {
		m.onEnd(end)
	}
}

// discard removes a task that never started and gives its slot back.
func (m *Manager) discard(t *task) {
	m.mu.Lock()
	delete(m.tasks, t.id)
	if t.key != "" && m.keys[t.caller][t.key] == t {
		delete(m.keys[t.caller], t.key)
	}
	m.running--
	m.mu.Unlock()
	t.cancelFn()
}

// notifyLocked wakes every long poll waiting on the task. The caller holds m.mu.
func (t *task) notifyLocked() {
	close(t.changed)
	t.changed = make(chan struct{})
}

// waitFor blocks until the task ends, wait elapses, or ctx is done, whichever comes first.
func waitFor(ctx context.Context, done <-chan struct{}, wait time.Duration) {
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
}

// waitDuration converts a wait_ms argument to a duration. Values outside 0 to maxWaitMS are invalid_args.
func waitDuration(ms int64) (time.Duration, error) {
	if ms < 0 || ms > maxWaitMS {
		return 0, toolerr.Args("wait_ms")
	}
	return time.Duration(ms) * time.Millisecond, nil
}
