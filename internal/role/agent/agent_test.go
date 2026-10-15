package agent

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func configuration(t *testing.T) (string, *config.Store) {
	t.Helper()
	root := t.TempDir()
	doc := map[string]any{"node": map[string]any{"name": "test-node", "roles": []string{"agent"}}, "mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}}, "agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "authz": map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}, "pty": map[string]any{"default_shell": "auto"}}}
	b, _ := json.Marshal(doc)
	path := filepath.Join(root, "config.json")
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := config.NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	return path, s
}
func connect(t *testing.T, url string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { session.Close() })
	return session
}
func tool(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, e := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func object(t *testing.T, r *mcp.CallToolResult) map[string]any {
	t.Helper()
	b, e := json.Marshal(r.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func Test_Gate1_MCPRealHTTP(t *testing.T) {
	_, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	ctx := context.Background()
	list, e := client.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(list.Tools) != 22 {
		t.Fatal("tools", len(list.Tools))
	}
	for _, name := range []string{"fs.write", "fs.read", "sys.info", "exec.run"} {
		args := map[string]any{}
		switch name {
		case "fs.write":
			args = map[string]any{"path": "test.txt", "content": "Hello MCP\n", "caller_id": "forged/owner"}
		case "fs.read":
			args["path"] = "test.txt"
		case "exec.run":
			if os.PathSeparator == '\\' {
				args = map[string]any{"cmd": "powershell", "args": []string{"-NoProfile", "-Command", "Write-Output hello"}}
			} else {
				args = map[string]any{"cmd": "printf", "args": []string{"hello"}}
			}
		}
		o := tool(t, client, name, args)
		if o.IsError {
			t.Fatalf("%s: %v", name, o.Content)
		}
	}
	o := tool(t, client, "pty.open", map[string]any{"cols": 80, "rows": 24, "owner": "forged/owner"})
	if o.IsError {
		t.Fatal(o.Content)
	}
	v := object(t, o)
	owner := v["owner"].(string)
	if !strings.HasPrefix(owner, "local/") || owner == "forged/owner" {
		t.Fatal(v)
	}
	id := v["session_id"]
	if o = tool(t, client, "pty.write", map[string]any{"session_id": id, "data": "echo MCP_TERMINAL\n"}); o.IsError {
		t.Fatal(o.Content)
	}
	if o = tool(t, client, "pty.read", map[string]any{"session_id": id, "wait_ms": 1000}); o.IsError {
		t.Fatal(o.Content)
	}
	other := connect(t, server.URL+"/mcp")
	o = tool(t, other, "pty.write", map[string]any{"session_id": id, "data": "bad\n"})
	if !o.IsError || object(t, o)["error"] != "not_owner" {
		t.Fatal(o)
	}
	o = tool(t, client, "fs.write", map[string]any{"path": "bad.txt"})
	if !o.IsError || object(t, o)["error"] != "invalid_args" {
		t.Fatal(o)
	}
	if e = client.Close(); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.PTY.List()[0]["state"] == "orphaned" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("disconnect did not orphan PTY")
}
func Test_CONSEC06_ReloadNextCall(t *testing.T) {
	path, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	b, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	agent := doc["agent"].(map[string]any)
	agent["authz"] = map[string]any{"tag:peer": map[string]any{"tools": []string{"sys.*"}, "backends": []string{}}}
	b, _ = json.Marshal(doc)
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	o := tool(t, client, "fs.write", map[string]any{"path": "denied", "content": "x"})
	if !o.IsError || object(t, o)["error"] != "unauthorized" {
		t.Fatal(o)
	}
	if _, e = os.Stat(filepath.Join(filepath.Dir(path), "denied")); !os.IsNotExist(e) {
		t.Fatal("denied mutation happened")
	}
	list, e := client.ListTools(context.Background(), nil)
	if e != nil || len(list.Tools) != 1 || list.Tools[0].Name != "sys.info" {
		t.Fatal(list, e)
	}
	if e = os.WriteFile(path, []byte("broken: ["), 0600); e != nil {
		t.Fatal(e)
	}
	if o = tool(t, client, "sys.info", map[string]any{}); o.IsError {
		t.Fatal(o)
	}
}
