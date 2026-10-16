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
func (n *node) get(path string) map[string]any {
	n.t.Helper()
	r, err := n.control.Do(context.Background(), "GET", path, nil, nil)
	if err != nil || r.Status != 200 {
		n.t.Fatalf("GET %s: %v %v", path, r, err)
	}
	var out map[string]any
	if err = json.Unmarshal(r.Body, &out); err != nil {
		n.t.Fatal(err)
	}
	return out
}
func (n *node) post(path string, body any) {
	n.t.Helper()
	r, err := n.control.Do(context.Background(), "POST", path, body, nil)
	if err != nil || r.Status != 200 {
		n.t.Fatalf("POST %s: %v %v", path, r, err)
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

// conformance: CON-ARCH-09 the channel is not on TCP, not behind the Gateway, and not an MCP tool
func Test_CONARCH09_ControlChannelIsLocalOnly(t *testing.T) {
	n := startNode(t)
	ep, err := control.ReadEndpoint(n.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Transport != "unix" && ep.Transport != "pipe" || strings.Contains(ep.Address, "127.0.0.1") || strings.Contains(ep.Address, "http") {
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
	st := n.get("/v1/status")
	if st["node"] != "ctl-node" || st["version"] != Version || st["control_api"] != control.APIVersion || st["draining"] != false {
		t.Fatalf("%v", st)
	}
	if roles := st["roles"].([]any); len(roles) != 2 {
		t.Fatalf("%v", roles)
	}
	if limits, _ := st["limits"].(map[string]any); limits["pty_max_sessions"] == nil {
		t.Fatalf("limits: %v", st["limits"])
	}
	if network := st["network"].(map[string]any); network["mode"] != "local" {
		t.Fatalf("network: %v", network)
	}
	if peers := n.get("/v1/peers"); peers["mode"] != "local" || len(peers["peers"].([]any)) != 0 {
		t.Fatalf("%v", peers)
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
	if st := n.get("/v1/status"); st["draining"] != true || st["health"] != "draining" {
		t.Fatalf("%v", st)
	}
	out, isErr := call(t, direct, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if !isErr || out["error"] != "policy_denied" || out["rule"] != "mesh.draining" {
		t.Fatalf("drained node accepted work: %v", out)
	}
	n.post("/v1/resume", nil)
	if st := n.get("/v1/status"); st["draining"] != false || st["health"] == "draining" {
		t.Fatalf("%v", st)
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
	sessions := n.get("/v1/sessions")
	ptys := sessions["pty"].([]any)
	if len(ptys) != 1 || ptys[0].(map[string]any)["id"] != opened["session_id"] {
		t.Fatalf("sessions: %v", sessions)
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
	got := n.get("/v1/audit?tool=exec.run")
	if records := got["records"].([]any); len(records) == 0 || got["rejected"] != 0.0 {
		t.Fatalf("audit view: %v", got)
	}
}

func Test_ControlEndpointRemovedOnShutdown(t *testing.T) {
	n := startNode(t)
	n.stop()
	if _, err := control.ReadEndpoint(n.stateDir); err == nil {
		t.Fatal("control.json left behind")
	}
}
