package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/delegate"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type cliFakeDelegate struct {
	allowReplies bool
	replies      map[string]*v2.DelegateReplyDetail
}

func (f *cliFakeDelegate) DelegateState() v2.DelegateState {
	pending := 0
	for _, r := range f.replies {
		if r.State == v2.DelegateReplyPending {
			pending++
		}
	}
	return v2.DelegateState{
		Enabled:             true,
		AllowSessions:       true,
		AllowSessionReplies: f.allowReplies,
		MaxConcurrent:       2,
		PendingReplies:      pending,
		Runners:             []v2.DelegateRunner{{Name: "claude", Available: true, Path: "/usr/bin/claude"}},
		Sessions:            []v2.DelegateSession{},
		Tasks:               []v2.DelegateTask{},
	}
}

func (f *cliFakeDelegate) ListDelegateReplies(state string) (v2.DelegateRepliesResponse, error) {
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

func (f *cliFakeDelegate) SubmitDelegateReply(req v2.DelegateReplyCreateRequest) (v2.DelegateReplySummary, error) {
	if !f.allowReplies {
		return v2.DelegateReplySummary{}, toolerr.New("policy_denied", "活会话回信已关闭", "rule", "delegate.session_replies_disabled")
	}
	sum := sha256.Sum256([]byte(req.Body))
	now := v2.Now()
	d := &v2.DelegateReplyDetail{
		ReplyID:    "r-11111111111111111111111111111111",
		TaskID:     "sr-11111111111111111111111111111111",
		MessageID:  req.MessageID,
		SessionID:  "sess-cli",
		Caller:     "node-a/cli",
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

func (f *cliFakeDelegate) PreviewDelegateReply(replyID string) (v2.DelegateReplyDetail, error) {
	d := f.replies[replyID]
	if d == nil {
		return v2.DelegateReplyDetail{}, toolerr.New("not_found", "不存在", "name", replyID)
	}
	d.Previewed = true
	return *d, nil
}

func (f *cliFakeDelegate) ApproveDelegateReply(replyID, bodySHA256 string) (v2.DelegateReplySummary, error) {
	d := f.replies[replyID]
	if d == nil {
		return v2.DelegateReplySummary{}, toolerr.New("not_found", "不存在", "name", replyID)
	}
	if !d.Previewed {
		return v2.DelegateReplySummary{}, toolerr.New("conflict", "未预览", "reason", "preview_required")
	}
	if bodySHA256 != "" && bodySHA256 != d.BodySHA256 {
		return v2.DelegateReplySummary{}, toolerr.New("conflict", "摘要不一致", "reason", "body_mismatch")
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

func (f *cliFakeDelegate) RejectDelegateReply(replyID string) (v2.DelegateReplySummary, error) {
	d := f.replies[replyID]
	if d == nil {
		return v2.DelegateReplySummary{}, toolerr.New("not_found", "不存在", "name", replyID)
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

// conformance: CTR-DLG-07
func Test_CLIDelegateReplyAndControlConfirm(t *testing.T) {
	fd := &cliFakeDelegate{allowReplies: true, replies: map[string]*v2.DelegateReplyDetail{}}
	state, _ := startKernelStubOptions(t, cliConfig, func(o *control.Options) {
		o.Delegate = fd
	})

	msgID := strings.Repeat("ab", 16)
	runControl := func(args ...string) map[string]any {
		t.Helper()
		out, err := output(t, func() error { return controlCommand(append(args, "--state-dir", state)) })
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("%v printed %q", args, out)
		}
		return v
	}

	// Reject > 8 KiB locally before talking to kernel.
	if err := delegateCommand([]string{"reply", "--state-dir", state, "--message-id", msgID, "--body", strings.Repeat("x", delegate.MaxSessionReplyBytes+1)}); err == nil {
		t.Fatal("oversized reply accepted")
	}

	// Submit via --stdin.
	oldStdin := delegateStdin
	delegateStdin = strings.NewReader("reply from stdin")
	t.Cleanup(func() { delegateStdin = oldStdin })
	subOut, err := output(t, func() error {
		return delegateCommand([]string{"reply", "--state-dir", state, "--message-id", msgID, "--stdin"})
	})
	if err != nil {
		t.Fatalf("delegate reply --stdin: %v", err)
	}
	var sum map[string]any
	if err := json.Unmarshal([]byte(subOut), &sum); err != nil {
		t.Fatal(err)
	}
	replyID := sum["reply_id"].(string)

	// Control status and list must not contain body.
	st := runControl("delegate", "status")
	if st["allow_session_replies"] != true || st["pending_replies"] != float64(1) {
		t.Fatalf("status = %v", st)
	}
	listed, _ := output(t, func() error {
		return controlCommand([]string{"delegate", "replies", "list", "--state", "pending", "--state-dir", state})
	})
	if strings.Contains(listed, "reply from stdin") {
		t.Fatalf("list leaked body: %s", listed)
	}

	// Preview returns body and sha256, then approve succeeds.
	prev := runControl("delegate", "replies", "preview", replyID)
	if prev["body"] != "reply from stdin" {
		t.Fatalf("preview = %v", prev)
	}
	app := runControl("delegate", "replies", "approve", replyID, "--sha256", prev["body_sha256"].(string))
	if app["state"] != "approved" {
		t.Fatalf("approve = %v", app)
	}

	// Submit via --body-file and reject.
	bodyFile := filepath.Join(t.TempDir(), "reply.txt")
	if err := os.WriteFile(bodyFile, []byte("reject body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := output(t, func() error {
		return delegateCommand([]string{"reply", "--state-dir", state, "--message-id", msgID, "--body-file", bodyFile})
	}); err != nil {
		t.Fatalf("delegate reply --body-file: %v", err)
	}
	rej := runControl("delegate", "replies", "reject", replyID)
	if rej["state"] != "rejected" {
		t.Fatalf("reject = %v", rej)
	}

	// Disabled replies returns error from kernel.
	fd.allowReplies = false
	if err := delegateCommand([]string{"reply", "--state-dir", state, "--message-id", msgID, "--body", "denied"}); err == nil {
		t.Fatal("disabled reply accepted")
	}
}
