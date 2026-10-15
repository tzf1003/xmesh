package backend

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/lease"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBackendProcess(t *testing.T) {
	helper := false
	for _, arg := range os.Args {
		if arg == "--xmesh-backend-helper" {
			helper = true
		}
	}
	if !helper {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "mock-ida", Version: "1"}, nil)
	for _, name := range []string{"echo", "crash", "slow"} {
		name := name
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if name == "crash" {
				os.Exit(23)
			}
			if name == "slow" {
				time.Sleep(20 * time.Millisecond)
			}
			var args map[string]any
			_ = json.Unmarshal(r.Params.Arguments, &args)
			if _, ok := args["_lease_token"]; ok {
				return nil, toolerr.New("internal", "token reached backend", "op", "test")
			}
			b, _ := json.Marshal(args)
			return &mcp.CallToolResult{StructuredContent: args, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
		})
	}
	if e := server.Run(context.Background(), &mcp.StdioTransport{}); e != nil {
		os.Exit(24)
	}
	os.Exit(0)
}
func fixture(t *testing.T, policy string) (*Manager, *lease.Manager) {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	bin, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	leases := lease.New()
	t.Cleanup(leases.Close)
	c := &config.Config{}
	c.Agent.DefaultCWD = root
	c.Agent.CWDPolicy = cwd.Policy{Allow: []string{root}}
	c.Agent.Backends = map[string]config.Backend{"ida": {Transport: "stdio", Command: []string{bin, "-test.run=TestBackendProcess", "--", "--xmesh-backend-helper"}, Policy: policy, IdleTimeout: "30m"}}
	m := New(c, leases, nil, nil)
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.WaitReady(ctx)
	if m.States()["ida"] != "running" {
		t.Fatal("mock not running", m.States())
	}
	return m, leases
}
func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func Test_Gate2_TokenStrippingAndCrashRestart(t *testing.T) {
	m, l := fixture(t, "exclusive")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, e := m.Control(ctx, "n/a", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
	if e != nil {
		t.Fatal(e)
	}
	token := a.(lease.Lease).Token
	if _, e = m.Call(ctx, "n/b", "ida.echo", raw(map[string]any{}), mcpmux.Progress{}); e == nil || e.(*toolerr.Error).Code != "lease_required" {
		t.Fatal(e)
	}
	if _, e = m.Call(ctx, "n/b", "ida.echo", raw(map[string]any{"_lease_token": token}), mcpmux.Progress{}); e == nil || e.(*toolerr.Error).Code != "resource_busy" {
		t.Fatal(e)
	}
	out, e := m.Call(ctx, "n/a", "ida.echo", raw(map[string]any{"_lease_token": token, "token": "ordinary-business-field", "value": "hello"}), mcpmux.Progress{})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(out.StructuredContent)
	if strings.Contains(string(b), "_lease_token") || !strings.Contains(string(b), "ordinary-business-field") {
		t.Fatal(string(b))
	}
	expires := a.(lease.Lease).Expires
	owner := l.Owner("backend:ida")
	if owner != "n/a" {
		t.Fatal(owner)
	}
	m.mu.Lock()
	actor := m.instances["ida"]
	m.mu.Unlock()
	actor.mu.Lock()
	_ = actor.bridge
	actor.mu.Unlock()
	if _, e = m.Call(ctx, "n/a", "ida.crash", raw(map[string]any{"_lease_token": token}), mcpmux.Progress{}); e == nil || e.(*toolerr.Error).Code != "backend_unavailable" {
		t.Fatal("crash result", e)
	}
	if e = l.Validate("backend:ida", "n/a", token); e == nil {
		t.Fatal("crashed token valid")
	}
	deadline := time.Now().Add(5 * time.Second)
	var fresh lease.Lease
	for time.Now().Before(deadline) {
		v, e := m.Control(ctx, "n/b", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
		if e == nil {
			fresh = v.(lease.Lease)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fresh.Token <= token {
		t.Fatal("fresh token", fresh, expires)
	}
	if _, e = m.Call(ctx, "n/a", "ida.echo", raw(map[string]any{"_lease_token": token}), mcpmux.Progress{}); e == nil || e.(*toolerr.Error).Fields["reason"] != "token_stale" {
		t.Fatal("stale", e)
	}
}
func Test_CTRBE01_SharedAndPerCaller(t *testing.T) {
	for _, policy := range []string{"shared", "per-caller"} {
		t.Run(policy, func(t *testing.T) {
			m, _ := fixture(t, policy)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var wg sync.WaitGroup
			for _, caller := range []string{"n/a", "n/b"} {
				caller := caller
				wg.Add(1)
				go func() {
					defer wg.Done()
					out, e := m.Call(ctx, caller, "ida.slow", raw(map[string]any{"_lease_token": "ignored", "caller": caller}), mcpmux.Progress{})
					if e != nil || out.IsError {
						t.Errorf("%v %v", out, e)
					}
				}()
			}
			wg.Wait()
			if policy == "per-caller" {
				m.Disconnect("n/a")
				m.mu.Lock()
				_, exists := m.instances["ida/n/a"]
				m.mu.Unlock()
				if exists {
					t.Fatal("caller process retained")
				}
			}
		})
	}
}

func Test_PerCallerIdleRecycle(t *testing.T) {
	m, _ := fixture(t, "per-caller")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, e := m.Call(ctx, "n/a", "ida.echo", raw(map[string]any{}), mcpmux.Progress{}); e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	a := m.instances["ida/n/a"]
	m.mu.Unlock()
	a.mu.Lock()
	a.lastUse = time.Now().Add(-time.Hour)
	a.mu.Unlock()
	a.gate <- struct{}{}
	m.reap(time.Now())
	m.mu.Lock()
	retained := m.instances["ida/n/a"] == a
	m.mu.Unlock()
	if !retained {
		t.Fatal("in-flight actor recycled")
	}
	<-a.gate
	m.reap(time.Now())
	m.mu.Lock()
	_, retained = m.instances["ida/n/a"]
	_, discovery := m.instances["ida/discovery"]
	m.mu.Unlock()
	if retained || !discovery {
		t.Fatal("idle/discovery lifecycle")
	}
	if _, e := m.Call(ctx, "n/a", "ida.echo", raw(map[string]any{}), mcpmux.Progress{}); e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	fresh := m.instances["ida/n/a"] != a
	m.mu.Unlock()
	if !fresh {
		t.Fatal("actor not recreated")
	}
}

func Test_CONSEC06_PolicyReloadRetainsProcessAndLease(t *testing.T) {
	m, leases := fixture(t, "exclusive")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, e := m.Control(ctx, "n/a", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
	if e != nil {
		t.Fatal(e)
	}
	token := v.(lease.Lease).Token
	m.mu.Lock()
	original := m.instances["ida"]
	next := *m.cfg
	m.mu.Unlock()
	next.Agent.Policy.Exec.AllowCmd = []string{"a-command-that-is-not-the-backend"}
	m.Configure(&next)
	m.mu.Lock()
	retained := m.instances["ida"] == original
	m.mu.Unlock()
	if !retained || leases.Validate("backend:ida", "n/a", token) != nil {
		t.Fatal("policy reload discarded active context or lease")
	}
	if _, e = m.Call(ctx, "n/a", "ida.echo", raw(map[string]any{"_lease_token": token}), mcpmux.Progress{}); e != nil {
		t.Fatal("existing process", e)
	}
	// A later process start uses the updated policy, including restart paths.
	if bridge, e := original.connect(ctx); e == nil {
		_ = bridge.Close()
		t.Fatal("new process bypassed updated policy")
	} else if e.(*toolerr.Error).Code != "policy_denied" {
		t.Fatal(e)
	}
}
