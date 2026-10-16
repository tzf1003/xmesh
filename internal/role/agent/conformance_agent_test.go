package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// conformanceConfig writes a config with the helper backends "ida" (exclusive)
// and "shared" (shared) and returns its path and root directory.
func conformanceConfig(t *testing.T, authz map[string]any) (string, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := []string{bin, "-test.run=TestAgentBackendProcess", "--", "--xmesh-agent-backend-helper"}
	if authz == nil {
		authz = map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}
	}
	doc := map[string]any{
		"node": map[string]any{"name": "test-node", "roles": []string{"agent"}},
		"mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}},
		"agent": map[string]any{
			"default_cwd": root,
			"cwd_policy":  map[string]any{"allow": []string{root}},
			"authz":       authz,
			"pty":         map[string]any{"default_shell": "auto"},
			"backends": map[string]any{
				"ida":    map[string]any{"transport": "stdio", "command": command, "policy": "exclusive"},
				"shared": map[string]any{"transport": "stdio", "command": command, "policy": "shared"},
			},
		},
	}
	body, _ := json.Marshal(doc)
	path := filepath.Join(root, "config.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path, root
}

func startAgent(t *testing.T, path string, authenticate Authenticate) (*Agent, *httptest.Server) {
	t.Helper()
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if authenticate == nil {
		authenticate = Local
	}
	server := httptest.NewServer(a.HTTP(authenticate))
	t.Cleanup(server.Close)
	return a, server
}

func errorCode(t *testing.T, out *mcp.CallToolResult) (string, map[string]any) {
	t.Helper()
	if !out.IsError {
		return "", object(t, out)
	}
	v := object(t, out)
	code, _ := v["error"].(string)
	return code, v
}

// conformance: CTR-BE-02 RUL-06
func Test_CTRBE02_RUL06_BuiltinLeaseToolsOverMCP(t *testing.T) {
	path, _ := conformanceConfig(t, nil)
	a, server := startAgent(t, path, nil)
	one, two := connect(t, server.URL+"/mcp"), connect(t, server.URL+"/mcp")
	// Only exclusive backends have leases.
	for _, name := range []string{"backend.acquire", "backend.renew", "backend.release"} {
		out := tool(t, one, name, map[string]any{"name": "shared", "ttl": "1m", "_lease_token": 1})
		if code, _ := errorCode(t, out); code != "invalid_args" {
			t.Fatalf("%s on a shared backend: %v", name, object(t, out))
		}
	}
	if code, _ := errorCode(t, tool(t, one, "backend.acquire", map[string]any{"name": "ida"})); code != "invalid_args" {
		t.Fatal("acquire without ttl")
	}
	granted := tool(t, one, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})
	lease := object(t, granted)
	token := lease["token"].(float64)
	if granted.IsError || token < 1 || lease["resource"] != "backend:ida" || lease["owner"] == "" {
		t.Fatal(lease)
	}
	if code, body := errorCode(t, tool(t, two, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})); code != "resource_busy" || body["reason"] != "held" || body["held_by"] != lease["owner"] {
		t.Fatal("second acquire", body)
	}
	// RUL-06: the parameter is _lease_token for renew and release; a plain
	// "token" parameter is not a credential.
	for _, name := range []string{"backend.renew", "backend.release"} {
		out := tool(t, one, name, map[string]any{"name": "ida", "ttl": "1m", "token": token})
		if code, _ := errorCode(t, out); code != "invalid_args" {
			t.Fatalf("%s accepted a parameter named token: %v", name, object(t, out))
		}
	}
	if a.Leases.Owner("backend:ida") != lease["owner"] {
		t.Fatal("rejected calls disturbed the lease")
	}
	for _, spelling := range []any{token, "" + jsonNumber(token)} {
		out := tool(t, one, "backend.renew", map[string]any{"name": "ida", "ttl": "2m", "_lease_token": spelling})
		if out.IsError || object(t, out)["token"] != token {
			t.Fatalf("renew with %v: %v", spelling, object(t, out))
		}
	}
	if code, body := errorCode(t, tool(t, two, "backend.renew", map[string]any{"name": "ida", "ttl": "1m", "_lease_token": token})); code != "resource_busy" || body["reason"] != "token_mismatch" {
		t.Fatal("renew by non-owner", body)
	}
	for attempt := 0; attempt < 2; attempt++ {
		out := tool(t, one, "backend.release", map[string]any{"name": "ida", "_lease_token": jsonNumber(token)})
		if out.IsError || object(t, out)["released"] != true {
			t.Fatalf("release attempt %d: %v", attempt, object(t, out))
		}
	}
	again := object(t, tool(t, two, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"}))
	if again["token"].(float64) <= token {
		t.Fatal("token not advanced after release", again)
	}
}

func jsonNumber(f float64) string { b, _ := json.Marshal(int64(f)); return string(b) }

// conformance: CON-CONC-02
func Test_CONCONC02_OneTransportIdentityEverywhere(t *testing.T) {
	path, _ := conformanceConfig(t, nil)
	a, server := startAgent(t, path, nil)
	dir := t.TempDir()
	logger, err := audit.New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	a.SetAudit(logger)
	one, two := connect(t, server.URL+"/mcp"), connect(t, server.URL+"/mcp")
	forged := map[string]any{"owner": "forged/owner", "caller_id": "forged/caller", "node_id": "forged"}
	withForged := func(args map[string]any) map[string]any {
		for k, v := range forged {
			args[k] = v
		}
		return args
	}
	opened := object(t, tool(t, one, "pty.open", withForged(map[string]any{"cols": 80, "rows": 24})))
	owner := opened["owner"].(string)
	if !identity.Valid(owner) || strings.Contains(owner, "forged") || !strings.HasPrefix(owner, "local/") {
		t.Fatalf("pty owner %q is not the transport caller_id", owner)
	}
	lease := object(t, tool(t, one, "backend.acquire", withForged(map[string]any{"name": "ida", "ttl": "1m"})))
	if lease["owner"] != owner || a.Leases.Owner("backend:ida") != owner {
		t.Fatalf("lease owner %v differs from pty owner %v", lease["owner"], owner)
	}
	other := object(t, tool(t, two, "pty.open", map[string]any{"cols": 80, "rows": 24}))["owner"]
	if other == owner {
		t.Fatal("two MCP sessions share a caller_id")
	}
	// The per-caller PTY count and the authorization both key on that identity.
	if code, _ := errorCode(t, tool(t, two, "pty.write", map[string]any{"session_id": opened["session_id"], "data": "x", "caller_id": owner})); code != "not_owner" {
		t.Fatal("a caller_id argument overrode the transport identity")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	seen := map[string]bool{}
	for _, file := range files {
		body, _ := os.ReadFile(file)
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) != nil {
				continue
			}
			if row["caller"] == owner {
				seen[row["tool"].(string)] = true
			}
			if strings.Contains(line, "forged") && row["caller"] != owner {
				t.Fatalf("audit caller is not the transport identity: %s", line)
			}
		}
	}
	if !seen["pty.open"] || !seen["backend.acquire"] {
		t.Fatalf("audit does not carry the same caller_id: %v", seen)
	}
}

// conformance: CON-CONC-08
func Test_CONCONC08_AgentRestartDropsLeases(t *testing.T) {
	path, root := conformanceConfig(t, nil)
	// First process: get the token to 2 so that it cannot coincide with a fresh 1.
	a1, server1 := startAgent(t, path, nil)
	client := connect(t, server1.URL+"/mcp")
	first := object(t, tool(t, client, "backend.acquire", map[string]any{"name": "ida", "ttl": "10m"}))
	if out := tool(t, client, "backend.release", map[string]any{"name": "ida", "_lease_token": jsonNumber(first["token"].(float64))}); out.IsError {
		t.Fatal(object(t, out))
	}
	held := object(t, tool(t, client, "backend.acquire", map[string]any{"name": "ida", "ttl": "10m"}))
	old := held["token"].(float64)
	if old <= first["token"].(float64) {
		t.Fatal("token did not advance", held)
	}
	_ = client.Close()
	server1.Close()
	a1.Close()
	// Nothing about the lease may be on disk.
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, _ := os.ReadFile(p)
		if strings.Contains(string(body), "backend:ida") || strings.Contains(strings.ToLower(filepath.Base(p)), "lease") {
			t.Fatalf("lease state persisted in %s", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Second process over the same configuration.
	_, server2 := startAgent(t, path, nil)
	next := connect(t, server2.URL+"/mcp")
	if out := tool(t, next, "ida.echo", map[string]any{"_lease_token": old}); !out.IsError || object(t, out)["error"] != "resource_busy" || object(t, out)["reason"] != "token_stale" {
		t.Fatalf("pre-restart token honoured after restart: %v", object(t, out))
	}
	fresh := tool(t, next, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})
	if fresh.IsError || object(t, fresh)["token"] != float64(1) {
		t.Fatalf("first acquire after restart: %v", object(t, fresh))
	}
	if out := tool(t, next, "ida.echo", map[string]any{"_lease_token": old}); !out.IsError || object(t, out)["error"] != "resource_busy" {
		t.Fatalf("pre-restart token honoured after new acquire: %v", object(t, out))
	}
}

// conformance: CON-CONC-11
func Test_CONCONC11_SessionEndOrphansPTYButKeepsLeaseUntilTTL(t *testing.T) {
	path, _ := conformanceConfig(t, nil)
	a, server := startAgent(t, path, nil)
	one, two := connect(t, server.URL+"/mcp"), connect(t, server.URL+"/mcp")
	granted := time.Now()
	lease := object(t, tool(t, one, "backend.acquire", map[string]any{"name": "ida", "ttl": "2s"}))
	opened := object(t, tool(t, one, "pty.open", map[string]any{"cols": 80, "rows": 24}))
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := a.PTY.List()
		if len(rows) == 1 && rows[0]["state"] == "orphaned" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session end did not orphan its PTY", rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, active, _ := a.PTY.Counts(); active != 0 {
		t.Fatal("orphaned terminal still counts as active", active)
	}
	_ = opened
	// No immediate release: the exclusive lease survives the session end.
	if a.Leases.Owner("backend:ida") != lease["owner"] {
		t.Fatal("lease released the moment the session ended")
	}
	if code, body := errorCode(t, tool(t, two, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})); code != "resource_busy" || body["reason"] != "held" {
		t.Fatal("lease of an ended session was handed out early", body)
	}
	// It is freed by TTL, not by a connection-drop shortcut.
	for {
		out := tool(t, two, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})
		if !out.IsError {
			if object(t, out)["token"].(float64) <= lease["token"].(float64) {
				t.Fatal("token reused")
			}
			if time.Since(granted) < 1900*time.Millisecond {
				t.Fatalf("lease freed after %v, before its 2s ttl", time.Since(granted))
			}
			return
		}
		if time.Since(granted) > 10*time.Second {
			t.Fatal("lease never expired")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type tagTransport struct{ tag, anon string }

func (r tagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := req.Clone(req.Context())
	c.Header.Set("X-Test-Tag", r.tag)
	if r.anon != "" {
		c.Header.Set("X-Test-Anon", r.anon)
	}
	return http.DefaultTransport.RoundTrip(c)
}

func taggedAuth(r *http.Request) (Principal, error) {
	if r.Header.Get("X-Test-Anon") != "" {
		return Principal{}, toolerr.New("unauthenticated", "WhoIs 无法识别对端")
	}
	p := Principal{Node: "stable-peer", Tags: []string{}}
	if tag := r.Header.Get("X-Test-Tag"); tag != "" {
		p.Tags = strings.Split(tag, ",")
	}
	if client := r.Header.Get("X-Xmesh-Oauth-Client"); client != "" {
		p.OAuthClient = client
	}
	return p, nil
}

func connectTagged(t *testing.T, url, tag string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "tagged", Version: "1"}, nil)
	s, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: tagTransport{tag: tag}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// conformance: CON-SEC-03 CON-SEC-01 CTR-AUTHZ-01
func Test_CONSEC03_UnidentifiedPeerAndUnlistedTagAreDenied(t *testing.T) {
	authz := map[string]any{
		"tag:peer":    map[string]any{"tools": []string{"*"}, "backends": []string{"*"}},
		"tag:sysonly": map[string]any{"tools": []string{"sys.*"}, "backends": []string{}},
		"tag:empty":   map[string]any{"tools": []string{}, "backends": []string{"*"}},
		"tag:fsonly":  map[string]any{"tools": []string{"fs.*"}, "backends": []string{"ida"}},
	}
	path, root := conformanceConfig(t, authz)
	_, server := startAgent(t, path, taggedAuth)

	// No identity (WhoIs failed): the request is refused with unauthenticated
	// before any tool can run.
	request, _ := http.NewRequest("POST", server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fs.write","arguments":{"path":"x","content":"x"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("X-Test-Anon", "1")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 401 || !strings.Contains(string(body), `"unauthenticated"`) {
		t.Fatalf("unidentified peer: %d %s", resp.StatusCode, body)
	}
	if _, err = os.Stat(filepath.Join(root, "x")); !os.IsNotExist(err) {
		t.Fatal("unidentified peer caused a side effect")
	}

	// A tag with no authz entry is denied by default, whatever the arguments claim.
	for _, tag := range []string{"tag:unlisted", "tag:device,tag:cloud", ""} {
		client := connectTagged(t, server.URL+"/mcp", tag)
		out := tool(t, client, "fs.write", map[string]any{"path": "denied", "content": "x", "tag": "tag:peer", "tags": []string{"tag:peer"}, "node_id": "stable-peer"})
		if code, _ := errorCode(t, out); code != "unauthorized" {
			t.Fatalf("tag %q was allowed: %v", tag, object(t, out))
		}
		if code, _ := errorCode(t, tool(t, client, "sys.info", map[string]any{})); code != "unauthorized" {
			t.Fatalf("tag %q may call sys.info", tag)
		}
		list, err := client.ListTools(context.Background(), nil)
		if err != nil || len(list.Tools) != 0 {
			t.Fatalf("tag %q sees tools: %v %v", tag, list, err)
		}
	}
	if _, err = os.Stat(filepath.Join(root, "denied")); !os.IsNotExist(err) {
		t.Fatal("denied tag created a file")
	}

	// Allow-lists: tools=[] allows nothing, '*' means all only when written,
	// 'sys.*' does not leak into other tools, backends are a separate list.
	empty := connectTagged(t, server.URL+"/mcp", "tag:empty")
	if code, _ := errorCode(t, tool(t, empty, "sys.info", map[string]any{})); code != "unauthorized" {
		t.Fatal("tools: [] still allowed a tool")
	}
	sys := connectTagged(t, server.URL+"/mcp", "tag:sysonly")
	if out := tool(t, sys, "sys.info", map[string]any{}); out.IsError {
		t.Fatal(object(t, out))
	}
	for name, args := range map[string]map[string]any{
		"fs.read":         {"path": "config.json"},
		"exec.run":        {"cmd": "true"},
		"pty.list":        {},
		"backend.acquire": {"name": "ida", "ttl": "1m"},
	} {
		if code, _ := errorCode(t, tool(t, sys, name, args)); code != "unauthorized" {
			t.Fatalf("tag:sysonly may call %s", name)
		}
	}
	fsOnly := connectTagged(t, server.URL+"/mcp", "tag:fsonly")
	if out := tool(t, fsOnly, "fs.read", map[string]any{"path": "config.json"}); out.IsError {
		t.Fatal(object(t, out))
	}
	// backend.* is a tool pattern too: fs.* does not cover it even though the
	// backend itself is listed.
	if code, _ := errorCode(t, tool(t, fsOnly, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})); code != "unauthorized" {
		t.Fatal("backend list alone authorised a built-in lease tool")
	}
	peer := connectTagged(t, server.URL+"/mcp", "tag:peer")
	if out := tool(t, peer, "fs.write", map[string]any{"path": "ok", "content": "x"}); out.IsError {
		t.Fatal(object(t, out))
	}
}

// conformance: CTR-AUTHZ-01
func Test_CTRAUTHZ01_AllowListPatterns(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Authz = map[string]config.Grant{
		"tag:a": {Tools: []string{"fs.*", "ida.*"}, Backends: []string{"ida"}},
		"tag:b": {Tools: []string{"*"}, Backends: []string{"*"}},
		"tag:c": {Tools: []string{}, Backends: []string{}},
	}
	caller := func(tags ...string) identity.Caller { return identity.Caller{ID: "n/s", Tags: tags} }
	for _, tc := range []struct {
		tags    []string
		tool    string
		backend string
		want    bool
	}{
		{[]string{"tag:a"}, "fs.read", "", true},
		{[]string{"tag:a"}, "fs.delete", "", true},
		{[]string{"tag:a"}, "fsx.read", "", false},
		{[]string{"tag:a"}, "exec.run", "", false},
		{[]string{"tag:a"}, "", "ida", true},
		{[]string{"tag:a"}, "", "frida", false},
		{[]string{"tag:b"}, "anything.at.all", "", true},
		{[]string{"tag:b"}, "", "whatever", true},
		{[]string{"tag:c"}, "sys.info", "", false},
		{[]string{"tag:c"}, "", "ida", false},
		{[]string{"tag:unknown"}, "sys.info", "", false},
		{nil, "sys.info", "", false},
		{[]string{"tag:unknown", "tag:a"}, "fs.read", "", true},
	} {
		if got := Allowed(cfg, caller(tc.tags...), tc.tool, tc.backend); got != tc.want {
			t.Errorf("tags=%v tool=%q backend=%q: got %v want %v", tc.tags, tc.tool, tc.backend, got, tc.want)
		}
	}
}

// conformance: CON-SEC-01
func Test_CONSEC01_DevTagExceptionIsOnlyForTheIPv4Loopback(t *testing.T) {
	for remote, ok := range map[string]bool{
		"127.0.0.1:5000":   true,
		"[::1]:5000":       false,
		"192.168.1.5:5000": false,
		"10.0.0.2:5000":    false,
		"100.64.0.9:5000":  false,
		"127.0.0.2:5000":   false,
		"no-port":          false,
		"":                 false,
	} {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.RemoteAddr = remote
		principal, err := Local(r)
		if ok {
			if err != nil || principal.Node != "local" || len(principal.Tags) != 1 || principal.Tags[0] != "tag:peer" {
				t.Fatalf("%q: %+v %v", remote, principal, err)
			}
			continue
		}
		if e, isTool := err.(*toolerr.Error); !isTool || e.Code != "unauthenticated" {
			t.Fatalf("%q must be unauthenticated, got %+v %v", remote, principal, err)
		}
	}
}

type ingressTransport struct{ tag, client string }

func (r ingressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := req.Clone(req.Context())
	c.Header.Set("X-Test-Tag", r.tag)
	if r.client != "" {
		c.Header.Set("X-Xmesh-Oauth-Client", r.client)
	}
	return http.DefaultTransport.RoundTrip(c)
}

func ingressAuth(r *http.Request) (Principal, error) {
	// The node identity is the same for every connection, as it is for all
	// sessions arriving through one ingress node.
	return identity.FromPeer("stable-node", strings.Split(r.Header.Get("X-Test-Tag"), ","), r.Header.Get("X-Xmesh-Oauth-Client"))
}

func connectAs(t *testing.T, url, tag, client string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "as-" + client, Version: "1"}, nil)
	s, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: ingressTransport{tag, client}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err == nil {
		t.Cleanup(func() { s.Close() })
	}
	return s, err
}

// conformance: RUL-02 RUL-13
func Test_RUL02_RUL13_IngressSessionsAndOrphanTakeoverByOAuthClient(t *testing.T) {
	path, _ := conformanceConfig(t, map[string]any{
		"tag:ingress": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}},
		"tag:peer":    map[string]any{"tools": []string{"*"}, "backends": []string{"*"}},
	})
	a, server := startAgent(t, path, ingressAuth)
	url := server.URL + "/mcp"

	// RUL-02: an ingress tag without the oauth client header is refused.
	if _, err := connectAs(t, url, "tag:ingress", ""); err == nil {
		t.Fatal("ingress session without X-Xmesh-Oauth-Client was accepted")
	}
	claudeA, err := connectAs(t, url, "tag:ingress", "claude")
	if err != nil {
		t.Fatal(err)
	}
	claudeC, _ := connectAs(t, url, "tag:ingress", "claude")
	codex, _ := connectAs(t, url, "tag:ingress", "codex")
	peer, _ := connectAs(t, url, "tag:peer", "")
	for name, s := range map[string]*mcp.ClientSession{"claudeC": claudeC, "codex": codex, "peer": peer} {
		if s == nil {
			t.Fatalf("%s could not connect", name)
		}
	}
	owner := func(s *mcp.ClientSession) string {
		t.Helper()
		o := object(t, tool(t, s, "pty.open", map[string]any{"cols": 80, "rows": 24}))
		if o["owner"] == nil {
			t.Fatalf("pty.open result has no owner: %v", o)
		}
		return o["owner"].(string)
	}
	// Each downstream MCP session has its own caller_id: node id + "/" + session id.
	first := object(t, tool(t, claudeA, "pty.open", map[string]any{"cols": 80, "rows": 24}))
	id, firstOwner := first["session_id"].(string), first["owner"].(string)
	nodeID, sessionPart, ok := strings.Cut(firstOwner, "/")
	if !ok || nodeID != "stable-node" || sessionPart == "" || sessionPart == "init" || nodeID == "local" {
		t.Fatalf("caller_id %q is not node_id/mcp_session_id", firstOwner)
	}
	for _, s := range []*mcp.ClientSession{claudeC, codex, peer} {
		// Pre-register the sessions with the Agent by opening nothing: ask for their identity via a harmless call.
		if o := tool(t, s, "sys.info", map[string]any{}); o.IsError {
			t.Fatal(object(t, o))
		}
	}

	// The creating session goes away: the terminal is orphaned and keeps its oauth client.
	if err = claudeA.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(a.PTY.List()) != 1 || a.PTY.List()[0]["state"] != "orphaned" {
		if time.Now().After(deadline) {
			t.Fatal("terminal was not orphaned", a.PTY.List())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Same node, different OAuth client: refused. A session without an OAuth client
	// (tag:peer) is refused too, because the terminal belongs to a client.
	for name, s := range map[string]*mcp.ClientSession{"codex": codex, "peer": peer} {
		out := tool(t, s, "pty.transfer", map[string]any{"session_id": id, "to_caller": "stable-node/" + sessionIDOf(t, s)})
		if !out.IsError {
			t.Fatalf("%s took over a terminal that belongs to another OAuth client", name)
		}
	}
	if rows := a.PTY.List(); rows[0]["state"] != "orphaned" {
		t.Fatalf("a refused takeover changed the terminal: %v", rows)
	}
	// The same OAuth client on a new session succeeds.
	takeover := tool(t, claudeC, "pty.transfer", map[string]any{"session_id": id, "to_caller": "stable-node/" + sessionIDOf(t, claudeC)})
	if takeover.IsError {
		t.Fatalf("same OAuth client could not resume its terminal: %v", object(t, takeover))
	}
	if rows := a.PTY.List(); rows[0]["state"] != "active" || !strings.HasPrefix(rows[0]["owner"].(string), "stable-node/") || rows[0]["owner"] == firstOwner {
		t.Fatalf("terminal after takeover: %v", rows)
	}

	// A terminal opened without an OAuth client (local or tailnet) is taken over by
	// node id only: another session on the same node can pick it up.
	owner(peer)
	peer2, err := connectAs(t, url, "tag:peer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = peer.Close(); err != nil {
		t.Fatal(err)
	}
	var peerTerminal string
	deadline = time.Now().Add(5 * time.Second)
	for peerTerminal == "" {
		for _, row := range a.PTY.List() {
			if row["state"] == "orphaned" {
				peerTerminal = row["id"].(string)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("peer terminal was not orphaned", a.PTY.List())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if out := tool(t, peer2, "pty.transfer", map[string]any{"session_id": peerTerminal, "to_caller": "stable-node/" + sessionIDOf(t, peer2)}); out.IsError {
		t.Fatalf("session without OAuth client could not resume a node-only terminal: %v", object(t, out))
	}
}

// sessionIDOf returns the MCP session id of a client session (the part of its
// caller_id after the node id).
func sessionIDOf(t *testing.T, s *mcp.ClientSession) string {
	t.Helper()
	id := s.ID()
	if id == "" {
		t.Fatal("client session has no Mcp-Session-Id")
	}
	return id
}
