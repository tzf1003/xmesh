package app

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func address(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	a := l.Addr().String()
	l.Close()
	return a
}

// conformance: CON-ARCH-03
func Test_Runtime_AgentGatewaySharedState(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	gatewayAddr := address(t)
	agentAddr := address(t)
	doc := map[string]any{"node": map[string]any{"name": "local-node", "roles": []string{"agent", "gateway"}}, "mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}}, "gateway": map[string]any{"listen": gatewayAddr}, "agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "authz": map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}}}
	b, _ := json.Marshal(doc)
	path := filepath.Join(root, "config.json")
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	store, e := config.NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, store, Options{Local: true, Listen: agentAddr, StateDir: filepath.Join(root, "state")})
	}()
	defer func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			t.Error("runtime shutdown hung")
		}
	}()
	connect := func(endpoint string) *mcp.ClientSession {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			attempt, stop := context.WithTimeout(context.Background(), time.Second)
			s, e := client.Connect(attempt, &mcp.StreamableClientTransport{Endpoint: endpoint}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
			stop()
			if e == nil {
				return s
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("listener not ready", endpoint)
		return nil
	}
	gateway := connect("http://" + gatewayAddr + "/mcp")
	defer gateway.Close()
	direct := connect("http://" + agentAddr + "/mcp")
	defer direct.Close()
	call := func(s *mcp.ClientSession, name string, args any) map[string]any {
		t.Helper()
		timeout := 5 * time.Second
		if os.PathSeparator == '\\' && name == "local/pty.open" {
			// Allow first-launch Windows console and shell initialization on CI.
			timeout = 30 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		o, e := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil || o.IsError {
			t.Fatalf("%s (deadline %s) %v %v", name, timeout, o, e)
		}
		b, _ := json.Marshal(o.StructuredContent)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		return v
	}
	opened := call(gateway, "local/pty.open", map[string]any{"cols": 80, "rows": 24})
	if opened["owner"] == "local/" {
		t.Fatal("missing downstream session", opened)
	}
	list := call(direct, "pty.list", map[string]any{})
	if len(list["sessions"].([]any)) != 1 {
		t.Fatal(list)
	}
	id := opened["session_id"]
	call(gateway, "local/pty.close", map[string]any{"session_id": id})
	call(gateway, "local/fs.write", map[string]any{"path": "roundtrip", "content": "runtime"})
	call(direct, "fs.read", map[string]any{"path": "roundtrip"})
	files, e := filepath.Glob(filepath.Join(root, "state", "audit", "*.jsonl"))
	if e != nil || len(files) == 0 {
		t.Fatal("missing audit", files, e)
	}
	_, port, _ := net.SplitHostPort(gatewayAddr)
	if _, e = strconv.Atoi(port); e != nil {
		t.Fatal(e)
	}
}
