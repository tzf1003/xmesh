package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

func TestGatewayAsyncExecutionUsesAcceptingTransport(t *testing.T) {
	dir := t.TempDir()
	logger, err := audit.New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	var complete atomic.Bool
	var wrongSession atomic.Bool
	dial := func(ctx context.Context, _ mesh.Peer, _ Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		server := mcp.NewServer(&mcp.Implementation{Name: "ui-fixture", Version: "1"}, nil)
		var mu sync.Mutex
		acceptedSession := "<uninitialized>"
		for _, name := range []string{"sys.info", "ui.run", "ui.result"} {
			server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				mu.Lock()
				defer mu.Unlock()
				switch name {
				case "ui.run":
					acceptedSession = req.Session.ID()
					return agent.Result(map[string]any{"run_id": "r_same", "status": "accepted"}, nil), nil
				case "ui.result":
					if acceptedSession != req.Session.ID() {
						wrongSession.Store(true)
					}
					status := "running"
					if complete.Load() {
						status = "passed"
					}
					return agent.Result(map[string]any{"protocol_version": 1, "run_id": "r_same", "status": status, "body": "PRIVATE_TASK_RESULT"}, nil), nil
				default:
					return agent.Result(map[string]any{"backends": []any{}}, nil), nil
				}
			})
		}
		one, two := mcp.NewInMemoryTransports()
		if _, err := server.Connect(ctx, one, nil); err != nil {
			return nil, err
		}
		return mcpmux.Connect(ctx, two, opts)
	}
	g := New(Options{NodeName: "gateway", LocalPeer: mesh.Peer{ID: "device", Name: "device", Online: true}, Dial: dial, Audit: logger})
	t.Cleanup(g.Close)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true, ClientID: "client"}, nil }))
	t.Cleanup(server.Close)
	one, two := connect(t, server.URL+"/mcp"), connect(t, server.URL+"/mcp")
	if out := call(t, one, "local/ui.run", map[string]any{"protocol_version": 1, "steps": []any{"PRIVATE_TASK_ARGUMENT"}}); out.IsError {
		t.Fatal(out)
	}
	_ = call(t, two, "local/sys.info", map[string]any{})
	active, _ := logger.Executions()
	if len(active) != 1 || active[0].State != "async" || active[0].TaskStatus != "accepted" || active[0].Layer != "gateway" {
		t.Fatalf("accepted task snapshot: %+v", active)
	}
	id := active[0].ID
	waitFor(t, "task reaches running state", 4*time.Second, func() bool {
		active, _ := logger.Executions()
		return len(active) == 1 && active[0].TaskStatus == "running"
	})
	complete.Store(true)
	waitFor(t, "task completion recorded", 4*time.Second, func() bool { active, _ := logger.Executions(); return len(active) == 0 })
	if wrongSession.Load() {
		t.Fatal("result polled through another transport")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	found := false
	for _, file := range files {
		raw, _ := os.ReadFile(file)
		if strings.Contains(string(raw), "PRIVATE_TASK_") {
			t.Fatal("task content appeared in audit")
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var event map[string]any
			_ = json.Unmarshal([]byte(line), &event)
			found = found || event["event"] == "task_complete" && event["execution_id"] == id && event["task_status"] == "passed"
		}
	}
	if !found {
		t.Fatal("task completion correlation missing")
	}

	// Closing the caller's actual MCP session cancels its monitoring lifecycle.
	complete.Store(false)
	disconnected := connect(t, server.URL+"/mcp")
	if out := call(t, disconnected, "local/ui.run", map[string]any{"protocol_version": 1}); out.IsError {
		t.Fatal(out)
	}
	active, _ = logger.Executions()
	if len(active) != 1 {
		t.Fatalf("disconnected fixture was not tracked: %+v", active)
	}
	retiredID := active[0].ID
	if err := disconnected.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "closed MCP session releases tracking", 4*time.Second, func() bool { active, _ := logger.Executions(); return len(active) == 0 })
	found = false
	files, _ = filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, file := range files {
		raw, _ := os.ReadFile(file)
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var event map[string]any
			_ = json.Unmarshal([]byte(line), &event)
			found = found || event["event"] == "task_untracked" && event["execution_id"] == retiredID && event["task_status"] == "unknown" && event["error"] == "monitor_closed"
		}
	}
	if !found {
		t.Fatal("closed MCP session tracking outcome missing")
	}
}

func TestGatewayAndAgentExecutionHistoryLayers(t *testing.T) {
	logger, err := audit.New(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	a, _ := conformanceAgent(t, "device", map[string]config.Grant{"tag:peer": {Tools: []string{"*"}, Backends: []string{"*"}}}, nil)
	a.SetAudit(logger)
	g := New(Options{NodeName: "gateway", LocalPeer: mesh.Peer{Name: "device", ID: "device", Online: true}, Audit: logger, Dial: transportDial(a, func(Principal) identity.Principal {
		return identity.Principal{Node: "gateway", Tags: []string{"tag:peer"}}
	})})
	t.Cleanup(g.Close)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	var mu sync.Mutex
	layers := map[string]string{}
	logger.Observe(func(event map[string]any) {
		tool, _ := event["tool"].(string)
		if tool == "sys.info" || tool == "local/sys.info" {
			mu.Lock()
			layers[event["layer"].(string)] = event["execution_id"].(string)
			mu.Unlock()
		}
	})
	if out := call(t, connect(t, server.URL+"/mcp"), "local/sys.info", map[string]any{}); out.IsError {
		t.Fatal(out)
	}
	mu.Lock()
	defer mu.Unlock()
	if layers["agent"] == "" || layers["gateway"] == "" || layers["agent"] == layers["gateway"] {
		t.Fatalf("receiving layers do not distinguish executions: %+v", layers)
	}
}
