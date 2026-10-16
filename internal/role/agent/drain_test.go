package agent

import (
	"net/http/httptest"
	"testing"
)

// conformance: CTR-GW-05
func Test_CTRGW05_DrainKeepsExistingResources(t *testing.T) {
	_, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	opened := tool(t, client, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if opened.IsError {
		t.Fatal(opened)
	}
	id := object(t, opened)["session_id"]
	a.Drain(false)
	denied := tool(t, client, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if !denied.IsError || object(t, denied)["rule"] != "mesh.draining" {
		t.Fatal(denied)
	}
	if o := tool(t, client, "pty.write", map[string]any{"session_id": id, "data": "echo existing\n"}); o.IsError {
		t.Fatal(o)
	}
	a.Drain(true)
	_, active, _ := a.PTY.Counts()
	if active != 0 {
		t.Fatal("kill retained active", active)
	}
}
