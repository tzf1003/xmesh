package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
)

// conformanceAgent starts an Agent on a real loopback HTTP listener. mutate
// edits the agent section of the base test configuration before the store is
// opened. The returned root is the (symlink-resolved) allow root and default cwd.
func conformanceAgent(t *testing.T, mutate func(agent map[string]any, root string)) (*Agent, string, string) {
	t.Helper()
	path, _ := configuration(t)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	section := doc["agent"].(map[string]any)
	root, err := filepath.EvalSymlinks(section["default_cwd"].(string))
	if err != nil {
		t.Fatal(err)
	}
	section["default_cwd"] = root
	section["cwd_policy"] = map[string]any{"allow": []string{root}}
	if mutate != nil {
		mutate(section, root)
	}
	if body, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	return a, server.URL + "/mcp", root
}

// callWithin calls a tool and fails if it does not finish inside bound. Unlike
// tool(), the deadline is chosen by the caller.
func callWithin(t *testing.T, s *mcp.ClientSession, bound time.Duration, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	out, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s did not complete within %s: %v", name, bound, err)
	}
	return out
}

// waitFor polls until cond holds, failing with what after a generous bound.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// firstActive returns the id of the first active terminal in pty.list.
func firstActive(t *testing.T, s *mcp.ClientSession) string {
	t.Helper()
	rows := object(t, tool(t, s, "pty.list", map[string]any{}))["sessions"].([]any)
	for _, row := range rows {
		m := row.(map[string]any)
		if m["state"] == "active" {
			return m["id"].(string)
		}
	}
	t.Fatal("no active terminal", rows)
	return ""
}
