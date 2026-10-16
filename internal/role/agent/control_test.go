package agent

import (
	"net/http/httptest"
	"testing"
)

func Test_ControlResumeAcceptsWorkAgain(t *testing.T) {
	_, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	a.Drain(false)
	if !a.Draining() {
		t.Fatal("not draining")
	}
	if o := tool(t, client, "pty.open", map[string]any{"cols": 80, "rows": 24}); !o.IsError {
		t.Fatal("drained agent opened a terminal")
	}
	a.Resume()
	if a.Draining() {
		t.Fatal("still draining")
	}
	opened := tool(t, client, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if opened.IsError {
		t.Fatal(opened)
	}
	sessions := a.Sessions()
	if len(sessions.PTY) != 1 || sessions.PTY[0].Kind != "pty" || sessions.PTY[0].CreatedAt.IsZero() || sessions.Exec == nil || sessions.Leases == nil {
		t.Fatalf("%+v", sessions)
	}
	_ = tool(t, client, "pty.close", map[string]any{"session_id": object(t, opened)["session_id"]})
}

func Test_ControlOverviewIsUnfilteredByTags(t *testing.T) {
	_, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	ov := a.Overview()
	if ov.Limits.PTYMaxSessions == 0 || ov.Limits.ExecShared == 0 || ov.Warnings == nil || ov.Backends == nil || ov.Workspaces == nil || ov.Hostname == "" || ov.PathSep == "" {
		t.Fatalf("%+v", ov)
	}
}
