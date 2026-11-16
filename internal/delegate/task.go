package delegate

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

// maxEventText is the longest text one event keeps (CTR-DLG-03). A longer text is cut at a UTF-8 boundary.
const maxEventText = 8 << 10

// maxEventBytes bounds the event log and the fallback reply of one task, so a runner that prints without end
// cannot exhaust memory. When the bound is reached, one system event says so and the rest is dropped.
const maxEventBytes = 4 << 20

const budgetNotice = "输出超过单个任务的上限，之后的输出已省略"

// event is one entry of the output log of a task.
type event struct {
	seq       uint64
	typ       string
	text      string
	truncated bool
}

// task is one runner process and everything it has produced. Its fields are guarded by Manager.mu, except the
// channels, startErr and cancelFn, which are set before the task is shared or once the start is decided.
type task struct {
	id          string
	caller      string
	key         string
	runner      string
	kind        string
	cwd         string
	mode        string
	fingerprint string
	timeout     time.Duration
	retain      time.Duration
	started     time.Time
	ended       time.Time

	sessionID string
	state     string
	exitCode  int
	cancelled bool
	finished  bool
	failed    bool

	answer    string
	hasAnswer bool
	assistant strings.Builder
	result    string

	events      []event
	eventBytes  int
	budgetNoted bool

	ready     chan struct{}
	readyOnce sync.Once
	startErr  error

	changed  chan struct{}
	done     chan struct{}
	cancelFn context.CancelFunc
}

// stateLocked is the state a caller sees. A task whose cancellation was asked for reads as cancelled while its
// process tree is still being terminated. The caller holds m.mu.
func (t *task) stateLocked() string {
	switch {
	case t.finished:
		return t.state
	case t.cancelled:
		return stateCancelled
	}
	return stateRunning
}

// markCancelled records that the caller or the switch asked for the task to end. The caller holds m.mu.
func (t *task) markCancelled() { t.cancelled = true }

// setReady records the outcome of the start. Only the first call counts.
func (t *task) setReady(err error) {
	t.readyOnce.Do(func() {
		t.startErr = err
		close(t.ready)
	})
}

// finalReply is the reply of a finished task: the final result of the runner, or the assistant text when the
// runner gave no final result. The caller holds m.mu.
func (t *task) finalReply() string {
	if t.hasAnswer {
		return t.answer
	}
	return t.assistant.String()
}

// summary is the row of delegate.list. The caller holds m.mu.
func (t *task) summary(now time.Time) Object {
	end := now
	if t.finished {
		end = t.ended
	}
	out := Object{"task_id": t.id, "runner": t.runner, "state": t.stateLocked(), "cwd": t.cwd, "started_at": t.started.UTC().Format(time.RFC3339Nano), "duration_ms": end.Sub(t.started).Milliseconds()}
	if t.sessionID != "" {
		out["session_id"] = t.sessionID
	}
	return out
}

// runViewLocked is the reply of delegate.run. The caller holds m.mu.
func (t *task) runViewLocked() Object {
	out := Object{"task_id": t.id, "state": t.stateLocked(), "runner": t.runner, "resolved_path": t.cwd}
	if t.sessionID != "" {
		out["session_id"] = t.sessionID
	}
	if t.finished {
		out["result"] = t.result
		out["exit_code"] = t.exitCode
		out["duration_ms"] = t.ended.Sub(t.started).Milliseconds()
		out["next_cursor"] = int64(len(t.events))
	}
	return out
}

// pageLocked returns the events after cursor, at most limit of them. Event seq numbers start at 1 and count the
// events in order, so the event after cursor is the one at index cursor. The caller holds m.mu.
func (t *task) pageLocked(cursor int64, limit int) []event {
	start := int(min(cursor, int64(len(t.events))))
	end := min(start+limit, len(t.events))
	return t.events[start:end]
}

// outputLocked is the reply of delegate.output for one page. The caller holds m.mu.
func (t *task) outputLocked(cursor int64, page []event) Object {
	next := cursor
	events := []Object{}
	for _, e := range page {
		events = append(events, eventObject(e))
		next = int64(e.seq)
	}
	out := Object{"state": t.stateLocked(), "events": events, "next_cursor": next, "eof": t.finished && next >= int64(len(t.events))}
	if t.finished {
		out["result"] = t.result
		out["exit_code"] = t.exitCode
		out["duration_ms"] = t.ended.Sub(t.started).Milliseconds()
		if t.sessionID != "" {
			out["session_id"] = t.sessionID
		}
	}
	return out
}

func eventObject(e event) Object {
	out := Object{"seq": e.seq, "type": e.typ, "text": e.text}
	if e.truncated {
		out["truncated"] = true
	}
	return out
}

// appendLocked adds one event unless the output budget of the task is spent. It reports whether the event was
// kept. The caller holds m.mu.
func (t *task) appendLocked(typ, text string) bool {
	text, cut := clip(text)
	if t.eventBytes+len(text) > maxEventBytes {
		if t.budgetNoted {
			return false
		}
		t.budgetNoted = true
		typ, text, cut = "system", budgetNotice, false
	}
	t.eventBytes += len(text)
	t.events = append(t.events, event{seq: uint64(len(t.events)) + 1, typ: typ, text: text, truncated: cut})
	return true
}

// keepAssistantLocked adds assistant text to the fallback reply, within the same budget. The caller holds m.mu.
func (t *task) keepAssistantLocked(text string) {
	if t.assistant.Len()+len(text) <= maxEventBytes {
		t.assistant.WriteString(text)
	}
}

// clip cuts s to at most maxEventText bytes, at a UTF-8 character boundary, and reports whether it cut.
func clip(s string) (string, bool) {
	if len(s) <= maxEventText {
		return s, false
	}
	n := maxEventText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// output implements delegate.output (CTR-DLG-03). It waits up to wait_ms for an event after the cursor, and
// wakes as soon as the task produces one or ends.
func (m *Manager) output(ctx context.Context, r Request, raw json.RawMessage) (Object, error) {
	var a struct {
		TaskID string `json:"task_id"`
		Cursor int64  `json:"cursor"`
		WaitMS int64  `json:"wait_ms"`
		Limit  *int   `json:"limit"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return nil, toolerr.Args("arguments")
	}
	if a.TaskID == "" {
		return nil, toolerr.Args("task_id")
	}
	if a.Cursor < 0 {
		return nil, toolerr.Args("cursor")
	}
	limit := defaultLimit
	if a.Limit != nil {
		limit = *a.Limit
		if limit < 1 || limit > maxLimit {
			return nil, toolerr.Args("limit")
		}
	}
	wait, err := waitDuration(a.WaitMS)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	m.mu.Lock()
	t := m.owned(r.Caller, a.TaskID)
	if t == nil {
		m.mu.Unlock()
		return nil, notFound(a.TaskID)
	}
	for {
		page := t.pageLocked(a.Cursor, limit)
		left := time.Until(deadline)
		if len(page) > 0 || t.finished || left <= 0 || ctx.Err() != nil {
			out := t.outputLocked(a.Cursor, page)
			m.mu.Unlock()
			return out, nil
		}
		changed := t.changed
		m.mu.Unlock()
		timer := time.NewTimer(left)
		select {
		case <-changed:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
		m.mu.Lock()
	}
}

// cancel implements delegate.cancel (CTR-DLG-03). It terminates the process tree and waits, up to cancelWait,
// until the process is gone. On a finished task it changes nothing.
func (m *Manager) cancel(ctx context.Context, r Request, raw json.RawMessage) (Object, error) {
	var a struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(raw, &a) != nil || a.TaskID == "" {
		return nil, toolerr.Args("task_id")
	}
	m.mu.Lock()
	t := m.owned(r.Caller, a.TaskID)
	if t == nil {
		m.mu.Unlock()
		return nil, notFound(a.TaskID)
	}
	running := !t.finished
	if running {
		t.markCancelled()
	}
	stop, done := t.cancelFn, t.done
	m.mu.Unlock()
	if running {
		stop()
		waitFor(ctx, done, cancelWait)
	}
	m.mu.Lock()
	state := t.stateLocked()
	m.mu.Unlock()
	return Object{"task_id": t.id, "state": state}, nil
}

// notFound is the error for a task another caller created, or one that does not exist. The two are not told apart.
func notFound(taskID string) error {
	return toolerr.New("not_found", "任务不存在，或不属于当前调用方", "task_id", taskID)
}
