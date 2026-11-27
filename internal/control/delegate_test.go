package control

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// fakeDelegate is an agent with one running task of another caller, one runner, and an in-memory reply queue.
type fakeDelegate struct {
	replies         map[string]*v2.DelegateReplyDetail
	allowReplies    bool
	delegateEnabled bool
}

func newFakeDelegate() *fakeDelegate {
	return &fakeDelegate{
		delegateEnabled: true,
		allowReplies:    true,
		replies:         map[string]*v2.DelegateReplyDetail{},
	}
}

func (f *fakeDelegate) DelegateState() v2.DelegateState {
	pending := 0
	for _, r := range f.replies {
		if r.State == v2.DelegateReplyPending {
			pending++
		}
	}
	return v2.DelegateState{
		Enabled: f.delegateEnabled, AllowWrite: true, AllowSessions: false, AllowSessionReplies: f.allowReplies,
		MaxConcurrent: 2, Running: 1, PendingReplies: pending,
		Runners:  []v2.DelegateRunner{{Name: "claude", Available: true, Path: "/usr/bin/claude"}, {Name: "codex", Available: false}},
		Sessions: []v2.DelegateSession{},
		Tasks: []v2.DelegateTask{{
			TaskID: "t-5e0a7c41b9d24f6e8a13c0d7f2e4b916", Caller: "node-b/bob", Runner: "claude", State: "running", CWD: "/w/a",
			StartedAt: v2.Now(), DurationMS: 5,
		}},
	}
}

func (f *fakeDelegate) ListDelegateReplies(state string) (v2.DelegateRepliesResponse, error) {
	switch state {
	case "", "pending", "approved", "rejected", "expired", "cancelled":
	default:
		return v2.DelegateRepliesResponse{}, toolerr.Args("state")
	}
	out := v2.DelegateRepliesResponse{Replies: []v2.DelegateReplySummary{}}
	for _, r := range f.replies {
		if state != "" && string(r.State) != state {
			continue
		}
		out.Replies = append(out.Replies, v2.DelegateReplySummary{
			ReplyID: r.ReplyID, TaskID: r.TaskID, MessageID: r.MessageID, SessionID: r.SessionID,
			Caller: r.Caller, State: r.State, Bytes: r.Bytes, Previewed: r.Previewed,
			CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, DecidedAt: r.DecidedAt,
		})
	}
	return out, nil
}

func (f *fakeDelegate) SubmitDelegateReply(req v2.DelegateReplyCreateRequest) (v2.DelegateReplySummary, error) {
	if !f.allowReplies {
		return v2.DelegateReplySummary{}, toolerr.New("policy_denied", "活会话回信已关闭", "rule", "delegate.session_replies_disabled")
	}
	if len(req.Body) > 8192 {
		return v2.DelegateReplySummary{}, toolerr.New("invalid_args", "回信正文超过 8 KiB 上限", "field", "body", "reason", "body_too_large")
	}
	sum := sha256.Sum256([]byte(req.Body))
	now := v2.Now()
	d := &v2.DelegateReplyDetail{
		ReplyID:    "r-8f1c9a2e6b7d4e8f9a0b1c2d3e4f5a6b",
		TaskID:     "sr-5e0a7c41b9d24f6e8a13c0d7f2e4b916",
		MessageID:  req.MessageID,
		SessionID:  "sess-1",
		Caller:     "node-b/bob",
		State:      v2.DelegateReplyPending,
		Bytes:      len(req.Body),
		Body:       req.Body,
		BodySHA256: hex.EncodeToString(sum[:]),
		CreatedAt:  now,
		ExpiresAt:  now,
	}
	f.replies[d.ReplyID] = d
	return v2.DelegateReplySummary{
		ReplyID: d.ReplyID, TaskID: d.TaskID, MessageID: d.MessageID, SessionID: d.SessionID,
		Caller: d.Caller, State: d.State, Bytes: d.Bytes, Previewed: d.Previewed,
		CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt,
	}, nil
}

func (f *fakeDelegate) PreviewDelegateReply(replyID string) (v2.DelegateReplyDetail, error) {
	d := f.replies[replyID]
	if d == nil {
		return v2.DelegateReplyDetail{}, toolerr.New("not_found", "回信草稿不存在", "name", replyID)
	}
	if d.State == v2.DelegateReplyPending {
		d.Previewed = true
	}
	return *d, nil
}

func (f *fakeDelegate) ApproveDelegateReply(replyID, bodySHA256 string) (v2.DelegateReplySummary, error) {
	d := f.replies[replyID]
	if d == nil {
		return v2.DelegateReplySummary{}, toolerr.New("not_found", "回信草稿不存在", "name", replyID)
	}
	if !d.Previewed {
		return v2.DelegateReplySummary{}, toolerr.New("conflict", "必须先预览", "reason", "preview_required")
	}
	if bodySHA256 != "" && bodySHA256 != d.BodySHA256 {
		return v2.DelegateReplySummary{}, toolerr.New("conflict", "摘要不匹配", "reason", "body_mismatch")
	}
	now := v2.Now()
	d.State = v2.DelegateReplyApproved
	d.Body = ""
	d.DecidedAt = &now
	return v2.DelegateReplySummary{
		ReplyID: d.ReplyID, TaskID: d.TaskID, MessageID: d.MessageID, SessionID: d.SessionID,
		Caller: d.Caller, State: d.State, Bytes: d.Bytes, Previewed: d.Previewed,
		CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt, DecidedAt: d.DecidedAt,
	}, nil
}

func (f *fakeDelegate) RejectDelegateReply(replyID string) (v2.DelegateReplySummary, error) {
	d := f.replies[replyID]
	if d == nil {
		return v2.DelegateReplySummary{}, toolerr.New("not_found", "回信草稿不存在", "name", replyID)
	}
	now := v2.Now()
	d.State = v2.DelegateReplyRejected
	d.Body = ""
	d.DecidedAt = &now
	return v2.DelegateReplySummary{
		ReplyID: d.ReplyID, TaskID: d.TaskID, MessageID: d.MessageID, SessionID: d.SessionID,
		Caller: d.Caller, State: d.State, Bytes: d.Bytes, Previewed: d.Previewed,
		CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt, DecidedAt: d.DecidedAt,
	}, nil
}

// conformance: CTR-CTL-12
//
// A node without an agent does not declare the delegate capability, and GET /v2/delegate answers not_implemented.
// Each fixture is a test of its own: on Windows the control pipe name is per node and user, so two servers in one test collide.
func TestDelegateRouteAbsentWithoutAgent(t *testing.T) {
	f := newFixture(t)
	if f.server.hasCapability(v2.CapDelegate) {
		t.Fatal("delegate capability declared without an agent")
	}
	apiError(t, f.do("GET", "/v2/delegate", nil, nil), v2.ErrNotImplemented)
}

// conformance: CTR-CTL-12
//
// A node with an agent declares the delegate capability, and GET /v2/delegate answers with a DelegateState.
func TestDelegateRouteServedByAgent(t *testing.T) {
	fd := newFakeDelegate()
	f := newFixture(t, func(o *Options) { o.Delegate = fd })
	if !f.server.hasCapability(v2.CapDelegate) {
		t.Fatal("delegate capability missing with an agent")
	}
	f.ok("GET", "/v2/delegate", nil, "DelegateState")
}

// conformance: CTR-DLG-07 CTR-CTL-12
func TestDelegateReplyRoutes(t *testing.T) {
	fd := newFakeDelegate()
	f := newFixture(t, func(o *Options) { o.Delegate = fd })

	msgID := strings.Repeat("ab", 16)
	created := f.do("POST", "/v2/delegate/replies", v2.DelegateReplyCreateRequest{MessageID: msgID, Body: "approved body"}, nil)
	if created.Status != 201 {
		t.Fatalf("POST /v2/delegate/replies: %d %s", created.Status, created.Body)
	}
	valid(t, "DelegateReplySummary", created.Body)
	var sum v2.DelegateReplySummary
	decodeAs(t, created, &sum)

	// List must not include body.
	listed := f.ok("GET", "/v2/delegate/replies?state=pending", nil, "DelegateRepliesResponse")
	if strings.Contains(string(listed.Body), "approved body") {
		t.Fatalf("reply list leaked body: %s", listed.Body)
	}

	// Approve before preview fails with conflict/preview_required.
	apiError(t, f.do("POST", "/v2/delegate/replies/"+sum.ReplyID+"/approve", map[string]any{}, nil), v2.ErrConflict)

	// Preview returns body and sha256.
	previewed := f.ok("GET", "/v2/delegate/replies/"+sum.ReplyID, nil, "DelegateReplyDetail")
	var detail v2.DelegateReplyDetail
	decodeAs(t, previewed, &detail)
	if detail.Body != "approved body" || !detail.Previewed {
		t.Fatalf("detail = %+v", detail)
	}

	// Approve with sha256 succeeds.
	f.ok("POST", "/v2/delegate/replies/"+sum.ReplyID+"/approve", v2.DelegateReplyApproveRequest{BodySHA256: detail.BodySHA256}, "DelegateReplySummary")

	// Reject works too.
	created2 := f.do("POST", "/v2/delegate/replies", v2.DelegateReplyCreateRequest{MessageID: msgID, Body: "reject me"}, nil)
	if created2.Status != 201 {
		t.Fatalf("POST /v2/delegate/replies: %d %s", created2.Status, created2.Body)
	}
	valid(t, "DelegateReplySummary", created2.Body)
	var sum2 v2.DelegateReplySummary
	decodeAs(t, created2, &sum2)
	f.ok("POST", "/v2/delegate/replies/"+sum2.ReplyID+"/reject", nil, "DelegateReplySummary")

	// Disabled replies return forbidden.
	fd.allowReplies = false
	apiError(t, f.do("POST", "/v2/delegate/replies", v2.DelegateReplyCreateRequest{MessageID: msgID, Body: "denied"}, nil), v2.ErrForbidden)

	// Invalid 8 KiB+ body returns invalid_args.
	fd.allowReplies = true
	apiError(t, f.do("POST", "/v2/delegate/replies", v2.DelegateReplyCreateRequest{MessageID: msgID, Body: strings.Repeat("x", 8193)}, nil), v2.ErrInvalidArgs)
}
