package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/api/control/v1/schemacheck"
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
	doc := map[string]any{"node": map[string]any{"name": "ctl-node", "roles": []string{"agent", "gateway"}}, "mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}}, "gateway": map[string]any{"listen": n.gateway}, "agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "authz": map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}}}
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
			if r, err := n.control.Do(ctx, "GET", "/v1/status", nil, nil); err == nil && r.Status == 200 {
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
func (n *node) status() v1.StatusResponse {
	n.t.Helper()
	var st v1.StatusResponse
	n.get("/v1/status", "StatusResponse", &st)
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
func session(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		attempt, stop := context.WithTimeout(context.Background(), time.Second)
		s, err := client.Connect(attempt, &mcp.StreamableClientTransport{Endpoint: endpoint}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
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
	if ep.Transport != v1.TransportUnix && ep.Transport != v1.TransportPipe || strings.Contains(ep.Address(), "127.0.0.1") || strings.Contains(ep.Address(), "http") {
		t.Fatalf("endpoint is not a socket or pipe: %+v", ep)
	}
	// Neither the Agent's nor the Gateway's TCP listener serves the control API.
	for _, address := range []string{n.agent, n.gateway} {
		for _, path := range []string{"/v1/status", "/v1/config", "/v1/events", "/v1/drain", "/control/drain"} {
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
		s := session(t, endpoint)
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
	if st.Hostname != "ctl-node" || st.Control.KernelVersion != Version || st.Control.APIVersion != v1.APIVersion || st.Drain.Draining || st.Lifecycle != "running" {
		t.Fatalf("%+v", st)
	}
	if len(st.Roles) != 2 || st.Limits.PTYMaxSessions == 0 || st.Network.Mode != "local" || st.Paths.StateDir != n.stateDir || st.Paths.ConfigFile == "" {
		t.Fatalf("%+v", st)
	}
	if st.Gateway == nil || st.Gateway.Listen != n.gateway || st.Gateway.URL != "http://"+n.gateway+"/mcp" || st.Gateway.Auth != "none" || st.Ingress != nil {
		t.Fatalf("gateway info: %+v ingress %+v", st.Gateway, st.Ingress)
	}
	var peers v1.PeersResponse
	n.get("/v1/peers", "PeersResponse", &peers)
	if len(peers.Peers) != 1 || !peers.Peers[0].Self || peers.Peers[0].Name != "ctl-node" || peers.Peers[0].Connection != "local" {
		t.Fatalf("%+v", peers)
	}
	var cfg v1.ConfigResponse
	n.get("/v1/config", "ConfigResponse", &cfg)
	if !cfg.Valid || cfg.EffectiveETag != cfg.ETag {
		t.Fatalf("%+v", cfg)
	}
}

// The channel can drain the node and bring it back, which the MCP-side drain cannot.
func Test_ControlDrainResumeEndToEnd(t *testing.T) {
	n := startNode(t)
	direct := session(t, "http://"+n.agent+"/mcp")
	gateway := session(t, "http://"+n.gateway+"/mcp")
	if _, isErr := call(t, direct, "exec.run", map[string]any{"cmd": "go", "args": []string{"version"}}); isErr {
		t.Fatal("exec before drain failed")
	}
	n.post("/v1/drain", map[string]string{"mode": "keep"})
	if st := n.status(); !st.Drain.Draining || *st.Drain.Mode != v1.DrainKeep {
		t.Fatalf("%+v", st.Drain)
	}
	out, isErr := call(t, direct, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if !isErr || out["error"] != "policy_denied" || out["rule"] != "mesh.draining" {
		t.Fatalf("drained node accepted work: %v", out)
	}
	n.post("/v1/resume", nil)
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
	var sessions v1.SessionsResponse
	n.get("/v1/sessions", "SessionsResponse", &sessions)
	if len(sessions.PTY) != 1 || sessions.PTY[0].ID != opened["session_id"] || sessions.PTY[0].State != "active" || sessions.PTY[0].CreatedAt.IsZero() {
		t.Fatalf("sessions: %+v", sessions)
	}
	call(t, direct, "pty.close", map[string]any{"session_id": opened["session_id"]})
}

func Test_ControlEventsAndAuditFromRunningNode(t *testing.T) {
	n := startNode(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := n.control.Stream(ctx, "/v1/events", nil)
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
	direct := session(t, "http://"+n.agent+"/mcp")
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
	var got v1.AuditResponse
	n.get("/v1/audit?tool=exec.run", "AuditResponse", &got)
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
	direct := session(t, "http://"+n.agent+"/mcp")
	program := filepath.Join(n.stateDir, "..", "sleep.go")
	if err := os.WriteFile(program, []byte("package main\nimport \"time\"\nfunc main() { time.Sleep(4 * time.Second) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		call(t, direct, "exec.run", map[string]any{"cmd": "go", "args": []string{"run", program, "--secret-flag=SECRET-ARG"}, "env": map[string]string{"SECRET_ENV": "SECRET-VALUE"}})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var sessions v1.SessionsResponse
		n.get("/v1/sessions", "SessionsResponse", &sessions)
		if len(sessions.Exec) == 1 {
			run := sessions.Exec[0]
			raw, _ := json.Marshal(sessions)
			if run.Cmd != "go" || run.Mode != "shared" || strings.Contains(string(raw), "SECRET") {
				t.Fatalf("%+v", run)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exec run never appeared")
		}
		time.Sleep(50 * time.Millisecond)
	}
	<-done
	var after v1.SessionsResponse
	n.get("/v1/sessions", "SessionsResponse", &after)
	if len(after.Exec) != 0 {
		t.Fatalf("%+v", after.Exec)
	}
}

// Rule CON-ARCH-10: the kernel does not depend on the UI. An event subscriber that vanishes (a crashed
// UI) leaves the terminal, the node status and the channel itself untouched.
func Test_CONARCH10_UIExitDoesNotAffectKernel(t *testing.T) {
	n := startNode(t)
	direct := session(t, "http://"+n.agent+"/mcp")
	opened, failed := call(t, direct, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if failed {
		t.Fatal(opened)
	}
	id, _ := opened["session_id"].(string)
	// a UI subscribes to the event stream and is then killed without a goodbye
	for i := 0; i < 3; i++ {
		ctx, kill := context.WithCancel(context.Background())
		res, err := n.control.Stream(ctx, "/v1/events", nil)
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
	var sessions v1.SessionsResponse
	n.get("/v1/sessions", "SessionsResponse", &sessions)
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
