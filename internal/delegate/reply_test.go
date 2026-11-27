package delegate

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
)

func replyTestSettings(allowReplies bool) config.Delegate {
	on := true
	return config.Delegate{
		Enabled:             true,
		AllowSessions:       &on,
		AllowSessionReplies: &allowReplies,
	}
}

// conformance: CTR-DLG-07
func Test_CTRDLG07_DisabledByDefaultRefusesSubmitAndOmitsTaskID(t *testing.T) {
	h := newHarness(t, testSettings(nil))
	sessions := claudeHome(t)
	socket, got := receiver(t)
	liveSession(t, sessions, "sess-R0", h.dir, socket)

	if h.settings.SessionRepliesAllowed() {
		t.Fatal("allow_session_replies must default to false")
	}
	out := h.ok(callerA, "delegate.send", map[string]any{"session_id": "sess-R0", "message": "ping"})
	if _, hasTaskID := out["task_id"]; hasTaskID {
		t.Fatalf("delegate.send returned task_id while allow_session_replies is false: %v", out)
	}
	wire := lines(t, got)
	if strings.Contains(wire[len(wire)-1], "xmesh delegate reply") {
		t.Fatalf("reply hint injected while allow_session_replies is false: %q", wire)
	}

	msgID := out["message_id"].(string)
	_, err := h.m.SubmitSessionReply(h.settings, msgID, "sess-R0", "hello")
	h.wantCode(err, "policy_denied")
	if rule := fieldOf(err, "rule"); rule != "delegate.session_replies_disabled" {
		t.Fatalf("rule = %v, want delegate.session_replies_disabled", rule)
	}

	_, err = h.call(callerA, "delegate.output", map[string]any{"task_id": msgID})
	h.wantCode(err, "not_found")
}

// conformance: CTR-DLG-07
func Test_CTRDLG07_Enforces8KiBAndUTF8Limit(t *testing.T) {
	h := newHarness(t, replyTestSettings(true))
	sessions := claudeHome(t)
	socket, got := receiver(t)
	liveSession(t, sessions, "sess-R1", h.dir, socket)

	send := h.ok(callerA, "delegate.send", map[string]any{"session_id": "sess-R1", "message": "check limits"})
	lines(t, got)
	msgID := send["message_id"].(string)

	badCases := []struct {
		name   string
		body   string
		reason string
	}{
		{"empty", "", "empty"},
		{"invalid utf8", string([]byte{0xff, 0xfe, 'a'}), "invalid_utf8"},
		{"8193 ascii bytes", strings.Repeat("x", MaxSessionReplyBytes+1), "body_too_large"},
		{"8193 utf8 multibyte bytes", strings.Repeat("中", 2731), "body_too_large"},
	}
	for _, tc := range badCases {
		_, err := h.m.SubmitSessionReply(h.settings, msgID, "sess-R1", tc.body)
		h.wantCode(err, "invalid_args")
		if fieldOf(err, "field") != "body" || fieldOf(err, "reason") != tc.reason {
			t.Fatalf("%s: field=%v reason=%v, want field=body reason=%s", tc.name, fieldOf(err, "field"), fieldOf(err, "reason"), tc.reason)
		}
	}

	// Exact 8 KiB (8192 bytes) with multi-byte runes succeeds.
	exact := strings.Repeat("中", 2730) + "ab"
	if len(exact) != MaxSessionReplyBytes {
		t.Fatalf("len(exact) = %d, want %d", len(exact), MaxSessionReplyBytes)
	}
	summary, err := h.m.SubmitSessionReply(h.settings, msgID, "sess-R1", exact)
	if err != nil {
		t.Fatalf("exact 8 KiB reply rejected: %v", err)
	}
	if summary.Bytes != MaxSessionReplyBytes || summary.State != replyStatePending {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

// conformance: CTR-DLG-07
func Test_CTRDLG07_RequiresPreviewAndHidesDraftUntilApproved(t *testing.T) {
	h := newHarness(t, replyTestSettings(true))
	sessions := claudeHome(t)
	socket, got := receiver(t)
	liveSession(t, sessions, "sess-R2", h.dir, socket)

	send := h.ok(callerA, "delegate.send", map[string]any{"session_id": "sess-R2", "message": "status?"})
	taskID, _ := send["task_id"].(string)
	msgID, _ := send["message_id"].(string)
	if taskID == "" || msgID == "" {
		t.Fatalf("send = %v, want task_id and message_id", send)
	}
	wire := lines(t, got)
	if content := decodeUserLine(t, wire[len(wire)-1]); !strings.Contains(content, "xmesh delegate reply --message-id "+msgID) {
		t.Fatalf("missing reply adapter hint: %q", content)
	}

	const secretDraft = "DRAFT-REPLY-SECRET-9910"
	summary, err := h.m.SubmitSessionReply(h.settings, msgID, "sess-R2", secretDraft)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	// Unapproved draft must never be visible via delegate.output or Overview or list summaries.
	out := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID, "wait_ms": 0})
	if out["state"] != stateRunning || out["eof"] != false || len(out["events"].([]Object)) != 0 {
		t.Fatalf("unapproved output = %v", out)
	}
	rawOut, _ := json.Marshal(out)
	if strings.Contains(string(rawOut), secretDraft) {
		t.Fatalf("unapproved output leaked draft body: %s", rawOut)
	}
	ov := h.m.Overview(h.settings, nil)
	if ov.PendingReplies != 1 {
		t.Fatalf("PendingReplies = %d, want 1", ov.PendingReplies)
	}
	rawOv, _ := json.Marshal(ov)
	if strings.Contains(string(rawOv), secretDraft) {
		t.Fatalf("overview leaked draft body: %s", rawOv)
	}
	list, err := h.m.ListSessionReplies("pending")
	if err != nil || len(list) != 1 || list[0].Previewed {
		t.Fatalf("list = %+v, err = %v", list, err)
	}

	// Cross-caller access to task_id must be denied with not_found.
	_, err = h.call(callerB, "delegate.output", map[string]any{"task_id": taskID})
	h.wantCode(err, "not_found")
	_, err = h.call(callerB, "delegate.cancel", map[string]any{"task_id": taskID})
	h.wantCode(err, "not_found")

	// Direct approval without previewing must fail with preview_required.
	_, err = h.m.ApproveSessionReply(h.settings, summary.ReplyID, "")
	h.wantCode(err, "conflict")
	if fieldOf(err, "reason") != "preview_required" {
		t.Fatalf("reason = %v, want preview_required", fieldOf(err, "reason"))
	}

	// Preview exposes the full body and sha256.
	detail, err := h.m.PreviewSessionReply(summary.ReplyID)
	if err != nil || detail.Body != secretDraft || !detail.Previewed || detail.BodySHA256 == "" {
		t.Fatalf("preview = %+v, err = %v", detail, err)
	}

	// Wrong sha256 must fail with body_mismatch.
	_, err = h.m.ApproveSessionReply(h.settings, summary.ReplyID, strings.Repeat("0", 64))
	h.wantCode(err, "conflict")
	if fieldOf(err, "reason") != "body_mismatch" {
		t.Fatalf("reason = %v, want body_mismatch", fieldOf(err, "reason"))
	}

	// Long-polling delegate.output wakes up immediately when approved.
	resCh := make(chan Object, 1)
	errCh := make(chan error, 1)
	go func() {
		res, e := h.call(callerA, "delegate.output", map[string]any{"task_id": taskID, "wait_ms": 5000})
		resCh <- res
		errCh <- e
	}()
	time.Sleep(30 * time.Millisecond)

	// Turning allow_session_replies off refuses approval.
	offSettings := replyTestSettings(false)
	_, err = h.m.ApproveSessionReply(offSettings, summary.ReplyID, detail.BodySHA256)
	h.wantCode(err, "policy_denied")
	if fieldOf(err, "rule") != "delegate.session_replies_disabled" {
		t.Fatalf("rule = %v, want delegate.session_replies_disabled", fieldOf(err, "rule"))
	}

	// Approving with matching sha256 releases the reply.
	approved, err := h.m.ApproveSessionReply(h.settings, summary.ReplyID, detail.BodySHA256)
	if err != nil || approved.State != replyStateApproved {
		t.Fatalf("approve = %+v, err = %v", approved, err)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("long poll failed: %v", e)
	}
	finalOut := <-resCh
	events := finalOut["events"].([]Object)
	if finalOut["state"] != stateDone || finalOut["eof"] != true || finalOut["result"] != secretDraft || len(events) != 1 || events[0]["text"] != secretDraft || events[0]["type"] != "assistant" {
		t.Fatalf("approved output = %v", finalOut)
	}
}

// conformance: CTR-DLG-07
func Test_CTRDLG07_RejectWipesBodyAndEndsTask(t *testing.T) {
	h := newHarness(t, replyTestSettings(true))
	sessions := claudeHome(t)
	socket, got := receiver(t)
	liveSession(t, sessions, "sess-R3", h.dir, socket)

	send := h.ok(callerA, "delegate.send", map[string]any{"session_id": "sess-R3", "message": "hi"})
	lines(t, got)
	taskID := send["task_id"].(string)
	msgID := send["message_id"].(string)

	const rejectedBody = "REJECTED-SENSITIVE-BODY-123"
	summary, err := h.m.SubmitSessionReply(h.settings, msgID, "sess-R3", rejectedBody)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := h.m.RejectSessionReply(summary.ReplyID)
	if err != nil || rejected.State != replyStateRejected {
		t.Fatalf("reject = %+v, err = %v", rejected, err)
	}

	detail, err := h.m.PreviewSessionReply(summary.ReplyID)
	if err != nil || detail.Body != "" {
		t.Fatalf("rejected body still present: %+v", detail)
	}
	out := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID})
	if out["state"] != stateRejected || out["eof"] != true || len(out["events"].([]Object)) != 0 {
		t.Fatalf("rejected output = %v", out)
	}
	if _, hasResult := out["result"]; hasResult {
		t.Fatalf("rejected output must not carry result: %v", out)
	}

	// Resubmitting for the same message_id fails.
	_, err = h.m.SubmitSessionReply(h.settings, msgID, "sess-R3", "retry")
	h.wantCode(err, "conflict")
}

// conformance: CTR-DLG-07
func Test_CTRDLG07_ExpirationAndTimeoutTransitions(t *testing.T) {
	t.Run("approval_expired_wipes_body", func(t *testing.T) {
		h := newHarness(t, replyTestSettings(true))
		now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		h.m.SetClock(func() time.Time { return now })
		h.m.SetReplyTTLs(30*time.Minute, 5*time.Minute)

		sessions := claudeHome(t)
		socket, got := receiver(t)
		liveSession(t, sessions, "sess-E1", h.dir, socket)

		send := h.ok(callerA, "delegate.send", map[string]any{"session_id": "sess-E1", "message": "hi"})
		lines(t, got)
		taskID := send["task_id"].(string)
		msgID := send["message_id"].(string)

		sub, err := h.m.SubmitSessionReply(h.settings, msgID, "sess-E1", "EXPIRED-SECRET")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.m.PreviewSessionReply(sub.ReplyID); err != nil {
			t.Fatal(err)
		}

		// Advance past 5m approval TTL.
		now = now.Add(6 * time.Minute)
		_, err = h.m.ApproveSessionReply(h.settings, sub.ReplyID, "")
		h.wantCode(err, "conflict")
		if fieldOf(err, "reason") != "reply_expired" {
			t.Fatalf("reason = %v, want reply_expired", fieldOf(err, "reason"))
		}
		detail, err := h.m.PreviewSessionReply(sub.ReplyID)
		if err != nil || detail.State != replyStateExpired || detail.Body != "" {
			t.Fatalf("expired detail = %+v, err = %v", detail, err)
		}
		out := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID})
		if out["state"] != stateExpired || out["eof"] != true || len(out["events"].([]Object)) != 0 {
			t.Fatalf("expired output = %v", out)
		}
	})

	t.Run("reply_window_timeout", func(t *testing.T) {
		h := newHarness(t, replyTestSettings(true))
		now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		h.m.SetClock(func() time.Time { return now })
		h.m.SetReplyTTLs(2*time.Minute, 1*time.Minute)

		sessions := claudeHome(t)
		socket, got := receiver(t)
		liveSession(t, sessions, "sess-E2", h.dir, socket)

		send := h.ok(callerA, "delegate.send", map[string]any{"session_id": "sess-E2", "message": "hi"})
		lines(t, got)
		taskID := send["task_id"].(string)
		msgID := send["message_id"].(string)

		now = now.Add(3 * time.Minute)
		out := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID})
		if out["state"] != stateTimeout || out["eof"] != true {
			t.Fatalf("timeout output = %v", out)
		}
		_, err := h.m.SubmitSessionReply(h.settings, msgID, "sess-E2", "too late")
		h.wantCode(err, "conflict")
		if fieldOf(err, "reason") != "reply_timeout" {
			t.Fatalf("reason = %v, want reply_timeout", fieldOf(err, "reason"))
		}
	})

	t.Run("long_poll_wakes_on_approval_expiry", func(t *testing.T) {
		h := newHarness(t, replyTestSettings(true))
		h.m.SetReplyTTLs(5*time.Second, 50*time.Millisecond)

		sessions := claudeHome(t)
		socket, got := receiver(t)
		liveSession(t, sessions, "sess-E3", h.dir, socket)

		send := h.ok(callerA, "delegate.send", map[string]any{"session_id": "sess-E3", "message": "hi"})
		lines(t, got)
		taskID := send["task_id"].(string)
		msgID := send["message_id"].(string)

		if _, err := h.m.SubmitSessionReply(h.settings, msgID, "sess-E3", "ephemeral"); err != nil {
			t.Fatal(err)
		}
		out := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID, "wait_ms": 2000})
		if out["state"] != stateExpired || out["eof"] != true {
			t.Fatalf("long poll did not wake on expiry: %v", out)
		}
	})
}

// conformance: CTR-DLG-07
func Test_CTRDLG07_CancelAndSwitchOffWipePendingDrafts(t *testing.T) {
	h := newHarness(t, replyTestSettings(true))
	sessions := claudeHome(t)
	socket, got := receiver(t)
	liveSession(t, sessions, "sess-C1", h.dir, socket)

	send1 := h.ok(callerA, "delegate.send", map[string]any{"session_id": "sess-C1", "message": "one"})
	lines(t, got)
	sub1, err := h.m.SubmitSessionReply(h.settings, send1["message_id"].(string), "sess-C1", "SECRET-1")
	if err != nil {
		t.Fatal(err)
	}
	cancelled := h.ok(callerA, "delegate.cancel", map[string]any{"task_id": send1["task_id"]})
	if cancelled["state"] != stateCancelled {
		t.Fatalf("cancel = %v", cancelled)
	}
	// Idempotent second cancel.
	if again := h.ok(callerA, "delegate.cancel", map[string]any{"task_id": send1["task_id"]}); again["state"] != stateCancelled {
		t.Fatalf("second cancel = %v", again)
	}
	if detail, err := h.m.PreviewSessionReply(sub1.ReplyID); err != nil || detail.Body != "" || detail.State != replyStateCancelled {
		t.Fatalf("cancelled detail = %+v, err = %v", detail, err)
	}

	// CancelSessionReplies cancels remaining active threads and wipes drafts.
	socket2, got2 := receiver(t)
	liveSession(t, sessions, "sess-C2", h.dir, socket2)
	send2 := h.ok(callerA, "delegate.send", map[string]any{"session_id": "sess-C2", "message": "two"})
	lines(t, got2)
	sub2, err := h.m.SubmitSessionReply(h.settings, send2["message_id"].(string), "sess-C2", "SECRET-2")
	if err != nil {
		t.Fatal(err)
	}
	h.m.CancelSessionReplies()
	if detail, err := h.m.PreviewSessionReply(sub2.ReplyID); err != nil || detail.Body != "" || detail.State != replyStateCancelled {
		t.Fatalf("hot-disabled detail = %+v, err = %v", detail, err)
	}
	out2 := h.ok(callerA, "delegate.output", map[string]any{"task_id": send2["task_id"]})
	if out2["state"] != stateCancelled || out2["eof"] != true {
		t.Fatalf("hot-disabled output = %v", out2)
	}
}

// conformance: CTR-DLG-07
func Test_CTRDLG07_ConcurrentReplyLifecycleIsRaceFree(t *testing.T) {
	h := newHarness(t, replyTestSettings(true))
	sessions := claudeHome(t)

	const n = 12
	type item struct {
		taskID string
		msgID  string
		sessID string
	}
	items := make([]item, 0, n)
	for i := range n {
		sock, got := receiver(t)
		sessID := fmt.Sprintf("sess-conc-%d", i)
		liveSession(t, sessions, sessID, h.dir, sock)
		send := h.ok(callerA, "delegate.send", map[string]any{"session_id": sessID, "message": "go"})
		lines(t, got)
		items = append(items, item{
			taskID: send["task_id"].(string),
			msgID:  send["message_id"].(string),
			sessID: sessID,
		})
	}

	var wg sync.WaitGroup
	for i, it := range items {
		wg.Add(1)
		go func(idx int, it item) {
			defer wg.Done()
			sub, err := h.m.SubmitSessionReply(h.settings, it.msgID, it.sessID, fmt.Sprintf("reply-%d", idx))
			if err != nil {
				t.Errorf("submit %d: %v", idx, err)
				return
			}
			_, _ = h.m.ListSessionReplies("")
			_ = h.m.Overview(h.settings, nil)
			det, err := h.m.PreviewSessionReply(sub.ReplyID)
			if err != nil {
				t.Errorf("preview %d: %v", idx, err)
				return
			}
			switch idx % 3 {
			case 0:
				_, _ = h.m.ApproveSessionReply(h.settings, sub.ReplyID, det.BodySHA256)
			case 1:
				_, _ = h.m.RejectSessionReply(sub.ReplyID)
			case 2:
				_, _ = h.call(callerA, "delegate.cancel", map[string]any{"task_id": it.taskID})
			}
			out := h.ok(callerA, "delegate.output", map[string]any{"task_id": it.taskID, "wait_ms": 500})
			if out["eof"] != true {
				t.Errorf("output %d not eof: %v", idx, out)
			}
		}(i, it)
	}
	wg.Wait()
}
