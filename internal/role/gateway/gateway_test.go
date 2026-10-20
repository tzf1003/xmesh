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
	"sync/atomic"
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

func Test_MeshDrainIncludesFutureDiscovery(t *testing.T) {
	g, _ := fixture(t, false)
	g.Drain("")
	g.mu.Lock()
	g.devices["later"] = device{peer: mesh.Peer{ID: "later", Name: "later", Online: true}, online: true}
	g.mu.Unlock()
	devices, _ := g.snapshot()
	for name, device := range devices {
		if device.online {
			t.Fatal("drained mesh exposed discovered peer", name)
		}
	}
}

func Test_AuditUnknownToolResult(t *testing.T) {
	g, _ := fixture(t, false)
	dir := t.TempDir()
	logger, err := audit.New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	g.opts.Audit = logger
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	s := connect(t, server.URL+"/mcp")
	if _, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "local/sys.missing", Arguments: map[string]any{}}); err == nil {
		t.Fatal("unknown tool accepted")
	}
	if out := call(t, s, "local/sys.info", map[string]any{}); out.IsError {
		t.Fatal(out)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, path := range files {
		body, _ := os.ReadFile(path)
		if strings.Contains(string(body), `"tool":"local/sys.missing"`) && strings.Contains(string(body), `"error":"internal"`) && strings.Contains(string(body), `"execution_id":"x-`) && strings.Contains(string(body), `"layer":"gateway"`) {
			return
		}
	}
	t.Fatal("failed tool audit missing")
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

// conformance: CTR-GW-01 CTR-GW-03
func Test_CTRGW01_03_SessionDefaults(t *testing.T) {
	g, root := fixture(t, false)
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
	written := call(t, one, "fs.write", map[string]any{"path": "a", "content": "hello"})
	if written.IsError {
		t.Fatal(written)
	}
	if body, err := os.ReadFile(filepath.Join(root, "a")); err != nil || string(body) != "hello" {
		entries, listErr := os.ReadDir(root)
		t.Fatal("write result and disk differ", object(t, written), string(body), err, entries, listErr)
	}
	o := call(t, two, "local/fs.read", map[string]any{"path": "a"})
	if o.IsError || !strings.Contains(object(t, o)["content"].(string), "hello") {
		entries, listErr := os.ReadDir(root)
		t.Fatal(o, "write", object(t, written), "entries", entries, listErr)
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

// conformance: INV-08
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

// conformance: INV-09
func Test_INV09_OfflineRoutesRemoved(t *testing.T) {
	g, _ := fixture(t, false)
	local := g.opts.LocalPeer
	g.Update([]mesh.Peer{{ID: "peer-1", Name: "remote", Address: local.Address, Online: true}})
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	// A target call waits for that peer's initial asynchronous catalogue.
	if out := call(t, client, "remote/sys.info", map[string]any{}); out.IsError {
		t.Fatal(out)
	}
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

// conformance: CTR-AUD-01
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

// conformance: CTR-GW-02
func Test_CTRGW02_CatalogRefreshAndFavorite(t *testing.T) {
	backend := mcp.NewServer(&mcp.Implementation{Name: "device", Version: "1"}, nil)
	backend.AddTool(&mcp.Tool{Name: "sys.info", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return agent.Result(map[string]any{"os": "linux", "backends": []map[string]any{{"name": "ida", "favorite": false}}, "limits": map[string]any{"pty_in_use": 2, "pty_max_sessions": 20, "exec_shared_in_use": 1}}, nil), nil
	})
	install := func(description string) {
		for _, name := range []string{"ida.echo", "sys.echo"} {
			backend.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: map[string]any{"type": "object", "properties": map[string]any{description: map[string]any{"type": "string"}}}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return agent.Result(map[string]any{"ok": true}, nil), nil
			})
		}
	}
	install("first")
	g := New(Options{NodeName: "test-node", Favorite: true, LocalPeer: mesh.Peer{Name: "test-node", ID: "device"}, Dial: func(ctx context.Context, _ mesh.Peer, _ Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		one, two := mcp.NewInMemoryTransports()
		if _, e := backend.Connect(ctx, one, nil); e != nil {
			return nil, e
		}
		return mcpmux.Connect(ctx, two, opts)
	}})
	t.Cleanup(g.Close)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	list, e := client.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, tool := range list.Tools {
		if tool.Name == "local/ida.echo" {
			t.Fatal("non-favorite exposed")
		}
	}
	search := func() map[string]any {
		o := call(t, client, "tools.search", map[string]any{"device": "local", "pattern": "ida.echo"})
		if o.IsError {
			t.Fatal(o)
		}
		rows := object(t, o)["tools"].([]any)
		if len(rows) != 1 {
			t.Fatal(rows)
		}
		return rows[0].(map[string]any)
	}
	if search()["description"] != "first" {
		t.Fatal("initial description")
	}
	if o := call(t, client, "local/ida.echo", map[string]any{}); o.IsError {
		t.Fatal(o)
	}
	devices := object(t, call(t, client, "devices.list", map[string]any{}))["devices"].([]any)
	row := devices[0].(map[string]any)
	if row["backends"].([]any)[0] != "ida" || row["load"].(map[string]any)["exec_shared_in_use"] != float64(1) {
		t.Fatal(row)
	}
	install("updated")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if search()["description"] == "updated" {
			list, e := client.ListTools(context.Background(), nil)
			if e != nil {
				t.Fatal(e)
			}
			for _, tool := range list.Tools {
				if tool.Name == "local/sys.echo" && tool.Description == "updated" {
					b, _ := json.Marshal(tool.InputSchema)
					if !strings.Contains(string(b), `"updated"`) {
						t.Fatal("upstream schema remained stale", string(b))
					}
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("tool metadata remained stale")
}

func Test_DiscoveryWakesOnChangeSignal(t *testing.T) {
	var polls atomic.Int32
	changed := make(chan struct{}, 1)
	g := New(Options{NodeName: "n", Changed: changed, Discover: func(context.Context) ([]mesh.Peer, error) {
		polls.Add(1)
		return nil, nil
	}})
	t.Cleanup(g.Close)
	deadline := time.Now().Add(time.Second)
	for polls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	changed <- struct{}{}
	for polls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if polls.Load() < 2 {
		t.Fatal("a change signal did not poll ahead of the 10 s ticker")
	}
}

func TestDiscoveryIdentifiesSelfByID(t *testing.T) {
	g, _ := fixture(t, false)
	self := g.opts.LocalPeer
	self.Name = "self-advertisement"
	remote := mesh.Peer{ID: "remote-key", Name: "test-node-remote", DisplayName: g.opts.LocalPeer.Name, Online: true, Address: g.opts.LocalPeer.Address}
	g.Update([]mesh.Peer{self, remote})
	devices, _ := g.snapshot()
	if len(devices) != 2 || devices[g.opts.LocalPeer.Name].peer.ID != g.opts.LocalPeer.ID || devices[remote.Name].peer.ID != remote.ID {
		t.Fatal("identity-based discovery", devices)
	}
	if _, exists := devices[self.Name]; exists {
		t.Fatal("self advertisement inserted as remote")
	}
}
