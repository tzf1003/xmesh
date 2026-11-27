package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/control"
)

func liveTestSession(t *testing.T, claudeDir, id, cwd string) <-chan []byte {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("live session socket test uses unix domain sockets")
	}
	sessionsDir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp("", "xm-agent-sess")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "msg.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	got := make(chan []byte, 4)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(conn)
			_ = conn.Close()
			got <- buf.Bytes()
		}
	}()
	meta, _ := json.Marshal(map[string]any{
		"pid":                 os.Getpid(),
		"sessionId":           id,
		"cwd":                 cwd,
		"name":                "live-test",
		"status":              "idle",
		"messagingSocketPath": sockPath,
	})
	if err := os.WriteFile(filepath.Join(sessionsDir, id+".json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	return got
}

// conformance: CTR-DLG-07 CTR-AUD-01
func Test_CTRDLG07_AgentSessionReplyEndToEndAndAuditRedaction(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	path, store := delegateConfig(t, map[string]any{
		"enabled":               true,
		"allow_sessions":        true,
		"allow_session_replies": true,
	})
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	auditDir := t.TempDir()
	logger, err := audit.New(auditDir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	a.SetAudit(logger)

	stateDir := t.TempDir()
	ctl, err := control.Start(control.Options{
		Store:    store,
		Version:  "t",
		StateDir: stateDir,
		AuditDir: auditDir,
		Audit:    logger,
		Delegate: a,
		Host:     stubControlHost{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)

	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")

	delivered := liveTestSession(t, claudeDir, "sess-e2e", store.Get().Agent.DefaultCWD)
	const secretPrompt = "INJECT-SECRET-4411"
	const secretReply = "REPLY-SECRET-8822"

	sendRes := object(t, tool(t, client, "delegate.send", map[string]any{
		"session_id": "sess-e2e",
		"message":    secretPrompt,
	}))
	msgID, _ := sendRes["message_id"].(string)
	taskID, _ := sendRes["task_id"].(string)
	if msgID == "" || taskID == "" {
		t.Fatalf("sendRes = %v, want message_id and task_id", sendRes)
	}
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("message did not reach live session socket")
	}

	// Submit reply draft over control channel.
	ctlClient := control.NewClient(ctl.Endpoint())
	subResp, err := ctlClient.Do(t.Context(), "POST", "/v2/delegate/replies", v2.DelegateReplyCreateRequest{
		MessageID: msgID,
		SessionID: "sess-e2e",
		Body:      secretReply,
	}, nil)
	if err != nil || subResp.Error() != nil {
		t.Fatalf("submit reply: err=%v resp=%v", err, subResp)
	}
	var sum v2.DelegateReplySummary
	if err := json.Unmarshal(subResp.Body, &sum); err != nil {
		t.Fatal(err)
	}

	// Before approval, delegate.output must not leak the reply body.
	preOut := object(t, tool(t, client, "delegate.output", map[string]any{"task_id": taskID, "wait_ms": 0}))
	if preOut["state"] != "running" || preOut["eof"] != false {
		t.Fatalf("preOut = %v", preOut)
	}
	if b, _ := json.Marshal(preOut); strings.Contains(string(b), secretReply) {
		t.Fatalf("unapproved delegate.output leaked reply: %s", b)
	}
	if st := a.DelegateState(); st.PendingReplies != 1 || !st.AllowSessionReplies {
		t.Fatalf("DelegateState = %+v", st)
	}

	// Preview and approve over control channel.
	prevResp, err := ctlClient.Do(t.Context(), "GET", "/v2/delegate/replies/"+sum.ReplyID, nil, nil)
	if err != nil || prevResp.Error() != nil {
		t.Fatalf("preview: err=%v resp=%v", err, prevResp)
	}
	var detail v2.DelegateReplyDetail
	if err := json.Unmarshal(prevResp.Body, &detail); err != nil || detail.Body != secretReply {
		t.Fatalf("detail = %+v, err=%v", detail, err)
	}
	appResp, err := ctlClient.Do(t.Context(), "POST", "/v2/delegate/replies/"+sum.ReplyID+"/approve", v2.DelegateReplyApproveRequest{
		BodySHA256: detail.BodySHA256,
	}, nil)
	if err != nil || appResp.Error() != nil {
		t.Fatalf("approve: err=%v resp=%v", err, appResp)
	}

	// Now delegate.output returns the approved reply.
	postOut := object(t, tool(t, client, "delegate.output", map[string]any{"task_id": taskID, "wait_ms": 0}))
	if postOut["state"] != "done" || postOut["eof"] != true || postOut["result"] != secretReply {
		t.Fatalf("postOut = %v", postOut)
	}

	// Verify audit logs never contain either secretPrompt or secretReply.
	files, _ := filepath.Glob(filepath.Join(auditDir, "*.jsonl"))
	if len(files) == 0 {
		t.Fatal("expected audit files")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), secretPrompt) || strings.Contains(string(b), secretReply) {
			t.Fatalf("audit log leaked prompt or reply body: %s", b)
		}
	}

	// Hot reload turning allow_session_replies off cancels any pending reply.
	sendRes2 := object(t, tool(t, client, "delegate.send", map[string]any{
		"session_id": "sess-e2e",
		"message":    "second",
	}))
	<-delivered
	sub2, err := a.SubmitDelegateReply(v2.DelegateReplyCreateRequest{
		MessageID: sendRes2["message_id"].(string),
		SessionID: "sess-e2e",
		Body:      "PENDING-TO-CANCEL",
	})
	if err != nil {
		t.Fatal(err)
	}
	rewriteConfigFile(t, path, func(agent map[string]any) {
		d := agent["delegate"].(map[string]any)
		d["allow_session_replies"] = false
	})
	eventually(t, "hot reload cancels pending session replies", func() bool {
		out := object(t, tool(t, client, "delegate.output", map[string]any{"task_id": sendRes2["task_id"]}))
		return out["state"] == "cancelled" && out["eof"] == true
	})
	det2, err := a.PreviewDelegateReply(sub2.ReplyID)
	if err != nil || det2.Body != "" || det2.State != v2.DelegateReplyCancelled {
		t.Fatalf("det2 after hot disable = %+v, err = %v", det2, err)
	}
}

// stubControlHost satisfies control.Host for control.Start in agent tests.
type stubControlHost struct{}

func (stubControlHost) Status() v2.StatusResponse                { return v2.StatusResponse{} }
func (stubControlHost) Draining() bool                           { return false }
func (stubControlHost) Peers(context.Context) ([]v2.Peer, error) { return nil, nil }
func (stubControlHost) Sessions() v2.SessionsResponse            { return v2.SessionsResponse{} }
func (stubControlHost) Drain(bool) (int, int)                    { return 0, 0 }
func (stubControlHost) Resume()                                  {}
var _ http.Handler = (*http.ServeMux)(nil)
