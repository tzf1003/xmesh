package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/api/control/v2/schemacheck"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
)

type node struct {
	t        *testing.T
	stateDir string
	gateway  string
	agent    string
	control  *control.Client
	stop     func()
}

func startNode(t *testing.T) *node {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	n := &node{t: t, stateDir: filepath.Join(root, "state"), gateway: address(t), agent: address(t)}
	doc := map[string]any{"node": map[string]any{"name": "app-ctl-node", "roles": []string{"agent", "gateway"}}, "mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}}, "gateway": map[string]any{"listen": n.gateway}, "agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "authz": map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}}}
	b, _ := json.Marshal(doc)
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, store, Options{Local: true, Listen: n.agent, StateDir: n.stateDir}) }()
	var once sync.Once
	n.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Error("runtime shutdown hung")
			}
		})
	}
	t.Cleanup(n.stop)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if ep, err := control.ReadEndpoint(n.stateDir); err == nil {
			n.control = control.NewClient(ep)
			if r, err := n.control.Do(ctx, "GET", "/v2/status", nil, nil); err == nil && r.Status == 200 {
				return n
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("control channel did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// get fetches path, checks the body against the contract definition def, and decodes it into out.
func (n *node) get(path, def string, out any) {
	n.t.Helper()
	r, err := n.control.Do(context.Background(), "GET", path, nil, nil)
	if err != nil || r.Status != 200 {
		n.t.Fatalf("GET %s: %v %v", path, r, err)
	}
	if err = schemacheck.Validate(def, r.Body); err != nil {
		n.t.Fatalf("GET %s violates the contract: %v\n%s", path, err, r.Body)
	}
	if err = json.Unmarshal(r.Body, out); err != nil {
		n.t.Fatal(err)
	}
}
func (n *node) status() v2.StatusResponse {
	n.t.Helper()
	var st v2.StatusResponse
	n.get("/v2/status", "StatusResponse", &st)
	return st
}
func (n *node) post(path string, body any) {
	n.t.Helper()
	r, err := n.control.Do(context.Background(), "POST", path, body, nil)
	if err != nil || r.Status != 200 {
		n.t.Fatalf("POST %s: %v %v", path, r, err)
	}
	if err = schemacheck.Validate("DrainResponse", r.Body); err != nil {
		n.t.Fatalf("POST %s violates the contract: %v\n%s", path, err, r.Body)
	}
}
func session(t *testing.T, stateDir, endpoint string) *mcp.ClientSession {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		attempt, stop := context.WithTimeout(context.Background(), time.Second)
		s, err := client.Connect(attempt, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: gatewayHTTPClient(stateDir)}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
		stop()
		if err == nil {
			t.Cleanup(func() { s.Close() })
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("listener not ready", endpoint)
	return nil
}
func call(t *testing.T, s *mcp.ClientSession, name string, args any) (map[string]any, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	raw, _ := json.Marshal(o.StructuredContent)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	return v, o.IsError
}

// Rule CON-ARCH-09: the channel is not on TCP, not behind the Gateway, and not an MCP tool
func Test_CONARCH09_ControlChannelIsLocalOnly(t *testing.T) {
	n := startNode(t)
	ep, err := control.ReadEndpoint(n.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Transport != v2.TransportUnix && ep.Transport != v2.TransportPipe || strings.Contains(ep.Address(), "127.0.0.1") || strings.Contains(ep.Address(), "http") {
		t.Fatalf("endpoint is not a socket or pipe: %+v", ep)
	}
	// Neither the Agent's nor the Gateway's TCP listener serves the control API.
	for _, address := range []string{n.agent, n.gateway} {
		for _, path := range []string{"/v2/status", "/v2/config", "/v2/events", "/v2/drain", "/control/drain"} {
			res, err := http.Get("http://" + address + path)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode == 200 {
				t.Errorf("%s%s answered 200", address, path)
			}
		}
	}
	// No MCP tool, through the Gateway or directly, exposes it.
	for _, endpoint := range []string{"http://" + n.gateway + "/mcp", "http://" + n.agent + "/mcp"} {
		s := session(t, n.stateDir, endpoint)
		tools, err := s.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range tools.Tools {
			if strings.Contains(tool.Name, "control") || strings.Contains(tool.Name, "drain") && !strings.Contains(tool.Name, "backend") {
				t.Errorf("%s lists %s", endpoint, tool.Name)
			}
		}
	}
}

func Test_ControlStatusFromRunningNode(t *testing.T) {
	n := startNode(t)
	st := n.status()
	if st.Hostname != "app-ctl-node" || st.Control.KernelVersion != Version || st.Control.APIVersion != v2.APIVersion || st.Drain.Draining || st.Lifecycle != "running" {
		t.Fatalf("%+v", st)
	}
	if len(st.Roles) != 2 || st.Limits.PTYMaxSessions == 0 || st.Network.Mode != "local" || st.Paths.StateDir != n.stateDir || st.Paths.ConfigFile == "" {
		t.Fatalf("%+v", st)
	}
	if st.Gateway == nil || st.Gateway.Listen != n.gateway || st.Gateway.URL != "http://"+n.gateway+"/mcp" || st.Gateway.Auth != "pat" || len(st.Gateway.Addresses) != 1 || st.Ingress != nil {
		t.Fatalf("gateway info: %+v ingress %+v", st.Gateway, st.Ingress)
	}
	var peers v2.PeersResponse
	n.get("/v2/peers", "PeersResponse", &peers)
	if len(peers.Peers) != 1 || !peers.Peers[0].Self || peers.Peers[0].Name != "app-ctl-node" || peers.Peers[0].Connection != "local" {
		t.Fatalf("%+v", peers)
	}
	var cfg v2.ConfigResponse
	n.get("/v2/config", "ConfigResponse", &cfg)
	if !cfg.Valid || cfg.EffectiveETag != cfg.ETag {
		t.Fatalf("%+v", cfg)
	}
}

// The channel can drain the node and bring it back, which the MCP-side drain cannot.
func Test_ControlDrainResumeEndToEnd(t *testing.T) {
	n := startNode(t)
	direct := session(t, n.stateDir, "http://"+n.agent+"/mcp")
	gateway := session(t, n.stateDir, "http://"+n.gateway+"/mcp")
	if _, isErr := call(t, direct, "exec.run", map[string]any{"cmd": "go", "args": []string{"version"}}); isErr {
		t.Fatal("exec before drain failed")
	}
	n.post("/v2/drain", map[string]string{"mode": "keep"})
	if st := n.status(); !st.Drain.Draining || *st.Drain.Mode != v2.DrainKeep {
		t.Fatalf("%+v", st.Drain)
	}
	out, isErr := call(t, direct, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if !isErr || out["error"] != "policy_denied" || out["rule"] != "mesh.draining" {
		t.Fatalf("drained node accepted work: %v", out)
	}
	n.post("/v2/resume", nil)
	if st := n.status(); st.Drain.Draining {
		t.Fatalf("%+v", st.Drain)
	}
	opened, isErr := call(t, direct, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if isErr {
		t.Fatalf("resumed node refused work: %v", opened)
	}
	// the Gateway on the same node routes to it again
	list, isErr := call(t, gateway, "local/pty.list", map[string]any{})
	if isErr || len(list["sessions"].([]any)) != 1 {
		t.Fatalf("gateway after resume: %v", list)
	}
	var sessions v2.SessionsResponse
	n.get("/v2/sessions", "SessionsResponse", &sessions)
	if len(sessions.PTY) != 1 || sessions.PTY[0].ID != opened["session_id"] || sessions.PTY[0].State != "active" || sessions.PTY[0].CreatedAt.IsZero() {
		t.Fatalf("sessions: %+v", sessions)
	}
	call(t, direct, "pty.close", map[string]any{"session_id": opened["session_id"]})
}

func Test_ControlEventsAndAuditFromRunningNode(t *testing.T) {
	n := startNode(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := n.control.Stream(ctx, "/v2/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := make(chan string, 256)
	go func() {
		buf := make([]byte, 4096)
		var pending string
		for {
			k, err := res.Body.Read(buf)
			pending += string(buf[:k])
			for {
				line, rest, ok := strings.Cut(pending, "\n")
				if !ok {
					break
				}
				lines <- line
				pending = rest
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	direct := session(t, n.stateDir, "http://"+n.agent+"/mcp")
	call(t, direct, "sys.info", map[string]any{})
	if _, isErr := call(t, direct, "exec.run", map[string]any{"cmd": "go", "args": []string{"version"}}); isErr {
		t.Fatal("exec failed")
	}
	deadline := time.After(10 * time.Second)
	for found := false; !found; {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream ended")
			}
			if strings.HasPrefix(line, "event: call") {
				found = true
			}
			if strings.Contains(line, "go version") || strings.Contains(line, "args_digest") {
				t.Fatalf("stream carries content: %s", line)
			}
		case <-deadline:
			t.Fatal("no call event")
		}
	}
	var got v2.AuditResponse
	n.get("/v2/audit?tool=exec.run", "AuditResponse", &got)
	if !got.Enabled || len(got.Events) == 0 {
		t.Fatalf("audit view: %+v", got)
	}
}

func Test_ControlEndpointRemovedOnShutdown(t *testing.T) {
	n := startNode(t)
	n.stop()
	if _, err := control.ReadEndpoint(n.stateDir); err == nil {
		t.Fatal("control.json left behind")
	}
}

// A running exec call is visible in the sessions view with its executable's base name only.
func Test_ControlSessionsShowRunningExec(t *testing.T) {
	n := startNode(t)
	direct := session(t, n.stateDir, "http://"+n.agent+"/mcp")
	program := filepath.Join(n.stateDir, "..", "sleep.go")
	if err := os.WriteFile(program, []byte("package main\nimport \"time\"\nfunc main() { time.Sleep(4 * time.Second) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Warm the build cache so the run below spends its time in the program, not in a cold Windows compile.
	if out, err := osexec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "sleep"), program).CombinedOutput(); err != nil {
		t.Fatalf("warm-up build: %v\n%s", err, out)
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, err := direct.CallTool(ctx, &mcp.CallToolParams{Name: "exec.run", Arguments: map[string]any{"cmd": "go", "args": []string{"run", program, "--secret-flag=SECRET-ARG"}, "env": map[string]string{"SECRET_ENV": "SECRET-VALUE"}}})
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	executionID := ""
	for {
		var sessions v2.SessionsResponse
		n.get("/v2/sessions", "SessionsResponse", &sessions)
		if len(sessions.Exec) == 1 {
			run := sessions.Exec[0]
			raw, _ := json.Marshal(sessions)
			if run.Cmd != "go" || run.Mode != "shared" || strings.Contains(string(raw), "SECRET") {
				t.Fatalf("%+v", run)
			}
			for _, execution := range sessions.Executions {
				if execution.Tool == "exec.run" && execution.Layer == "agent" {
					executionID = execution.ID
					if execution.State != "running" || execution.Caller == "" || execution.StartedAt.IsZero() || execution.UpdatedAt.IsZero() {
						t.Fatalf("execution metadata: %+v", execution)
					}
				}
			}
			if executionID == "" || sessions.ExecutionLimits == nil {
				t.Fatal("all-tool execution missing", string(raw))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exec run never appeared")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatalf("exec.run: %v", err)
	}
	var after v2.SessionsResponse
	n.get("/v2/sessions", "SessionsResponse", &after)
	if len(after.Exec) != 0 {
		t.Fatalf("%+v", after.Exec)
	}
	for _, execution := range after.Executions {
		if execution.ID == executionID {
			t.Fatal("completed execution remained live", execution)
		}
	}
	var history v2.AuditResponse
	n.get("/v2/audit?tool=exec.run", "AuditResponse", &history)
	for _, raw := range history.Events {
		var event map[string]any
		_ = json.Unmarshal(raw, &event)
		if event["execution_id"] == executionID && event["layer"] == "agent" && event["started_at"] != nil {
			return
		}
	}
	t.Fatal("completed execution missing from audit history")
}

// Rule CON-ARCH-10: the kernel does not depend on the UI. An event subscriber that vanishes (a crashed
// UI) leaves the terminal, the node status and the channel itself untouched.
func Test_CONARCH10_UIExitDoesNotAffectKernel(t *testing.T) {
	n := startNode(t)
	direct := session(t, n.stateDir, "http://"+n.agent+"/mcp")
	opened, failed := call(t, direct, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if failed {
		t.Fatal(opened)
	}
	id, _ := opened["session_id"].(string)
	// a UI subscribes to the event stream and is then killed without a goodbye
	for i := 0; i < 3; i++ {
		ctx, kill := context.WithCancel(context.Background())
		res, err := n.control.Stream(ctx, "/v2/events", nil)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("subscribe: %v", err)
		}
		buf := make([]byte, 1)
		if _, err = res.Body.Read(buf); err != nil {
			t.Fatal(err)
		}
		kill()
		res.Body.Close()
	}
	time.Sleep(200 * time.Millisecond)
	var sessions v2.SessionsResponse
	n.get("/v2/sessions", "SessionsResponse", &sessions)
	if len(sessions.PTY) != 1 || sessions.PTY[0].ID != id {
		t.Fatalf("terminal lost: %+v", sessions.PTY)
	}
	if st := n.status(); st.Control.Capabilities == nil || n.status().Drain.Draining {
		t.Fatalf("%+v", st)
	}
	if out, failed := call(t, direct, "pty.close", map[string]any{"session_id": id}); failed {
		t.Fatal(out)
	}
}

// A PAT made through the control channel is honoured by the running Gateway at once and refused
// once it is rotated away or deleted; pairing documents come from the node's own LAN certificate.
func Test_ControlPatsReachTheGateway(t *testing.T) {
	n := startNode(t)
	status := func(token string) int {
		req, _ := http.NewRequest("POST", "http://"+n.gateway+"/mcp", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	call := func(method, path string, body any, def string, out any) {
		t.Helper()
		r, err := n.control.Do(context.Background(), method, path, body, nil)
		if err != nil || (r.Status != 200 && r.Status != 201 && r.Status != 204) {
			t.Fatalf("%s %s: %v %v", method, path, r, err)
		}
		if def != "" {
			if err = schemacheck.Validate(def, r.Body); err != nil {
				t.Fatalf("%s %s violates the contract: %v\n%s", method, path, err, r.Body)
			}
			if err = json.Unmarshal(r.Body, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The kernel creates the default token when none exists, so an unauthenticated request is refused
	// from the start; a PAT made through the channel is honoured at once.
	defaultToken, err := os.ReadFile(filepath.Join(n.stateDir, "pats", "default.token"))
	if err != nil {
		t.Fatal(err)
	}
	if status("") != 401 || status(strings.TrimSpace(string(defaultToken))) == 401 {
		t.Fatal("the gateway serves a request without a token, or refuses the default token")
	}
	var made v2.PatSecret
	call("POST", "/v2/pats", v2.PatCreateRequest{Name: "ci", Scopes: []string{"devices:read"}}, "PatSecret", &made)
	authorized := status(made.Token)
	if status("") != 401 || authorized == 401 {
		t.Fatalf("without token=%d with token=%d", status(""), authorized)
	}
	var rotated v2.PatSecret
	call("POST", "/v2/pats/ci/rotate", nil, "PatSecret", &rotated)
	if status(made.Token) != 401 || status(rotated.Token) != authorized {
		t.Fatal("rotation did not switch the credential")
	}
	call("DELETE", "/v2/pats/ci", nil, "", nil)
	if status(rotated.Token) != 401 || status(strings.TrimSpace(string(defaultToken))) == 401 {
		t.Fatal("deleting one PAT changed the others")
	}
	var export v2.PairExportResponse
	call("POST", "/v2/pair/export", nil, "PairExportResponse", &export)
	if export.Pair.Name != "app-ctl-node" || export.Pair.Fingerprint == "" {
		t.Fatalf("%+v", export)
	}
	if n.status().Gateway == nil {
		t.Fatal("gateway info missing")
	}
}

func TestV2TailnetAuthKeyWarningUsesNestedConfig(t *testing.T) {
	cfg := &config.Config{SchemaVersion: 2}
	host := &controlHost{d: controlDeps{store: config.Static(cfg)}, firewall: newFirewallWatch()}
	cfg.Mesh.Tailnet.Enabled = true
	contains := func(warnings []v2.Warning, code v2.WarningCode) bool {
		for _, warning := range warnings {
			if warning.Code == code {
				return true
			}
		}
		return false
	}
	if !contains(host.warnings(cfg, v2.StatusResponse{}), v2.WarnAuthkeyUnreadable) {
		t.Fatal("nested key warning omitted")
	}
	cfg.Mesh.Tailnet.AuthMethod = "browser"
	if contains(host.warnings(cfg, v2.StatusResponse{}), v2.WarnAuthkeyUnreadable) {
		t.Fatal("browser mode emitted key warning")
	}
}
