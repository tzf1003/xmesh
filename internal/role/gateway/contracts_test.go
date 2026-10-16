package gateway

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// device is a scripted Agent standing in for one remote node.
type scripted struct {
	server *mcp.Server
}

func newScripted() *scripted {
	s := mcp.NewServer(&mcp.Implementation{Name: "device", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "sys.info", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return agent.Result(map[string]any{"os": "linux", "backends": []map[string]any{{"name": "ida", "favorite": true}}, "limits": map[string]any{}}, nil), nil
	})
	return &scripted{server: s}
}

func (s *scripted) tool(name string, schema map[string]any, handler func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	s.server.AddTool(&mcp.Tool{Name: name, InputSchema: schema}, handler)
}

func (s *scripted) gateway(t *testing.T, o Options) (*Gateway, *mcp.ClientSession) {
	t.Helper()
	o.NodeName = "test-node"
	o.LocalPeer = mesh.Peer{Name: "test-node", ID: "device"}
	o.Dial = func(ctx context.Context, _ mesh.Peer, _ Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		one, two := mcp.NewInMemoryTransports()
		if _, e := s.server.Connect(ctx, one, nil); e != nil {
			return nil, e
		}
		return mcpmux.Connect(ctx, two, opts)
	}
	g := New(o)
	t.Cleanup(g.Close)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
		return Principal{ClientID: "client", OAuthSession: "grant-family", Local: o.Ingress == false}, nil
	}))
	t.Cleanup(server.Close)
	return g, connect(t, server.URL+"/mcp")
}

func echoTool(received chan<- map[string]any) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		_ = json.Unmarshal(r.Params.Arguments, &args)
		received <- args
		return agent.Result(map[string]any{"ok": true}, nil), nil
	}
}

func Test_CTRGW02_03_ToolSchemas(t *testing.T) {
	g, _ := fixture(t, false)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	list, e := client.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		tools[tool.Name] = tool
	}
	schema := func(name string) map[string]any {
		tool := tools[name]
		if tool == nil {
			t.Fatal("tool missing", name)
		}
		if tool.Description == "" || tool.Description == name {
			t.Fatal("description is only the tool name", name)
		}
		b, _ := json.Marshal(tool.InputSchema)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return out
	}
	if p, _ := schema("devices.list")["properties"].(map[string]any); len(p) != 0 {
		t.Fatal("devices.list takes no parameters")
	}
	search := schema("tools.search")
	props, _ := search["properties"].(map[string]any)
	if props["device"] == nil || props["pattern"] == nil || len(search["required"].([]any)) != 2 {
		t.Fatal(search)
	}
	defaults := schema("session.set_defaults")
	props, _ = defaults["properties"].(map[string]any)
	if props["device"] == nil || props["cwd"] == nil || props["env"] == nil || defaults["minProperties"] != float64(1) {
		t.Fatal(defaults)
	}
}

func Test_CTRGW01_03_SessionDefaultsNotInjectedIntoBackends(t *testing.T) {
	device := newScripted()
	fsReads, backendCalls := make(chan map[string]any, 4), make(chan map[string]any, 4)
	device.tool("fs.read", map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "cwd": map[string]any{"type": "string"}}}, echoTool(fsReads))
	// A backend tool that happens to declare cwd and env must still be untouched.
	device.tool("ida.open", map[string]any{"type": "object", "properties": map[string]any{"cwd": map[string]any{"type": "string"}, "env": map[string]any{"type": "object"}}}, echoTool(backendCalls))
	_, client := device.gateway(t, Options{})
	if o := call(t, client, "session.set_defaults", map[string]any{"device": "local", "cwd": "/work", "env": map[string]any{"A": "1"}}); o.IsError {
		t.Fatal(o)
	}
	if o := call(t, client, "fs.read", map[string]any{"path": "a"}); o.IsError {
		t.Fatal(o)
	}
	got := <-fsReads
	if got["cwd"] != "/work" {
		t.Fatal("builtin did not receive the cwd default", got)
	}
	if _, injected := got["env"]; injected {
		t.Fatal("env injected into a tool that does not declare it", got)
	}
	if o := call(t, client, "ida.open", map[string]any{"binary": "x"}); o.IsError {
		t.Fatal(o)
	}
	got = <-backendCalls
	if _, injected := got["cwd"]; injected {
		t.Fatal("backend tool received session cwd", got)
	}
	if _, injected := got["env"]; injected {
		t.Fatal("backend tool received session env", got)
	}
	if got["binary"] != "x" {
		t.Fatal("backend arguments altered", got)
	}
	// An explicit value, even an empty string, is never overridden.
	if o := call(t, client, "fs.read", map[string]any{"path": "a", "cwd": ""}); o.IsError {
		t.Fatal(o)
	}
	if got = <-fsReads; got["cwd"] != "" {
		t.Fatal("explicit empty cwd overridden", got)
	}
}

func Test_CTRLEASE01_GatewayRenew(t *testing.T) {
	device := newScripted()
	var mu sync.Mutex
	renewals, releases := 0, 0
	var renewArgs []map[string]any
	device.tool("backend.acquire", map[string]any{"type": "object"}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return agent.Result(map[string]any{"resource": "backend:ida", "owner": "n/s", "token": 7, "expires": time.Now().Add(time.Second).UTC().Format(time.RFC3339)}, nil), nil
	})
	device.tool("backend.renew", map[string]any{"type": "object"}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		_ = json.Unmarshal(r.Params.Arguments, &args)
		mu.Lock()
		renewals++
		renewArgs = append(renewArgs, args)
		mu.Unlock()
		return agent.Result(map[string]any{"resource": "backend:ida", "token": 7}, nil), nil
	})
	device.tool("backend.release", map[string]any{"type": "object"}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mu.Lock()
		releases++
		mu.Unlock()
		return agent.Result(map[string]any{"released": true}, nil), nil
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return renewals }
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if ok() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("timed out waiting for", what)
	}
	g, client := device.gateway(t, Options{})
	if o := call(t, client, "local/backend.acquire", map[string]any{"name": "ida", "ttl": "400ms"}); o.IsError {
		t.Fatal(o)
	}
	// Half the TTL is 200ms: several renewals prove the heartbeat repeats.
	waitFor("repeated renewal", func() bool { return count() >= 3 })
	mu.Lock()
	first := renewArgs[0]
	mu.Unlock()
	if first["name"] != "ida" || first["_lease_token"] != float64(7) || first["ttl"] != "400ms" {
		t.Fatal("renew arguments", first)
	}
	// Releasing stops the heartbeat.
	if o := call(t, client, "local/backend.release", map[string]any{"name": "ida", "_lease_token": 7}); o.IsError {
		t.Fatal(o)
	}
	time.Sleep(50 * time.Millisecond)
	settled := count()
	time.Sleep(600 * time.Millisecond)
	if count() != settled {
		t.Fatal("renewals continued after release")
	}
	// A new lease renews until the upstream session ends.
	if o := call(t, client, "local/backend.acquire", map[string]any{"name": "ida", "ttl": "400ms"}); o.IsError {
		t.Fatal(o)
	}
	waitFor("renewal of the second lease", func() bool { return count() > settled })
	_ = client.Close()
	waitFor("view to close", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.views) == 0 })
	settled = count()
	time.Sleep(600 * time.Millisecond)
	if count() != settled {
		t.Fatal("renewals continued after the upstream session ended")
	}
}

func Test_CTRLEASE01_RenewStopsWhenLeaseIsLost(t *testing.T) {
	device := newScripted()
	var mu sync.Mutex
	renewals := 0
	device.tool("backend.acquire", map[string]any{"type": "object"}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return agent.Result(map[string]any{"resource": "backend:ida", "token": 1}, nil), nil
	})
	device.tool("backend.renew", map[string]any{"type": "object"}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mu.Lock()
		renewals++
		mu.Unlock()
		return agent.Result(nil, toolerr.New("resource_busy", "lease gone", "reason", "token_stale")), nil
	})
	_, client := device.gateway(t, Options{})
	if o := call(t, client, "local/backend.acquire", map[string]any{"name": "ida", "ttl": "200ms"}); o.IsError {
		t.Fatal(o)
	}
	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if renewals != 1 {
		t.Fatal("a lost lease must be renewed once and then abandoned, got", renewals)
	}
}

func Test_CTRMUX02_ProgressUnroutedAudited(t *testing.T) {
	device := newScripted()
	device.tool("noisy.run", map[string]any{"type": "object"}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		for range 3 {
			_ = r.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: "no-such-call", Progress: 1})
		}
		return agent.Result(map[string]any{"ok": true}, nil), nil
	})
	dir := t.TempDir()
	logger, e := audit.New(dir, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(logger.Close)
	_, client := device.gateway(t, Options{Audit: logger, NodeID: "local"})
	if o := call(t, client, "local/noisy.run", map[string]any{}); o.IsError {
		t.Fatal(o)
	}
	var rows []map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(unrouted(t, dir)) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	rows = unrouted(t, dir)
	if len(rows) != 1 {
		t.Fatal("expected one rate-limited progress_unrouted record, got", rows)
	}
	row := rows[0]
	if row["device"] != "test-node" || row["status"] != "ok" || !regexp.MustCompile(`^local/[A-Za-z0-9._:-]+$`).MatchString(row["caller"].(string)) {
		t.Fatal(row)
	}
	if row["oauth_session"] == nil || row["oauth_session"] == "grant-family" {
		t.Fatal("oauth_session absent or not digested", row)
	}
}

func unrouted(t *testing.T, dir string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, file := range files {
		b, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) == nil && row["event"] == "progress_unrouted" {
				out = append(out, row)
			}
		}
	}
	return out
}

func Test_CTRAUD01_CallerIsAgentCallerID(t *testing.T) {
	g, _ := fixture(t, true)
	gatewayDir := t.TempDir()
	gatewayLog, e := audit.New(gatewayDir, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(gatewayLog.Close)
	g.opts.Audit, g.opts.NodeID = gatewayLog, "local"
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
		return Principal{ClientID: "claude", OAuthSession: "grant-family", Scopes: []string{"devices:read", "sys:*"}}, nil
	}))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	if o := call(t, client, "devices.list", map[string]any{}); o.IsError {
		t.Fatal(o)
	}
	if o := call(t, client, "test-node/sys.info", map[string]any{}); o.IsError {
		t.Fatal(o)
	}
	rows := auditRows(t, gatewayDir)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		caller, _ := row["caller"].(string)
		if strings.HasPrefix(caller, "gateway/") || !regexp.MustCompile(`^local/[A-Za-z0-9._:-]+$`).MatchString(caller) {
			t.Fatal("caller is not a caller id", row)
		}
		session, _ := row["oauth_session"].(string)
		if row["oauth_client"] != "claude" || len(session) == 0 || session == "grant-family" {
			t.Fatal("oauth identity", row)
		}
	}
	if rows[0]["device"] != "test-node" || rows[0]["tool"] != "devices.list" {
		t.Fatal("gateway tool is attributed to this node", rows[0])
	}
	if rows[1]["device"] != "test-node" || rows[1]["tool"] != "test-node/sys.info" {
		t.Fatal(rows[1])
	}
	if rows[0]["caller"] == rows[1]["caller"] {
		t.Fatal("a routed call must carry the downstream session, not the upstream one")
	}
	// The routed call is attributed to the session the Agent sees.
	g.mu.Lock()
	var downstream string
	for v := range g.views {
		v.mu.Lock()
		downstream = v.down["test-node"].bridge.Session.ID()
		v.mu.Unlock()
	}
	g.mu.Unlock()
	if rows[1]["caller"] != "local/"+downstream {
		t.Fatal("caller differs from the downstream session", rows[1]["caller"], downstream)
	}
}

func auditRows(t *testing.T, dir string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, file := range files {
		b, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) == nil {
				out = append(out, row)
			}
		}
	}
	return out
}

func Test_CTRGW05_AgentDrainingStopsRouting(t *testing.T) {
	device := newScripted()
	var mu sync.Mutex
	draining := false
	device.tool("exec.run", map[string]any{"type": "object"}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if draining {
			return agent.Result(nil, toolerr.New("policy_denied", "节点正在下线", "rule", "mesh.draining")), nil
		}
		return agent.Result(map[string]any{"ok": true}, nil), nil
	})
	g, client := device.gateway(t, Options{})
	g.Update([]mesh.Peer{{ID: "peer-1", Name: "remote", Address: "http://unused", Online: true}})
	if o := call(t, client, "remote/exec.run", map[string]any{}); o.IsError {
		t.Fatal(o)
	}
	mu.Lock()
	draining = true
	mu.Unlock()
	if o := call(t, client, "remote/exec.run", map[string]any{}); !o.IsError || object(t, o)["rule"] != "mesh.draining" {
		t.Fatal("the Agent's refusal is returned unchanged", object(t, o))
	}
	// The Gateway learned from that refusal; the device is no longer routed.
	if o := call(t, client, "remote/sys.info", map[string]any{}); !o.IsError || object(t, o)["error"] != "not_found" {
		t.Fatal("device still routed after mesh.draining", object(t, o))
	}
	devices := object(t, call(t, client, "devices.list", map[string]any{}))["devices"].([]any)
	for _, row := range devices {
		if row.(map[string]any)["id"] == "remote" && row.(map[string]any)["online"] != false {
			t.Fatal("draining device still listed online", row)
		}
	}
	// A restarted Agent is a fresh process: it leaves and rejoins, and routes again.
	mu.Lock()
	draining = false
	mu.Unlock()
	g.Update(nil)
	g.Update([]mesh.Peer{{ID: "peer-1", Name: "remote", Address: "http://unused", Online: true}})
	if o := call(t, client, "remote/exec.run", map[string]any{}); o.IsError {
		t.Fatal("drain mark outlived the device's restart", object(t, o))
	}
}
