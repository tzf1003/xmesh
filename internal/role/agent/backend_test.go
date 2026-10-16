package agent

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAgentBackendProcess(t *testing.T) {
	helper := false
	for _, arg := range os.Args {
		if arg == "--xmesh-agent-backend-helper" {
			helper = true
		}
	}
	if !helper {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "ida", Version: "1"}, nil)
	for _, name := range []string{"echo", "crash"} {
		name := name
		s.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if name == "crash" {
				os.Exit(23)
			}
			var args map[string]any
			_ = json.Unmarshal(r.Params.Arguments, &args)
			if _, ok := args["_lease_token"]; ok {
				os.Exit(24)
			}
			return Result(args, nil), nil
		})
	}
	if e := s.Run(context.Background(), &mcp.StdioTransport{}); e != nil {
		os.Exit(25)
	}
	os.Exit(0)
}

// conformance: CTR-BE-01 CTR-LEASE-01 CTR-MUX-04 CON-CONC-07 INV-04
func Test_Gate2_TwoMCPClients(t *testing.T) {
	path, _ := configuration(t)
	b, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	bin, _ := os.Executable()
	doc["agent"].(map[string]any)["backends"] = map[string]any{"ida": map[string]any{"transport": "stdio", "command": []string{bin, "-test.run=TestAgentBackendProcess", "--", "--xmesh-agent-backend-helper"}, "policy": "exclusive"}}
	b, _ = json.Marshal(doc)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	store, e := config.NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	one := connect(t, server.URL+"/mcp")
	two := connect(t, server.URL+"/mcp")
	grant := tool(t, one, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})
	if grant.IsError {
		t.Fatal(grant)
	}
	token := object(t, grant)["token"]
	denied := tool(t, two, "ida.echo", map[string]any{"_lease_token": token, "marker": "second"})
	if !denied.IsError || object(t, denied)["error"] != "resource_busy" {
		t.Fatal(denied)
	}
	echo := tool(t, one, "ida.echo", map[string]any{"_lease_token": token, "token": "business", "marker": "first"})
	if echo.IsError || object(t, echo)["marker"] != "first" {
		t.Fatal(echo)
	}
	if crash := tool(t, one, "ida.crash", map[string]any{"_lease_token": token}); !crash.IsError || object(t, crash)["error"] != "backend_unavailable" {
		t.Fatal(crash)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		grant = tool(t, two, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})
		if !grant.IsError {
			if object(t, grant)["token"].(float64) <= token.(float64) {
				t.Fatal("token not advanced")
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grant.IsError {
		t.Fatal(grant)
	}
	denied = tool(t, one, "ida.echo", map[string]any{"_lease_token": token})
	if !denied.IsError || object(t, denied)["reason"] != "token_stale" {
		t.Fatal(denied)
	}
	list, e := two.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range list.Tools {
		if strings.HasPrefix(v.Name, "ida.") {
			found = true
		}
	}
	if !found {
		t.Fatal("restarted tools absent")
	}
}

func Test_CTRCFG02_BackendSupervisionReload(t *testing.T) {
	path, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	b, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	bin, _ := os.Executable()
	doc["agent"].(map[string]any)["backends"] = map[string]any{"ida": map[string]any{"transport": "stdio", "policy": "exclusive", "command": []string{bin, "-test.run=TestAgentBackendProcess", "--", "--xmesh-agent-backend-helper"}}}
	write := func() {
		b, _ := json.Marshal(doc)
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	wait := func(want bool) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			list, e := client.ListTools(context.Background(), nil)
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, tool := range list.Tools {
				if tool.Name == "ida.echo" {
					found = true
				}
			}
			if found == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("tool catalog did not reflect backend generation", want)
	}
	write()
	wait(true)
	grant := tool(t, client, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})
	if grant.IsError {
		t.Fatal(grant)
	}
	delete(doc["agent"].(map[string]any), "backends")
	write()
	wait(false)
	if a.Leases.Owner("backend:ida") != "" {
		t.Fatal("deleted backend retained lease")
	}
}
