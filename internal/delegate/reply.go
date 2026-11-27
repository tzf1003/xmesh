package delegate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

const (
	// MaxSessionReplyBytes is the hard UTF-8 byte limit of a single live-session reply draft (CTR-DLG-07).
	MaxSessionReplyBytes = 8 << 10

	defaultSessionReplyWindow = 30 * time.Minute
	defaultReplyApprovalTTL   = 10 * time.Minute
	maxPendingReplies         = 64

	stateRejected = "rejected"
	stateExpired  = "expired"

	replyStatePending   = "pending"
	replyStateApproved  = "approved"
	replyStateRejected  = "rejected"
	replyStateExpired   = "expired"
	replyStateCancelled = "cancelled"
)

// ReplySummary is the metadata-only view of one live-session reply draft (CTR-DLG-07).
// It never carries the reply body.
type ReplySummary struct {
	ReplyID   string
	TaskID    string
	MessageID string
	SessionID string
	Caller    string
	State     string
	Bytes     int
	Previewed bool
	CreatedAt time.Time
	ExpiresAt time.Time
	DecidedAt time.Time
}

// ReplyDetail is the local preview view of one live-session reply draft (CTR-DLG-07).
// Body is populated only while State is "pending".
type ReplyDetail struct {
	ReplySummary
	Body       string
	BodySHA256 string
}

// replyThread tracks one injected message that expects an explicit, locally confirmed reply (CTR-DLG-07).
type replyThread struct {
	id        string
	caller    string
	sessionID string
	cwd       string
	messageID string
	created   time.Time
	deadline  time.Time
	retain    time.Duration
	ended     time.Time

	state    string
	finished bool
	draft    *sessionReply
	events   []event
	result   string

	changed chan struct{}
	done    chan struct{}
}

// sessionReply is one explicit reply draft submitted by a live Claude Code session.
type sessionReply struct {
	id          string
	thread      *replyThread
	messageID   string
	taskID      string
	sessionID   string
	caller      string
	cwd         string
	body        string
	bodySHA256  string
	bytes       int
	state       string
	previewed   bool
	previewedAt time.Time
	created     time.Time
	expiresAt   time.Time
	decidedAt   time.Time
}

func (d *sessionReply) summary() ReplySummary {
	return ReplySummary{
		ReplyID:   d.id,
		TaskID:    d.taskID,
		MessageID: d.messageID,
		SessionID: d.sessionID,
		Caller:    d.caller,
		State:     d.state,
		Bytes:     d.bytes,
		Previewed: d.previewed,
		CreatedAt: d.created,
		ExpiresAt: d.expiresAt,
		DecidedAt: d.decidedAt,
	}
}

func (d *sessionReply) detail() ReplyDetail {
	body := ""
	if d.state == replyStatePending {
		body = d.body
	}
	return ReplyDetail{
		ReplySummary: d.summary(),
		Body:         body,
		BodySHA256:   d.bodySHA256,
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sessionsDisabledError() error {
	return toolerr.New("policy_denied", "活会话注入已关闭", "rule", "delegate.sessions_disabled", "reason", "agent.delegate.allow_sessions 未设为 true")
}

func sessionRepliesDisabledError() error {
	return toolerr.New("policy_denied", "活会话回信已关闭", "rule", "delegate.session_replies_disabled", "reason", "agent.delegate.allow_session_replies 未设为 true")
}

// SetReplyTTLs configures the reply submission window and the local confirmation TTL.
// Non-positive durations keep the current values. Intended for tests.
func (m *Manager) SetReplyTTLs(window, approval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if window > 0 {
		m.replyWindow = window
	}
	if approval > 0 {
		m.approvalTTL = approval
	}
}

// SetClock replaces the clock used by session reply expiration checks. Intended for tests.
func (m *Manager) SetClock(fn func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		m.nowFn = time.Now
		return
	}
	m.nowFn = fn
}

func (m *Manager) now() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

// CancelSessionReplies cancels every active reply thread and wipes all pending reply drafts.
// It is called when allow_sessions or allow_session_replies is turned off via hot reload (CTR-DLG-07).
func (m *Manager) CancelSessionReplies() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelAllRepliesLocked(m.now())
}

func (m *Manager) activeReplyCountLocked() int {
	n := 0
	for _, t := range m.replyThreads {
		if !t.finished {
			n++
		}
	}
	return n
}

func (m *Manager) checkReplyCapacity(r Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return toolerr.New("internal", "委托管理器正在关闭", "op", "delegate.send")
	}
	if m.gen != r.Gen {
		return disabledError()
	}
	m.pruneLocked(m.now())
	if active := m.activeReplyCountLocked(); active >= maxPendingReplies {
		return toolerr.New("delegate_saturated", "等待回信的注入任务已达上限", "in_use", active, "limit", maxPendingReplies)
	}
	return nil
}

func (m *Manager) registerReplyThread(r Request, sessionID, resolvedCWD, messageID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.gen != r.Gen {
		return "", false
	}
	now := m.now()
	m.pruneLocked(now)
	if m.activeReplyCountLocked() >= maxPendingReplies {
		return "", false
	}
	t := &replyThread{
		id:        newID("sr-"),
		caller:    r.Caller,
		sessionID: sessionID,
		cwd:       resolvedCWD,
		messageID: messageID,
		created:   now,
		deadline:  now.Add(m.replyWindow),
		retain:    r.Settings.RetainDuration(),
		state:     stateRunning,
		changed:   make(chan struct{}),
		done:      make(chan struct{}),
	}
	m.replyThreads[t.id] = t
	m.replyByMsg[messageID] = t
	return t.id, true
}

// ownedReplyLocked returns the reply thread with id when caller created it. The caller holds m.mu.
func (m *Manager) ownedReplyLocked(caller, id string) *replyThread {
	m.pruneLocked(m.now())
	if t := m.replyThreads[id]; t != nil && t.caller == caller {
		return t
	}
	return nil
}

func (t *replyThread) notifyLocked() {
	close(t.changed)
	t.changed = make(chan struct{})
}

func (t *replyThread) finishLocked(state string, now time.Time, m *Manager) {
	if t.finished {
		return
	}
	t.state = state
	t.finished = true
	t.ended = now
	m.finishedReplies = append(m.finishedReplies, t)
	t.notifyLocked()
	close(t.done)
}

func (t *replyThread) nextExpiryLocked() time.Time {
	if t.finished {
		return time.Time{}
	}
	if t.draft != nil && t.draft.state == replyStatePending {
		return t.draft.expiresAt
	}
	return t.deadline
}

func (t *replyThread) pageLocked(cursor int64, limit int) []event {
	start := int(min(cursor, int64(len(t.events))))
	end := min(start+limit, len(t.events))
	return t.events[start:end]
}

func (t *replyThread) outputLocked(cursor int64, page []event) Object {
	next := cursor
	events := []Object{}
	for _, e := range page {
		events = append(events, eventObject(e))
		next = int64(e.seq)
	}
	out := Object{
		"state":       t.state,
		"events":      events,
		"next_cursor": next,
		"eof":         t.finished && next >= int64(len(t.events)),
	}
	if t.finished {
		out["duration_ms"] = max(int64(0), t.ended.Sub(t.created).Milliseconds())
		out["session_id"] = t.sessionID
		if t.state == stateDone {
			out["result"] = t.result
		}
	}
	return out
}

func (m *Manager) cancelAllRepliesLocked(now time.Time) {
	for _, t := range m.replyThreads {
		if t.finished {
			continue
		}
		if t.draft != nil && t.draft.state == replyStatePending {
			t.draft.state = replyStateCancelled
			t.draft.decidedAt = now
			t.draft.body = ""
		}
		t.finishLocked(stateCancelled, now, m)
	}
}

func (m *Manager) pruneRepliesLocked(now time.Time) {
	for _, t := range m.replyThreads {
		if t.finished {
			continue
		}
		if t.draft != nil && t.draft.state == replyStatePending {
			if !now.Before(t.draft.expiresAt) {
				t.draft.state = replyStateExpired
				t.draft.decidedAt = now
				t.draft.body = ""
				t.finishLocked(stateExpired, now, m)
			}
			continue
		}
		if !now.Before(t.deadline) {
			t.finishLocked(stateTimeout, now, m)
		}
	}
	kept := m.finishedReplies[:0]
	for _, t := range m.finishedReplies {
		if now.Sub(t.ended) >= t.retain {
			m.evictReplyLocked(t)
			continue
		}
		kept = append(kept, t)
	}
	m.finishedReplies = kept
	for len(m.finishedReplies) > maxFinished {
		m.evictReplyLocked(m.finishedReplies[0])
		m.finishedReplies = m.finishedReplies[1:]
	}
}

func (m *Manager) evictReplyLocked(t *replyThread) {
	delete(m.replyThreads, t.id)
	delete(m.replyByMsg, t.messageID)
	if t.draft != nil {
		t.draft.body = ""
		delete(m.replies, t.draft.id)
	}
	t.result = ""
	t.events = nil
}

// SubmitSessionReply records an explicit reply draft from a live session into the local confirmation queue (CTR-DLG-07).
// The draft body stays invisible to the remote caller until a local user previews and approves it.
func (m *Manager) SubmitSessionReply(settings config.Delegate, messageID, sessionID, body string) (ReplySummary, error) {
	if !settings.Enabled {
		return ReplySummary{}, disabledError()
	}
	if !settings.SessionsAllowed() {
		return ReplySummary{}, sessionsDisabledError()
	}
	if !settings.SessionRepliesAllowed() {
		return ReplySummary{}, sessionRepliesDisabledError()
	}
	if messageID == "" {
		return ReplySummary{}, toolerr.Args("message_id")
	}
	if body == "" {
		return ReplySummary{}, toolerr.New("invalid_args", "回信正文不能为空", "field", "body", "reason", "empty")
	}
	if !utf8.ValidString(body) {
		return ReplySummary{}, toolerr.New("invalid_args", "回信正文必须是合法 UTF-8", "field", "body", "reason", "invalid_utf8")
	}
	if len(body) > MaxSessionReplyBytes {
		return ReplySummary{}, toolerr.New("invalid_args", "回信正文超过 8 KiB 上限", "field", "body", "reason", "body_too_large")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ReplySummary{}, toolerr.New("internal", "委托管理器正在关闭", "op", "delegate.reply")
	}
	now := m.now()
	m.pruneLocked(now)
	t := m.replyByMsg[messageID]
	if t == nil {
		return ReplySummary{}, toolerr.New("not_found", "注入消息不存在或已过期", "name", messageID)
	}
	if sessionID != "" && t.sessionID != sessionID {
		return ReplySummary{}, toolerr.New("not_found", "注入消息与会话不匹配", "name", messageID)
	}
	if t.finished {
		return ReplySummary{}, toolerr.New("conflict", "该注入消息已结束，不能再提交回信", "reason", "reply_"+t.state)
	}
	if t.draft != nil {
		return ReplySummary{}, toolerr.New("conflict", "该注入消息已经提交过回信草稿", "reason", "reply_already_submitted")
	}
	pending := 0
	for _, d := range m.replies {
		if d.state == replyStatePending {
			pending++
		}
	}
	if pending >= maxPendingReplies {
		return ReplySummary{}, toolerr.New("delegate_saturated", "待确认回信队列已满", "in_use", pending, "limit", maxPendingReplies)
	}

	d := &sessionReply{
		id:         newID("r-"),
		thread:     t,
		messageID:  messageID,
		taskID:     t.id,
		sessionID:  t.sessionID,
		caller:     t.caller,
		cwd:        t.cwd,
		body:       body,
		bodySHA256: sha256Hex(body),
		bytes:      len(body),
		state:      replyStatePending,
		created:    now,
		expiresAt:  now.Add(m.approvalTTL),
	}
	t.draft = d
	m.replies[d.id] = d
	return d.summary(), nil
}

// ListSessionReplies returns metadata summaries of session reply drafts, optionally filtered by state (CTR-DLG-07).
// It never includes the reply body.
func (m *Manager) ListSessionReplies(stateFilter string) ([]ReplySummary, error) {
	switch stateFilter {
	case "", replyStatePending, replyStateApproved, replyStateRejected, replyStateExpired, replyStateCancelled:
	default:
		return nil, toolerr.Args("state")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(m.now())
	out := make([]ReplySummary, 0, len(m.replies))
	for _, d := range m.replies {
		if stateFilter != "" && d.state != stateFilter {
			continue
		}
		out = append(out, d.summary())
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ReplyID < out[j].ReplyID
	})
	return out, nil
}

// PreviewSessionReply returns a single reply draft with its full body (while pending) and marks it previewed (CTR-DLG-07).
func (m *Manager) PreviewSessionReply(replyID string) (ReplyDetail, error) {
	if replyID == "" {
		return ReplyDetail{}, toolerr.Args("reply_id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.pruneLocked(now)
	d := m.replies[replyID]
	if d == nil {
		return ReplyDetail{}, toolerr.New("not_found", "回信草稿不存在", "name", replyID)
	}
	if d.state == replyStatePending {
		d.previewed = true
		d.previewedAt = now
	}
	return d.detail(), nil
}

// ApproveSessionReply confirms a previously previewed pending reply draft and releases its body to the caller (CTR-DLG-07).
func (m *Manager) ApproveSessionReply(settings config.Delegate, replyID, bodySHA256 string) (ReplySummary, error) {
	if !settings.Enabled {
		return ReplySummary{}, disabledError()
	}
	if !settings.SessionsAllowed() {
		return ReplySummary{}, sessionsDisabledError()
	}
	if !settings.SessionRepliesAllowed() {
		return ReplySummary{}, sessionRepliesDisabledError()
	}
	if replyID == "" {
		return ReplySummary{}, toolerr.Args("reply_id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.pruneLocked(now)
	d := m.replies[replyID]
	if d == nil {
		return ReplySummary{}, toolerr.New("not_found", "回信草稿不存在", "name", replyID)
	}
	if d.state != replyStatePending {
		return ReplySummary{}, toolerr.New("conflict", "该回信草稿已处理或已过期", "reason", "reply_"+d.state)
	}
	if !d.previewed {
		return ReplySummary{}, toolerr.New("conflict", "必须先逐条预览回信正文后再确认发送", "reason", "preview_required")
	}
	if bodySHA256 != "" && bodySHA256 != d.bodySHA256 {
		return ReplySummary{}, toolerr.New("conflict", "回信正文摘要与预览不一致", "reason", "body_mismatch")
	}

	body := d.body
	d.state = replyStateApproved
	d.decidedAt = now
	d.body = ""
	t := d.thread
	t.events = append(t.events, event{seq: uint64(len(t.events)) + 1, typ: "assistant", text: body})
	t.result = body
	t.finishLocked(stateDone, now, m)
	return d.summary(), nil
}

// RejectSessionReply rejects a pending reply draft and immediately wipes its body from memory (CTR-DLG-07).
func (m *Manager) RejectSessionReply(replyID string) (ReplySummary, error) {
	if replyID == "" {
		return ReplySummary{}, toolerr.Args("reply_id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.pruneLocked(now)
	d := m.replies[replyID]
	if d == nil {
		return ReplySummary{}, toolerr.New("not_found", "回信草稿不存在", "name", replyID)
	}
	if d.state != replyStatePending {
		return ReplySummary{}, toolerr.New("conflict", "该回信草稿已处理或已过期", "reason", "reply_"+d.state)
	}

	d.state = replyStateRejected
	d.decidedAt = now
	d.body = ""
	d.thread.finishLocked(stateRejected, now, m)
	return d.summary(), nil
}

func (m *Manager) replyOutput(ctx context.Context, caller, taskID string, cursor int64, limit int, deadline time.Time) (Object, error) {
	m.mu.Lock()
	t := m.ownedReplyLocked(caller, taskID)
	if t == nil {
		m.mu.Unlock()
		return nil, notFound(taskID)
	}
	for {
		now := m.now()
		m.pruneLocked(now)
		page := t.pageLocked(cursor, limit)
		left := deadline.Sub(now)
		if nextExp := t.nextExpiryLocked(); !nextExp.IsZero() {
			if untilExp := nextExp.Sub(now); untilExp < left {
				left = untilExp
			}
		}
		if len(page) > 0 || t.finished || left <= 0 || ctx.Err() != nil {
			out := t.outputLocked(cursor, page)
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

func (m *Manager) cancelReply(caller, taskID string) (Object, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	t := m.ownedReplyLocked(caller, taskID)
	if t == nil {
		return nil, false
	}
	if !t.finished {
		if t.draft != nil && t.draft.state == replyStatePending {
			t.draft.state = replyStateCancelled
			t.draft.decidedAt = now
			t.draft.body = ""
		}
		t.finishLocked(stateCancelled, now, m)
	}
	return Object{"task_id": t.id, "state": t.state}, true
}
