package agent

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

// CTR-BE-02 only says invalid_args; the audit asked for a reason so callers
// can tell a typo from a shared backend.
func Test_BackendLeaseReason(t *testing.T) {
	path, _ := configuration(t)
	b, _ := os.ReadFile(path)
	var doc map[string]any
	if e := json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	doc["agent"].(map[string]any)["backends"] = map[string]any{
		"ida":  map[string]any{"transport": "stdio", "command": []string{"cmd", "/c", "exit", "0"}, "policy": "exclusive"},
		"docs": map[string]any{"transport": "stdio", "command": []string{"cmd", "/c", "exit", "0"}, "policy": "shared"},
	}
	b, _ = json.Marshal(doc)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	store, e := config.NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")

	out := tool(t, client, "backend.acquire", map[string]any{"name": "missing", "ttl": "1m"})
	if !out.IsError || object(t, out)["reason"] != "unknown_backend" {
		t.Fatalf("unknown backend: %v", object(t, out))
	}
	out = tool(t, client, "backend.acquire", map[string]any{"name": "docs", "ttl": "1m"})
	if !out.IsError || object(t, out)["reason"] != "not_exclusive" {
		t.Fatalf("shared backend: %v", object(t, out))
	}
}
