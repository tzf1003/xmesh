package gateway

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/quota"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, ingress bool) (*Gateway, string) {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	c := &config.Config{}
	c.Node.Name = "test-node"
	c.Agent.DefaultCWD = root
	c.Agent.CWDPolicy = cwd.Policy{Allow: []string{root}}
	c.Agent.Authz = map[string]config.Grant{"tag:peer": {Tools: []string{"*"}, Backends: []string{"*"}}}
	c.Agent.Exec = config.Exec{MaxConcurrent: 4, ExclusiveSlots: 1, DefaultTimeout: "5s", InlineOutputBytes: "256KiB", IdempotencyCache: 20}
	c.Agent.PTY = config.PTY{MaxSessions: 20, MaxPerCaller: 8, IdleTimeout: "30m", OrphanGrace: "10m", DefaultShell: "auto", ScrollbackBytes: "1MiB", ClosedRetain: "5m", ClosedRetainMax: 20}
	a, e := agent.New(config.Static(c))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(agent.Local))
	t.Cleanup(server.Close)
	g := New(Options{NodeName: c.Node.Name, Ingress: ingress, LocalPeer: mesh.Peer{ID: "local", Name: c.Node.Name, Address: server.URL + "/mcp", Online: true, Tags: []string{"tag:device"}}, Dial: RemoteDial(server.Client())})
	t.Cleanup(g.Close)
	return g, root
}
func connect(t *testing.T, url string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	s, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func call(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, e := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func object(t *testing.T, o *mcp.CallToolResult) map[string]any {
	t.Helper()
	b, _ := json.Marshal(o.StructuredContent)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	return v
}
func Test_CTRGW01_03_SessionDefaults(t *testing.T) {
	g, _ := fixture(t, false)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	one := connect(t, server.URL+"/mcp")
	two := connect(t, server.URL+"/mcp")
	list, e := one.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, tool := range list.Tools {
		if tool.Name == "local/fs.read" {
			found = true
		}
	}
	if !found {
		t.Fatal("local tools missing")
	}
	if o := call(t, one, "session.set_defaults", map[string]any{"device": "local", "cwd": "."}); o.IsError {
		t.Fatal(o)
	}
	if o := call(t, one, "fs.write", map[string]any{"path": "a", "content": "hello"}); o.IsError {
		t.Fatal(o)
	}
	o := call(t, two, "local/fs.read", map[string]any{"path": "a"})
	if o.IsError || !strings.Contains(object(t, o)["content"].(string), "hello") {
		t.Fatal(o)
	}
	// Default device is confined to the first connection.
	o = call(t, two, "fs.read", map[string]any{"path": "a"})
	if !o.IsError {
		t.Fatal("defaults leaked")
	}
	if o = call(t, one, "session.set_defaults", map[string]any{"device": nil}); o.IsError {
		t.Fatal(o)
	}
	if o = call(t, one, "fs.read", map[string]any{"path": "a"}); !o.IsError {
		t.Fatal("default not cleared")
	}
}
func Test_INV08_IngressScopes(t *testing.T) {
	g, _ := fixture(t, true)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
		return Principal{ClientID: "client", Scopes: []string{"devices:read", "fs:read:*"}}, nil
	}))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	list, e := client.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, tool := range list.Tools {
		if strings.HasPrefix(tool.Name, "local/") || strings.HasSuffix(tool.Name, "fs.write") {
			t.Fatal("scope or prefix", tool.Name)
		}
	}
	o := call(t, client, "test-node/fs.write", map[string]any{"path": "a", "content": "x"})
	if !o.IsError || object(t, o)["error"] != "unauthorized" {
		t.Fatal(o)
	}
	o = call(t, client, "tools.search", map[string]any{"device": "test-node", "pattern": "fs.read"})
	if o.IsError || len(object(t, o)["tools"].([]any)) != 1 {
		t.Fatal(o)
	}
}
func Test_INV09_OfflineRoutesRemoved(t *testing.T) {
	g, _ := fixture(t, false)
	local := g.opts.LocalPeer
	g.Update([]mesh.Peer{{ID: "peer-1", Name: "remote", Address: local.Address, Online: true}})
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	list, e := client.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, tool := range list.Tools {
		if tool.Name == "remote/sys.info" {
			found = true
		}
	}
	if !found {
		t.Fatal("peer tools absent")
	}
	g.Update(nil)
	o := call(t, client, "remote/sys.info", map[string]any{})
	if !o.IsError || object(t, o)["error"] != "not_found" {
		t.Fatal(o)
	}
}

var _ = mcpmux.Options{}

func Test_CTRAUD01_RejectedCalls(t *testing.T) {
	g, _ := fixture(t, true)
	dir := t.TempDir()
	logger, e := audit.New(dir, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	defer logger.Close()
	g.opts.Audit = logger
	g.quota = quota.New(2, 2, time.Minute)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
		return Principal{ClientID: "audited-client", Scopes: []string{"fs:read:*"}}, nil
	}))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	for _, name := range []string{"test-node/fs.write", "offline/fs.read", "test-node/fs.read"} {
		if o := call(t, client, name, map[string]any{"path": "a", "content": "fixture-body"}); !o.IsError {
			t.Fatal(o)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var rows []map[string]any
	for _, file := range files {
		b, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(b), "fixture-body") {
			t.Fatal("body in audit")
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var r map[string]any
			if e = json.Unmarshal([]byte(line), &r); e != nil {
				t.Fatal(e)
			}
			rows = append(rows, r)
		}
	}
	if len(rows) != 3 {
		t.Fatal(rows)
	}
	for i, code := range []string{"unauthorized", "not_found", "policy_denied"} {
		if rows[i]["error"] != code || rows[i]["oauth_client"] != "audited-client" {
			t.Fatal(rows[i])
		}
	}
}
