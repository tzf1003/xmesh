package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

// TestUIMCPTwoNodes exercises the actual TypeScript MCP executable through two
// Agent HTTP endpoints and one Gateway, including per-caller evidence isolation.
func TestUIMCPTwoNodes(t *testing.T) {
	cli := os.Getenv("XMESH_UI_MCP_CLI")
	if cli == "" {
		t.Skip("set XMESH_UI_MCP_CLI to the built UI MCP CLI")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	website := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<label>User<input aria-label="User"></label><button onclick="document.querySelector('output').textContent=document.querySelector('input').value">Search</button><output data-testid="result"></output>`))
	}))
	defer website.Close()
	var peers []mesh.Peer
	for _, name := range []string{"ui-a", "ui-b"} {
		root := t.TempDir()
		root, _ = filepath.EvalSymlinks(root)
		env := map[string]any{"driver": "web", "url": website.URL, "allowed_origins": []string{website.URL}, "fixtures": "mock"}
		if browser := os.Getenv("XMESH_UI_TEST_BROWSER"); browser != "" {
			env["executable"] = browser
		}
		doc := map[string]any{"version": 1, "state_dir": filepath.Join(root, "ui-state"), "environments": map[string]any{"test": env}}
		body, _ := json.Marshal(doc)
		file := filepath.Join(root, "ui.json")
		if err := os.WriteFile(file, body, 0600); err != nil {
			t.Fatal(err)
		}
		c := &config.Config{}
		c.Node.Name = name
		c.Agent.DefaultCWD = root
		c.Agent.CWDPolicy = cwd.Policy{Allow: []string{root}}
		c.Agent.Exec = config.Exec{MaxConcurrent: 4, ExclusiveSlots: 1, DefaultTimeout: "5s", InlineOutputBytes: "256KiB", IdempotencyCache: 20}
		c.Agent.PTY = config.PTY{MaxSessions: 20, MaxPerCaller: 8, IdleTimeout: "30m", OrphanGrace: "10m", DefaultShell: "auto", ScrollbackBytes: "1MiB", ClosedRetain: "5m", ClosedRetainMax: 20}
		c.Agent.Authz = map[string]config.Grant{"tag:peer": {Tools: []string{"*"}, Backends: []string{"*"}}}
		c.Agent.Backends = map[string]config.Backend{"ui": {Transport: "stdio", Policy: "per-caller", Command: []string{node, cli, "serve", "--config", file}, ShutdownTimeout: "10s", ProcessCleanup: "tree"}}
		a, err := agent.New(config.Static(c))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Close)
		s := httptest.NewServer(a.HTTP(agent.Local))
		t.Cleanup(s.Close)
		peers = append(peers, mesh.Peer{ID: name, Name: name, Address: s.URL + "/mcp", Online: true, Tags: []string{"tag:device"}})
	}
	g := New(Options{NodeName: "test-gateway", Ingress: true, Dial: RemoteDial(http.DefaultClient)})
	t.Cleanup(g.Close)
	g.Update(peers)
	s := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
		return Principal{ClientID: "test", Scopes: []string{"devices:read", "backend:*:*"}}, nil
	}))
	t.Cleanup(s.Close)
	one, two := connect(t, s.URL+"/mcp"), connect(t, s.URL+"/mcp")
	var firstRun string
	for _, peer := range peers {
		prefix := peer.Name + "/ui."
		opened := call(t, one, prefix+"open", map[string]any{"environment_id": "test"})
		if opened.IsError {
			t.Fatal(object(t, opened))
		}
		session := object(t, opened)["session_id"]
		started := call(t, one, prefix+"run", map[string]any{"protocol_version": 1, "session_id": session, "request_id": "test", "steps": []any{
			map[string]any{"id": "fill", "action": "fill", "target": map[string]any{"label": "User"}, "value": peer.Name},
			map[string]any{"id": "click", "action": "click", "target": map[string]any{"role": "button", "name": "Search"}},
			map[string]any{"id": "verify", "action": "assert", "target": map[string]any{"testId": "result"}, "expect": map[string]any{"text": peer.Name}},
		}})
		if started.IsError {
			t.Fatal(object(t, started))
		}
		run := object(t, started)["run_id"].(string)
		if firstRun == "" {
			firstRun = run
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			out := call(t, one, prefix+"result", map[string]any{"run_id": run})
			result := object(t, out)
			if result["status"] == "passed" {
				break
			}
			if result["status"] == "failed" || out.IsError || time.Now().After(deadline) {
				t.Fatal(result)
			}
			time.Sleep(50 * time.Millisecond)
		}
		denied := call(t, two, prefix+"result", map[string]any{"run_id": run})
		if !denied.IsError {
			t.Fatal("run leaked across callers")
		}
		if out := call(t, one, prefix+"close", map[string]any{"session_id": session}); out.IsError {
			t.Fatal(object(t, out))
		}
	}
	wrong := call(t, one, "ui-b/ui.result", map[string]any{"run_id": firstRun})
	if !wrong.IsError {
		t.Fatal("run leaked across nodes")
	}
	_ = one.Close()
	_ = two.Close()
	// The next MCP session gets a fresh per-caller backend instance.
	fresh := connect(t, s.URL+"/mcp")
	denied := call(t, fresh, "ui-a/ui.result", map[string]any{"run_id": firstRun})
	if !denied.IsError {
		t.Fatal("old run resumed across MCP sessions")
	}
	_, _ = fresh.ListTools(context.Background(), nil)
}
