package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/oauth"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

func requirePTYHost(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" && os.Getenv("XMESH_TEST_WINDOWS_PTY") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_PTY=1 on Windows with ConPTY and PowerShell")
	}
}

// httpBackend serves a loopback MCP backend with one tool, echo, and counts
// the calls that reach it.
func httpBackend(t *testing.T, hits *atomic.Int32) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "http-backend", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		hits.Add(1)
		var args map[string]any
		_ = json.Unmarshal(r.Params.Arguments, &args)
		return agent.Result(map[string]any{"args": args}, nil), nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

func conformanceAgent(t *testing.T, name string, authz map[string]config.Grant, backends map[string]config.Backend) (*agent.Agent, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	c := &config.Config{}
	c.Node.Name = name
	c.Agent.DefaultCWD = root
	c.Agent.CWDPolicy = cwd.Policy{Allow: []string{root}}
	c.Agent.Authz = authz
	c.Agent.Backends = backends
	shell := "/bin/sh"
	if runtime.GOOS == "windows" {
		shell = "auto"
	}
	c.Agent.Exec = config.Exec{MaxConcurrent: 4, ExclusiveSlots: 1, DefaultTimeout: "5s", InlineOutputBytes: "256KiB", IdempotencyCache: 20}
	c.Agent.PTY = config.PTY{MaxSessions: 20, MaxPerCaller: 8, IdleTimeout: "30m", OrphanGrace: "10m", DefaultShell: shell, ScrollbackBytes: "1MiB", ClosedRetain: "5m", ClosedRetainMax: 20}
	a, err := agent.New(config.Static(c))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, root
}

func transportDial(a *agent.Agent, principal func(Principal) identity.Principal) Dial {
	return func(ctx context.Context, _ mesh.Peer, p Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		transport, err := a.Transport(ctx, principal(p))
		if err != nil {
			return nil, err
		}
		return mcpmux.Connect(ctx, transport, opts)
	}
}

func ingressPrincipal(p Principal) identity.Principal {
	return identity.Principal{Node: "ingress-node", Tags: []string{"tag:ingress"}, OAuthClient: p.ClientID}
}

var allScopes = []string{"devices:read", "sys:*", "exec:*", "pty:*", "fs:*", "backend:*:*"}

func grantAll() config.Grant { return config.Grant{Tools: []string{"*"}, Backends: []string{"*"}} }

func waitFor(t *testing.T, what string, limit time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func hasTool(t *testing.T, s *mcp.ClientSession, name string) bool {
	t.Helper()
	list, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// conformance: INV-03 CON-ARCH-04
func Test_INV03_GatewayAndIngressPersistNoDeviceResourceState(t *testing.T) {
	requirePTYHost(t)
	var hits atomic.Int32
	a, _ := conformanceAgent(t, "device", map[string]config.Grant{"tag:ingress": grantAll()}, map[string]config.Backend{"ida": {Transport: "http", URL: httpBackend(t, &hits), Policy: "exclusive", IdleTimeout: "30m"}})
	state := t.TempDir()
	logger, err := audit.New(filepath.Join(state, "audit"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	budget, err := NewPTYBudget(8, filepath.Join(state, "ingress-pty.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := New(Options{NodeName: "ingress", Ingress: true, LocalPeer: mesh.Peer{Name: "device", ID: "device", Online: true}, Dial: transportDial(a, ingressPrincipal), Audit: logger, PTYBudget: budget})
	t.Cleanup(g.Close)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{ClientID: "cloud", Scopes: allScopes}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")

	opened := object(t, call(t, client, "device/pty.open", map[string]any{"cols": 80, "rows": 24}))
	sessionID, owner := opened["session_id"].(string), opened["owner"].(string)
	if o := call(t, client, "device/pty.write", map[string]any{"session_id": sessionID, "data": "echo INV03_PTY_MARKER\n"}); o.IsError {
		t.Fatal(object(t, o))
	}
	waitFor(t, "terminal output", 10*time.Second, func() bool {
		out := object(t, call(t, client, "device/pty.read", map[string]any{"session_id": sessionID, "wait_ms": 200}))
		data, _ := out["data"].(string)
		return strings.Contains(data, "INV03_PTY_MARKER")
	})
	lease := object(t, call(t, client, "device/backend.acquire", map[string]any{"name": "ida", "ttl": "5m"}))
	if lease["token"] == nil {
		t.Fatal(lease)
	}
	if o := call(t, client, "device/ida.echo", map[string]any{"_lease_token": lease["token"], "value": "INV03_BACKEND_MARKER"}); o.IsError {
		t.Fatal(object(t, o))
	}
	if o := call(t, client, "device/exec.run", map[string]any{"cmd": "printf", "args": []string{"INV03_EXEC_MARKER"}, "idempotency_key": "inv03-idempotency-key"}); o.IsError {
		t.Fatal(object(t, o))
	}
	if o := call(t, client, "device/fs.write", map[string]any{"path": "inv03.txt", "content": "INV03_FILE_MARKER"}); o.IsError {
		t.Fatal(object(t, o))
	}
	// The resources exist, and they exist on the Agent.
	if len(a.PTY.List()) != 1 || a.Leases.Owner("backend:ida") == "" {
		t.Fatal("device resources missing on the Agent; the scan below would be vacuous")
	}
	_ = client.Close()
	g.Close()
	logger.Close()

	allowedExact := map[string]bool{"ingress-pty.json": true, "ingress-pty.json.lock": true}
	denied := []string{sessionID, owner, "INV03_PTY_MARKER", "INV03_BACKEND_MARKER", "INV03_EXEC_MARKER", "INV03_FILE_MARKER", "inv03-idempotency-key", "_lease_token", "backend:ida"}
	scanned := 0
	err = filepath.WalkDir(state, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(state, path)
		rel = filepath.ToSlash(rel)
		scanned++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(rel, "audit/") && strings.HasSuffix(rel, ".jsonl"):
			// Audit records metadata only; bodies and output never appear.
			for _, secret := range []string{"INV03_PTY_MARKER", "INV03_BACKEND_MARKER", "INV03_EXEC_MARKER", "INV03_FILE_MARKER"} {
				if strings.Contains(string(body), secret) {
					t.Errorf("%s contains %s", rel, secret)
				}
			}
		case allowedExact[rel]:
			for _, secret := range denied {
				if strings.Contains(string(body), secret) {
					t.Errorf("%s contains device resource data %q", rel, secret)
				}
			}
			if rel == "ingress-pty.json" {
				var doc map[string]json.RawMessage
				if err := json.Unmarshal(body, &doc); err != nil {
					t.Errorf("%s: %v", rel, err)
					return nil
				}
				for key := range doc {
					if key != "version" && key != "limit" && key != "peers" {
						t.Errorf("%s has unexpected field %q", rel, key)
					}
				}
				var peers map[string]map[string]json.RawMessage
				_ = json.Unmarshal(doc["peers"], &peers)
				for id, peer := range peers {
					for key := range peer {
						if key != "name" && key != "epoch" && key != "pending" {
							t.Errorf("%s peer %s has unexpected field %q", rel, id, key)
						}
					}
				}
			}
		default:
			t.Errorf("unexpected file in the Gateway/ingress state directory: %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("no state files scanned")
	}
	// Restarting the gateway on the same state sees nothing it must restore: the
	// resources are still where they were, on the Agent.
	restartedBudget, err := NewPTYBudget(8, filepath.Join(state, "ingress-pty.json"))
	if err != nil {
		t.Fatal(err)
	}
	g2 := New(Options{NodeName: "ingress", Ingress: true, LocalPeer: mesh.Peer{Name: "device", ID: "device", Online: true}, Dial: transportDial(a, ingressPrincipal), PTYBudget: restartedBudget})
	t.Cleanup(g2.Close)
	if len(a.PTY.List()) != 1 || a.Leases.Owner("backend:ida") == "" {
		t.Fatal("gateway restart disturbed Agent resources")
	}
}

// conformance: CON-ARCH-04
func Test_CONARCH04_GatewayRestartKeepsAgentResources(t *testing.T) {
	requirePTYHost(t)
	var hits atomic.Int32
	a, _ := conformanceAgent(t, "device", map[string]config.Grant{"tag:peer": grantAll()}, map[string]config.Backend{"ida": {Transport: "http", URL: httpBackend(t, &hits), Policy: "exclusive", IdleTimeout: "30m"}})
	dial := transportDial(a, func(Principal) identity.Principal {
		return identity.Principal{Node: "gw-node", Tags: []string{"tag:peer"}}
	})
	peer := mesh.Peer{Name: "device", ID: "device", Online: true}
	start := func() (*Gateway, *mcp.ClientSession) {
		g := New(Options{NodeName: "device", LocalPeer: peer, Dial: dial})
		t.Cleanup(g.Close)
		server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
		t.Cleanup(server.Close)
		return g, connect(t, server.URL+"/mcp")
	}
	g1, one := start()
	opened := object(t, call(t, one, "local/pty.open", map[string]any{"cols": 80, "rows": 24}))
	sessionID, owner := opened["session_id"].(string), opened["owner"].(string)
	call(t, one, "local/pty.write", map[string]any{"session_id": sessionID, "data": "echo ARCH04_MARK\n"})
	var cursor float64
	waitFor(t, "terminal output", 10*time.Second, func() bool {
		out := object(t, call(t, one, "local/pty.read", map[string]any{"session_id": sessionID, "wait_ms": 200}))
		cursor, _ = out["next_cursor"].(float64)
		data, _ := out["data"].(string)
		return strings.Contains(data, "ARCH04_MARK")
	})
	lease := object(t, call(t, one, "local/backend.acquire", map[string]any{"name": "ida", "ttl": "10m"}))
	token := lease["token"].(float64)
	leaseOwner := a.Leases.Owner("backend:ida")
	if leaseOwner != lease["owner"] {
		t.Fatal(leaseOwner, lease)
	}

	// Stop the Gateway entirely and bring a new one up.
	_ = one.Close()
	g1.Close()
	_, two := start()

	if a.Leases.Owner("backend:ida") != leaseOwner || a.Leases.Validate("backend:ida", leaseOwner, uint64(token)) != nil {
		t.Fatal("lease owner or token changed when the Gateway restarted")
	}
	rows := a.PTY.List()
	if len(rows) != 1 || rows[0]["id"] != sessionID {
		t.Fatalf("terminal lost with the Gateway: %v", rows)
	}
	// The terminal is the same one, with its output history; the new session's
	// node is the previous owner's node, so it can adopt the orphan.
	waitFor(t, "orphan state", 5*time.Second, func() bool { return a.PTY.List()[0]["state"] == "orphaned" })
	mine := object(t, call(t, two, "local/pty.open", map[string]any{"cols": 80, "rows": 24}))
	if o := call(t, two, "local/pty.transfer", map[string]any{"session_id": sessionID, "to_caller": mine["owner"]}); o.IsError {
		t.Fatalf("adopting the orphan after restart: %v (previous owner %s)", object(t, o), owner)
	}
	replay := object(t, call(t, two, "local/pty.read", map[string]any{"session_id": sessionID, "cursor": 0, "wait_ms": 0}))
	if !strings.Contains(fmt.Sprint(replay["data"]), "ARCH04_MARK") || replay["next_cursor"].(float64) < cursor {
		t.Fatalf("terminal history changed: %v", replay)
	}
	// The old holder's token is still the only valid one: another caller is refused.
	if o := call(t, two, "local/backend.acquire", map[string]any{"name": "ida", "ttl": "1m"}); !o.IsError || object(t, o)["error"] != "resource_busy" || object(t, o)["held_by"] != leaseOwner {
		t.Fatalf("Gateway re-arbitrated the lease: %v", object(t, o))
	}
}

// conformance: CTR-AUTHZ-01 CON-NET-06 CON-NET-07 CON-SEC-01
func Test_CTRAUTHZ01_AgentDeniesIngressTagAfterScopePasses(t *testing.T) {
	var idaHits, otherHits atomic.Int32
	authz := map[string]config.Grant{"tag:ingress": {
		// ida is the only backend the ingress tag may reach; exec is not listed at all.
		Backends: []string{"ida"},
		Tools:    []string{"sys.*", "fs.*", "backend.*", "ida.*", "other.*"},
	}}
	backends := map[string]config.Backend{
		"ida":   {Transport: "http", URL: httpBackend(t, &idaHits), Policy: "exclusive", IdleTimeout: "30m"},
		"other": {Transport: "http", URL: httpBackend(t, &otherHits), Policy: "exclusive", IdleTimeout: "30m"},
	}
	a, root := conformanceAgent(t, "device", authz, backends)
	g := New(Options{NodeName: "ingress", Ingress: true, LocalPeer: mesh.Peer{Name: "device", ID: "device", Online: true}, Dial: transportDial(a, ingressPrincipal)})
	t.Cleanup(g.Close)
	// This principal passes the ingress/Gateway layer for every tool below.
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{ClientID: "cloud", Scopes: allScopes}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")

	if o := call(t, client, "device/sys.info", map[string]any{}); o.IsError {
		t.Fatal(object(t, o))
	}
	// Backend not in the tag's backends list: the Agent refuses, nothing is leased.
	o := call(t, client, "device/backend.acquire", map[string]any{"name": "other", "ttl": "1m"})
	if !o.IsError || object(t, o)["error"] != "unauthorized" {
		t.Fatalf("backend outside agent.authz was reachable from tag:ingress: %v", object(t, o))
	}
	if a.Leases.Owner("backend:other") != "" || otherHits.Load() != 0 {
		t.Fatal("denied backend call had a side effect")
	}
	if hasTool(t, client, "device/other.echo") {
		t.Fatal("tool of a backend outside agent.authz is offered to the ingress")
	}
	if out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "device/other.echo", Arguments: map[string]any{}}); err == nil && !out.IsError {
		t.Fatal("backend call succeeded without agent.authz")
	}
	if otherHits.Load() != 0 {
		t.Fatal("the denied backend received a call")
	}
	// A tool absent from the tag's tools list: refused although exec:* is in scope.
	marker := filepath.Join(root, "ran")
	if hasTool(t, client, "device/exec.run") {
		t.Fatal("exec.run is offered to a tag whose tools list does not include it")
	}
	if out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "device/exec.run", Arguments: map[string]any{"cmd": "touch", "args": []string{marker}}}); err == nil && !out.IsError {
		t.Fatal("exec reached the Agent through tag:ingress")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("denied exec ran")
	}
	// The allowed backend still works end to end.
	lease := object(t, call(t, client, "device/backend.acquire", map[string]any{"name": "ida", "ttl": "1m"}))
	if o = call(t, client, "device/ida.echo", map[string]any{"_lease_token": lease["token"]}); o.IsError || idaHits.Load() != 1 {
		t.Fatalf("allowed backend: %v hits=%d", object(t, o), idaHits.Load())
	}

	// Same-host path: a local Gateway principal needs no scope, but the Agent's
	// own layer still applies (localhost is not trusted).
	local, _ := conformanceAgent(t, "device", map[string]config.Grant{"tag:peer": {Tools: []string{"sys.*"}}}, nil)
	lg := New(Options{NodeName: "device", LocalPeer: mesh.Peer{Name: "device", ID: "device", Online: true}, Dial: transportDial(local, func(Principal) identity.Principal {
		return identity.Principal{Node: "local", Tags: []string{"tag:peer"}}
	})})
	t.Cleanup(lg.Close)
	lserver := httptest.NewServer(lg.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(lserver.Close)
	lclient := connect(t, lserver.URL+"/mcp")
	if o = call(t, lclient, "local/sys.info", map[string]any{}); o.IsError {
		t.Fatal(object(t, o))
	}
	if out, err := lclient.CallTool(context.Background(), &mcp.CallToolParams{Name: "local/fs.write", Arguments: map[string]any{"path": "x", "content": "x"}}); err == nil && !out.IsError {
		t.Fatal("local Gateway bypassed the Agent layer")
	}
}

// conformance: INV-08
func Test_INV08_LocalPrefixOnlyOnTheLocalGatewayView(t *testing.T) {
	names := func(g *Gateway, p Principal) []string {
		server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return p, nil }))
		t.Cleanup(server.Close)
		list, err := connect(t, server.URL+"/mcp").ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, tool := range list.Tools {
			out = append(out, tool.Name)
		}
		return out
	}
	has := func(list []string, name string) bool {
		for _, n := range list {
			if n == name {
				return true
			}
		}
		return false
	}
	g, _ := fixture(t, false)
	local := names(g, Principal{Local: true})
	if !has(local, "local/fs.read") || has(local, "test-node/fs.read") {
		t.Fatalf("local view: %v", local)
	}
	ingress, _ := fixture(t, true)
	cloud := names(ingress, Principal{ClientID: "cloud", Scopes: allScopes})
	if !has(cloud, "test-node/fs.read") {
		t.Fatalf("ingress view lacks the node-prefixed tools: %v", cloud)
	}
	for _, name := range cloud {
		if strings.HasPrefix(name, "local/") || strings.Contains(name, "local/") {
			t.Fatalf("ingress exposes %q", name)
		}
	}
	// A cloud client asking for local/... gets nothing routed.
	server := httptest.NewServer(ingress.HTTP(func(*http.Request) (Principal, error) { return Principal{ClientID: "cloud", Scopes: allScopes}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	if out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "local/fs.read", Arguments: map[string]any{"path": "x"}}); err == nil && !out.IsError {
		t.Fatal("ingress routed a local/ tool name")
	}
}

type fakeMesh struct {
	mu        sync.Mutex
	sessions  map[string][]*mcp.ServerSession
	dials     map[string]int
	failAfter map[string]int
	release   chan struct{}
	once      sync.Once
}

func newFakeMesh(t *testing.T) *fakeMesh {
	f := &fakeMesh{sessions: map[string][]*mcp.ServerSession{}, dials: map[string]int{}, failAfter: map[string]int{}, release: make(chan struct{})}
	t.Cleanup(f.releaseAll)
	return f
}

func (f *fakeMesh) releaseAll() { f.once.Do(func() { close(f.release) }) }

func (f *fakeMesh) dial(ctx context.Context, peer mesh.Peer, _ Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
	f.mu.Lock()
	f.dials[peer.Name]++
	n := f.dials[peer.Name]
	limit, limited := f.failAfter[peer.Name]
	f.mu.Unlock()
	if limited && n > limit {
		return nil, fmt.Errorf("%s unreachable", peer.Name)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: peer.Name, Version: "1"}, nil)
	reply := func(v map[string]any) (*mcp.CallToolResult, error) { return agent.Result(v, nil), nil }
	add := func(name string, handler func(context.Context) (*mcp.CallToolResult, error)) {
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return handler(ctx) })
	}
	add("sys.info", func(context.Context) (*mcp.CallToolResult, error) {
		return reply(map[string]any{"os": "linux", "backends": []any{}, "limits": map[string]any{}})
	})
	add("sys.whoami", func(context.Context) (*mcp.CallToolResult, error) { return reply(map[string]any{"device": peer.Name}) })
	add("sys.slow", func(ctx context.Context) (*mcp.CallToolResult, error) {
		select {
		case <-f.release:
		case <-ctx.Done():
		}
		return reply(map[string]any{"device": peer.Name})
	})
	one, two := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, one, nil)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.sessions[peer.Name] = append(f.sessions[peer.Name], ss)
	f.mu.Unlock()
	return mcpmux.Connect(ctx, two, opts)
}

// conformance: INV-09 CTR-GW-04
func Test_INV09_VanishedPeerIsNotListedRoutedOrReplaced(t *testing.T) {
	f := newFakeMesh(t)
	g := New(Options{NodeName: "gw", LocalPeer: mesh.Peer{Name: "gw", ID: "gw", Online: true}, Dial: f.dial})
	t.Cleanup(g.Close)
	pa, pb := mesh.Peer{Name: "alpha", ID: "a", Address: "mem://a", Online: true}, mesh.Peer{Name: "beta", ID: "b", Address: "mem://b", Online: true}
	g.Update([]mesh.Peer{pa, pb})
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	who := func(device string) (string, bool) {
		out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: device + "/sys.whoami", Arguments: map[string]any{}})
		if err != nil || out.IsError {
			return "", false
		}
		return object(t, out)["device"].(string), true
	}
	if d, ok := who("alpha"); !ok || d != "alpha" {
		t.Fatal(d, ok)
	}
	if d, ok := who("beta"); !ok || d != "beta" {
		t.Fatal(d, ok)
	}
	waitFor(t, "alpha tools listed", 5*time.Second, func() bool { return hasTool(t, client, "alpha/sys.whoami") })

	// An in-flight call on alpha when alpha disappears from the mesh: it may fail
	// with a transport error, but it must not be answered by beta.
	type result struct {
		out *mcp.CallToolResult
		err error
	}
	inflight := make(chan result, 1)
	go func() {
		out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "alpha/sys.slow", Arguments: map[string]any{}})
		inflight <- result{out, err}
	}()
	time.Sleep(150 * time.Millisecond)
	g.Update([]mesh.Peer{pb})
	f.releaseAll()
	select {
	case r := <-inflight:
		if r.err == nil && !r.out.IsError && object(t, r.out)["device"] == "beta" {
			t.Fatal("in-flight call was re-routed to another device")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight call to a vanished device is still pending")
	}
	waitFor(t, "alpha tools withdrawn", 5*time.Second, func() bool { return !hasTool(t, client, "alpha/sys.whoami") })
	if !hasTool(t, client, "beta/sys.whoami") {
		t.Fatal("remaining peer lost its tools")
	}
	if d, ok := who("alpha"); ok {
		t.Fatalf("vanished device still routed (answered by %q)", d)
	}
	if out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "alpha/sys.whoami", Arguments: map[string]any{}}); err != nil || !out.IsError || object(t, out)["error"] != "not_found" {
		t.Fatalf("want not_found, got %v %v", out, err)
	}

	// CTR-GW-04: when the MCP connection itself drops, routing stops at once,
	// without waiting for the 30 s capability cache. The peer remains "online"
	// in the mesh status, but the transport is gone and cannot be redialled.
	f.mu.Lock()
	f.failAfter["beta"] = f.dials["beta"]
	sessions := append([]*mcp.ServerSession{}, f.sessions["beta"]...)
	f.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
	waitFor(t, "beta routing stopped", 5*time.Second, func() bool {
		out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "beta/sys.whoami", Arguments: map[string]any{}})
		return err == nil && out.IsError && object(t, out)["error"] == "not_found"
	})
	if hasTool(t, client, "beta/sys.whoami") {
		t.Fatal("tools of a disconnected peer are still listed")
	}
}

// conformance: CTR-GW-01 CTR-GW-02 CTR-GW-03
func Test_CTRGW01_ExplicitArgumentsBeatSessionDefaultsAndControlToolsAreUnprefixed(t *testing.T) {
	g, root := fixture(t, false)
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	if o := call(t, client, "session.set_defaults", map[string]any{"device": "local", "cwd": filepath.Join(root, "sub")}); o.IsError {
		t.Fatal(object(t, o))
	}
	// Unprefixed tool gets the default device and default cwd.
	if o := call(t, client, "fs.write", map[string]any{"path": "default.txt", "content": "1"}); o.IsError {
		t.Fatal(object(t, o))
	}
	// Explicit cwd (even an explicit one equal to the root) is not overridden.
	if o := call(t, client, "fs.write", map[string]any{"path": "explicit.txt", "content": "2", "cwd": root}); o.IsError {
		t.Fatal(object(t, o))
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "default.txt")); err != nil {
		t.Fatal("default cwd not applied", err)
	}
	if _, err := os.Stat(filepath.Join(root, "explicit.txt")); err != nil {
		t.Fatal("explicit cwd was overridden by the default", err)
	}
	// The three Gateway tools take no device prefix even with a default device.
	if o := call(t, client, "devices.list", map[string]any{}); o.IsError {
		t.Fatal(object(t, o))
	}
	// Invalid defaults are refused; null clears a single key and leaves the rest.
	if o := call(t, client, "session.set_defaults", map[string]any{"device": "no-such-device"}); !o.IsError || object(t, o)["error"] != "invalid_args" {
		t.Fatal("unknown default device accepted", object(t, o))
	}
	if o := call(t, client, "session.set_defaults", map[string]any{"cwd": nil}); o.IsError {
		t.Fatal(object(t, o))
	}
	if o := call(t, client, "fs.write", map[string]any{"path": "after-clear.txt", "content": "3"}); o.IsError {
		t.Fatal(object(t, o))
	}
	if _, err := os.Stat(filepath.Join(root, "after-clear.txt")); err != nil {
		t.Fatal("cwd default not cleared while the device default stayed", err)
	}
	// Offline target: not_found naming the device.
	o := call(t, client, "ghost/fs.read", map[string]any{"path": "x"})
	if !o.IsError || object(t, o)["error"] != "not_found" || object(t, o)["name"] != "ghost" {
		t.Fatal(object(t, o))
	}
}

// conformance: CTR-GW-02
func Test_CTRGW02_DevicesListScopeAndSearchArguments(t *testing.T) {
	g, _ := fixture(t, true)
	serve := func(scopes []string) *mcp.ClientSession {
		server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{ClientID: "c", Scopes: scopes}, nil }))
		t.Cleanup(server.Close)
		return connect(t, server.URL+"/mcp")
	}
	blind := serve([]string{"fs:read:*"})
	for _, name := range []string{"devices.list", "tools.search"} {
		o, err := blind.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"device": "test-node", "pattern": "fs"}})
		if err != nil {
			// The tool is not even offered to a client without devices:read.
			continue
		}
		if !o.IsError || object(t, o)["error"] != "unauthorized" {
			t.Fatalf("%s without devices:read: %v", name, object(t, o))
		}
	}
	client := serve([]string{"devices:read", "fs:read:*"})
	list := object(t, call(t, client, "devices.list", map[string]any{}))["devices"].([]any)
	row := list[0].(map[string]any)
	if row["id"] != "test-node" || row["online"] != true {
		t.Fatalf("devices.list row: %v", row)
	}
	for _, key := range []string{"os", "tags", "load", "backends", "stale"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("devices.list row lacks %q: %v", key, row)
		}
	}
	for _, args := range []map[string]any{{"device": "test-node", "pattern": ""}, {"device": "test-node"}, {"pattern": "fs"}} {
		if o := call(t, client, "tools.search", args); !o.IsError || object(t, o)["error"] != "invalid_args" {
			t.Fatalf("tools.search %v: %v", args, object(t, o))
		}
	}
	if o := call(t, client, "tools.search", map[string]any{"device": "nope", "pattern": "fs"}); !o.IsError || object(t, o)["error"] != "not_found" {
		t.Fatal(object(t, o))
	}
	found := object(t, call(t, client, "tools.search", map[string]any{"device": "test-node", "pattern": "FS.READ"}))
	tools := found["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "test-node/fs.read" || found["truncated"] != false {
		t.Fatalf("case-insensitive prefixed search: %v", found)
	}
}

// conformance: CON-ARCH-08
func Test_CONARCH08_DevicesCallEachOtherWithoutTheIngress(t *testing.T) {
	grants := map[string]config.Grant{"tag:peer": grantAll()}
	alpha, _ := conformanceAgent(t, "alpha", grants, nil)
	beta, betaRoot := conformanceAgent(t, "beta", grants, nil)
	serve := func(a *agent.Agent) *httptest.Server {
		s := httptest.NewServer(a.HTTP(agent.Local))
		t.Cleanup(s.Close)
		return s
	}
	alphaServer, betaServer := serve(alpha), serve(beta)

	// An ingress exists, then goes away.
	ingress := New(Options{NodeName: "ingress", Ingress: true, LocalPeer: mesh.Peer{Name: "alpha", ID: "alpha", Address: alphaServer.URL + "/mcp", Online: true}, Dial: RemoteDial(alphaServer.Client())})
	ingressServer := httptest.NewServer(ingress.HTTP(func(*http.Request) (Principal, error) { return Principal{ClientID: "cloud", Scopes: allScopes}, nil }))
	cloud := connect(t, ingressServer.URL+"/mcp")
	if o := call(t, cloud, "alpha/sys.info", map[string]any{}); o.IsError {
		t.Fatal(object(t, o))
	}
	_ = cloud.Close()
	ingressServer.Close()
	ingress.Close()

	logger, err := audit.New(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	// alpha's own Gateway reaches beta directly.
	gateway := New(Options{NodeName: "alpha", LocalPeer: mesh.Peer{Name: "alpha", ID: "alpha", Address: alphaServer.URL + "/mcp", Online: true}, Dial: RemoteDial(http.DefaultClient), Audit: logger})
	t.Cleanup(gateway.Close)
	gateway.Update([]mesh.Peer{{Name: "beta", ID: "beta", Address: betaServer.URL + "/mcp", Online: true}})
	server := httptest.NewServer(gateway.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	if o := call(t, client, "beta/fs.write", map[string]any{"path": "from-alpha", "content": "hello"}); o.IsError {
		t.Fatal(object(t, o))
	}
	if body, err := os.ReadFile(filepath.Join(betaRoot, "from-alpha")); err != nil || string(body) != "hello" {
		t.Fatal("beta did not receive the call", err)
	}
	if o := call(t, client, "beta/fs.read", map[string]any{"path": "from-alpha"}); o.IsError {
		t.Fatal(object(t, o))
	}
}

func oauthLogin(t *testing.T, base, client, scope string) string {
	t.Helper()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path, body, cookie string) (int, http.Header, string) {
		r, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		resp, err := noRedirect.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}
	verifier := strings.Repeat("v", 43)
	hash := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "state": {"s"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}}
	if scope != "" {
		q.Set("scope", scope)
	}
	path := "/oauth/authorize?" + q.Encode()
	status, headers, body := do("GET", path, "", "")
	if status != 200 {
		t.Fatal(status, body)
	}
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
	status, headers, body = do("POST", path, url.Values{"password": {"conformance-login"}, "csrf": {csrf[1]}}.Encode(), strings.Split(headers.Get("Set-Cookie"), ";")[0])
	if status != 302 {
		t.Fatal(status, body)
	}
	location, _ := url.Parse(headers.Get("Location"))
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "redirect_uri": {"http://127.0.0.1:1234/callback"}, "code": {location.Query().Get("code")}, "code_verifier": {verifier}}
	status, _, body = do("POST", "/oauth/token", form.Encode(), "")
	if status != 200 {
		t.Fatal(status, body)
	}
	var token map[string]any
	_ = json.Unmarshal([]byte(body), &token)
	return token["access_token"].(string)
}

// conformance: CON-ARCH-01 CON-SEC-02
func Test_CONARCH01_IngressServesOnlyMCPAndOAuthAndNeverWithoutAuth(t *testing.T) {
	g, _ := fixture(t, true)
	authorization, err := oauth.New(oauth.Options{Issuer: "https://mesh.example", Password: "conformance-login", StateDir: t.TempDir(), Clients: map[string][]string{"full": {"devices:read", "fs:*"}, "empty": {}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(authorization.Handler(g.HTTP(func(r *http.Request) (Principal, error) {
		token, ok := oauth.FromContext(r.Context())
		if !ok {
			return Principal{}, fmt.Errorf("unauthenticated")
		}
		return Principal{ClientID: token.ClientID, Scopes: token.Scopes}, nil
	})))
	t.Cleanup(server.Close)
	full := oauthLogin(t, server.URL, "full", "")
	empty := oauthLogin(t, server.URL, "empty", "")

	status := func(method, path, bearer string) int {
		r, _ := http.NewRequest(method, server.URL+path, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// No private RPC, WebSocket, tailnet control or xmesh API surface on the
	// public listener, even for a client holding a valid token.
	for _, path := range []string{"/", "/rpc", "/jsonrpc", "/ws", "/sse", "/events", "/api/xmesh/v1/nodes", "/api/xmesh/v1/control", "/v1/tools/call", "/control/drain", "/mcp/x", "/admin", "/oauth/users", "/oauth/login"} {
		for _, method := range []string{"GET", "POST"} {
			if code := status(method, path, full); code != 404 && code != 405 {
				t.Errorf("%s %s answered %d on the public listener", method, path, code)
			}
		}
	}
	// MCP has no unauthenticated mode and no alternative credential header.
	if status("POST", "/mcp", "") != 401 || status("POST", "/mcp", "not-a-token") != 401 {
		t.Fatal("MCP endpoint reachable without a valid access token")
	}
	r, _ := http.NewRequest("POST", server.URL+"/mcp", strings.NewReader("{}"))
	r.Header.Set("X-Api-Key", "x")
	r.Header.Set("X-Xmesh-Oauth-Client", "full")
	if resp, err := http.DefaultClient.Do(r); err != nil || resp.StatusCode != 401 {
		t.Fatalf("custom authentication headers accepted: %v %v", resp, err)
	}

	connectWith := func(token string) *mcp.ClientSession {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c := mcp.NewClient(&mcp.Implementation{Name: "arch01", Version: "1"}, nil)
		s, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	// The scope list is the whole permission: the client with no scopes can finish
	// OAuth and open a connection, but sees and can use nothing.
	nobody := connectWith(empty)
	list, err := nobody.ListTools(context.Background(), nil)
	if err != nil || len(list.Tools) != 0 {
		t.Fatalf("empty-scope client sees tools: %v %v", list, err)
	}
	for _, name := range []string{"test-node/fs.read", "test-node/sys.info", "devices.list", "test-node/exec.run"} {
		out, err := nobody.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"path": "x", "cmd": "true"}})
		if err == nil && !out.IsError {
			t.Fatalf("empty-scope client called %s", name)
		}
		if err == nil && object(t, out)["error"] != "unauthorized" {
			t.Fatalf("%s: %v", name, object(t, out))
		}
	}
	somebody := connectWith(full)
	if !hasTool(t, somebody, "test-node/fs.read") || hasTool(t, somebody, "test-node/fs.write") == false {
		t.Fatal("fs:* did not expand to read and write")
	}
	if hasTool(t, somebody, "test-node/exec.run") {
		t.Fatal("exec offered without exec scope")
	}
}
