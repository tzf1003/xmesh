package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

type budgetFixture struct {
	t       *testing.T
	agents  map[string]*agent.Agent
	peers   []mesh.Peer
	path    string
	opts    Options
	g       *Gateway
	limit   int
	configs map[string]*config.Config
}

func newBudgetFixture(t *testing.T, limit int) *budgetFixture {
	t.Helper()
	if runtime.GOOS == "windows" && os.Getenv("XMESH_TEST_WINDOWS_PTY") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_PTY=1 on Windows with ConPTY and PowerShell")
	}
	f := &budgetFixture{t: t, agents: map[string]*agent.Agent{}, configs: map[string]*config.Config{}, path: filepath.Join(t.TempDir(), "ingress-pty.json"), limit: limit}
	for _, name := range []string{"device-a", "device-b"} {
		root, _ := filepath.EvalSymlinks(t.TempDir())
		c := &config.Config{}
		c.Node.Name = name
		c.Agent.DefaultCWD = root
		c.Agent.CWDPolicy = cwd.Policy{Allow: []string{root}}
		c.Agent.Authz = map[string]config.Grant{"tag:ingress": {Tools: []string{"*"}}}
		shell := "/bin/sh"
		if runtime.GOOS == "windows" {
			shell = "auto"
		}
		c.Agent.PTY = config.PTY{MaxSessions: 30, MaxPerCaller: 20, IdleTimeout: "30m", OrphanGrace: "10m", DefaultShell: shell, ScrollbackBytes: "1MiB", ClosedRetain: "5m", ClosedRetainMax: 20}
		a, err := agent.New(config.Static(c))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Close)
		f.agents[name] = a
		f.configs[name] = c
		f.peers = append(f.peers, mesh.Peer{Name: name, ID: name, Online: true})
	}
	f.opts = Options{NodeName: "ingress", Ingress: true, LocalPeer: f.peers[0], Dial: func(ctx context.Context, peer mesh.Peer, p Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		transport, err := f.agents[peer.Name].Transport(ctx, identity.Principal{Node: "ingress-node", Tags: []string{"tag:ingress"}, OAuthClient: p.ClientID})
		if err != nil {
			return nil, err
		}
		return mcpmux.Connect(ctx, transport, opts)
	}}
	f.restart()
	return f
}

func (f *budgetFixture) restart() {
	f.t.Helper()
	if f.g != nil {
		f.g.Close()
	}
	var err error
	f.opts.PTYBudget, err = NewPTYBudget(f.limit, f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	f.g = New(f.opts)
	f.g.Update(f.peers[1:])
	f.t.Cleanup(f.g.Close)
}

func (f *budgetFixture) client(id string) *mcp.ClientSession {
	f.t.Helper()
	server := httptest.NewServer(f.g.HTTP(func(*http.Request) (Principal, error) {
		// The resource query is control-plane I/O, even for a pty-only client.
		return Principal{ClientID: id, Scopes: []string{"pty:*", "sys:*"}}, nil
	}))
	f.t.Cleanup(server.Close)
	return connect(f.t, server.URL+"/mcp")
}

func requireBudgetRule(t *testing.T, out *mcp.CallToolResult, rule string) {
	t.Helper()
	if !out.IsError || object(t, out)["rule"] != rule {
		t.Fatalf("want %s, got %#v", rule, out)
	}
}

func Test_IngressPTYConcurrentAcrossDevices(t *testing.T) {
	f := newBudgetFixture(t, 3)
	one, two := f.client("same-client"), f.client("same-client")
	type result struct {
		out *mcp.CallToolResult
		err error
	}
	results := make(chan result, 12)
	var workers sync.WaitGroup
	for i := range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			client, device := one, "device-a"
			if i%2 == 1 {
				client, device = two, "device-b"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: device + "/pty.open", Arguments: map[string]any{"cols": 80, "rows": 24}})
			results <- result{out, err}
		}()
	}
	workers.Wait()
	close(results)
	succeeded := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if !result.out.IsError {
			succeeded++
		} else {
			requireBudgetRule(t, result.out, "quota.exceeded")
		}
	}
	actual := 0
	for _, a := range f.agents {
		actual += a.PTY.BudgetSnapshot().Counts["oauth:same-client"]
	}
	if succeeded != 3 || actual != 3 {
		t.Fatal("admitted and actual terminals", succeeded, actual)
	}
	other := f.client("other-client")
	if out := call(t, other, "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}); out.IsError {
		t.Fatal("independent principal rejected", out)
	}
}

func Test_IngressPTYRestartOrphansAndClose(t *testing.T) {
	f := newBudgetFixture(t, 1)
	one := f.client("client")
	if out := call(t, one, "device-a/pty.open", map[string]any{"cols": 0, "rows": 24}); !out.IsError {
		t.Fatal("invalid terminal accepted")
	}
	opened := object(t, call(t, one, "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}))
	if opened["session_id"] == nil {
		t.Fatal(opened)
	}
	f.restart()
	two := f.client("client")
	requireBudgetRule(t, call(t, two, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}), "quota.exceeded")
	// Readiness establishes the receiving caller on the Agent.
	if out := call(t, two, "device-a/pty.list", map[string]any{}); out.IsError {
		t.Fatal(out)
	}
	var receiver string
	for id, budget := range f.agents["device-a"].PTY.BudgetSnapshot().Callers {
		if budget == "oauth:client" {
			receiver = id
		}
	}
	if receiver == "" {
		t.Fatal("missing connected receiver")
	}
	if out := call(t, two, "device-a/pty.transfer", map[string]any{"session_id": opened["session_id"], "to_caller": receiver}); out.IsError {
		t.Fatal("same-principal orphan recovery at limit", out)
	}
	if out := call(t, two, "device-a/pty.close", map[string]any{"session_id": opened["session_id"]}); out.IsError {
		t.Fatal(out)
	}
	if out := call(t, two, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}); out.IsError {
		t.Fatal("closed resource retained quota", out)
	}
}

func Test_IngressPTYTransferReceiverBudget(t *testing.T) {
	f := newBudgetFixture(t, 1)
	one, two := f.client("sender"), f.client("receiver")
	first := object(t, call(t, one, "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}))
	second := object(t, call(t, two, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}))
	if out := call(t, two, "device-a/pty.list", map[string]any{}); out.IsError {
		t.Fatal(out)
	}
	var receiver string
	for id, principal := range f.agents["device-a"].PTY.BudgetSnapshot().Callers {
		if principal == "oauth:receiver" {
			receiver = id
		}
	}
	args := map[string]any{"session_id": first["session_id"], "to_caller": receiver}
	requireBudgetRule(t, call(t, one, "device-a/pty.transfer", args), "quota.exceeded")
	for _, row := range f.agents["device-a"].PTY.List() {
		if row["id"] == first["session_id"] && row["owner"] != first["owner"] {
			t.Fatal("rejected transfer changed owner")
		}
	}
	if out := call(t, two, "device-b/pty.close", map[string]any{"session_id": second["session_id"]}); out.IsError {
		t.Fatal(out)
	}
	if out := call(t, one, "device-a/pty.transfer", args); out.IsError {
		t.Fatal("receiver with free budget rejected", out)
	}
	requireBudgetRule(t, call(t, two, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}), "quota.exceeded")
}

func Test_IngressPTYOfflineInventorySurvivesRestart(t *testing.T) {
	f := newBudgetFixture(t, 2)
	one := f.client("client")
	if out := call(t, one, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}); out.IsError {
		t.Fatal(out)
	}
	f.restart()
	f.g.Update(nil)
	two := f.client("client")
	requireBudgetRule(t, call(t, two, "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}), "quota.unavailable")
	if len(f.agents["device-a"].PTY.List()) != 0 {
		t.Fatal("offline accounting allowed a new terminal")
	}
	f.g.Update(f.peers[1:])
	if out := call(t, two, "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}); out.IsError {
		t.Fatal("returning device did not restore admission", out)
	}
}

func Test_IngressPTYStateValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	for _, body := range []string{`null`, `{}`, `{"version":2,"peers":{}}`, `{"version":1,"peers":{"id":{"name":"device"}}}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewPTYBudget(1, path); err == nil {
			t.Fatal("invalid journal accepted")
		}
	}
	if _, err := NewPTYBudget(0, ""); err == nil {
		t.Fatal("zero budget accepted")
	}
}

func Test_IngressPTYUncertainCallFencesUntilAgentRestart(t *testing.T) {
	f := newBudgetFixture(t, 2)
	client := f.client("client")
	if out := call(t, client, "device-a/pty.list", map[string]any{}); out.IsError {
		t.Fatal(out)
	}
	f.g.mu.Lock()
	var view *view
	for v := range f.g.views {
		view = v
	}
	f.g.mu.Unlock()
	budget := f.g.opts.PTYBudget
	_, err := budget.call(context.Background(), view, f.peers[0], "pty.open", nil, func(*mcpmux.Bridge) (*mcp.CallToolResult, error) {
		return nil, errors.New("transport ended before result")
	})
	if err == nil {
		t.Fatal("lost transport result was hidden")
	}
	f.restart()
	client = f.client("client")
	requireBudgetRule(t, call(t, client, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}), "quota.unavailable")
	f.g.Close()
	f.agents["device-a"].Close()
	replacement, err := agent.New(config.Static(f.configs["device-a"]))
	if err != nil {
		t.Fatal(err)
	}
	f.agents["device-a"] = replacement
	t.Cleanup(replacement.Close)
	f.restart()
	client = f.client("client")
	if out := call(t, client, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}); out.IsError {
		t.Fatal("new Agent epoch did not clear fence", out)
	}
}

func Test_IngressPTYJournalFailurePreventsSideEffect(t *testing.T) {
	f := newBudgetFixture(t, 1)
	client := f.client("client")
	// Renaming the journal over a directory fails after writing its temporary file.
	if err := os.Mkdir(f.path, 0700); err != nil {
		t.Fatal(err)
	}
	requireBudgetRule(t, call(t, client, "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}), "quota.unavailable")
	if len(f.agents["device-a"].PTY.List()) != 0 {
		t.Fatal("journal failure created terminal")
	}
}

func Test_IngressPTYAdmissionUsesSnapshotConnection(t *testing.T) {
	f := newBudgetFixture(t, 2)
	client := f.client("client")
	if out := call(t, client, "device-a/pty.list", map[string]any{}); out.IsError {
		t.Fatal(out)
	}
	f.g.mu.Lock()
	var current *view
	for v := range f.g.views {
		current = v
	}
	f.g.mu.Unlock()
	current.mu.Lock()
	connection := current.down["device-a"].bridge
	current.mu.Unlock()
	invoked := false
	out, err := f.g.opts.PTYBudget.call(context.Background(), current, f.peers[0], "pty.open", nil, func(bridge *mcpmux.Bridge) (*mcp.CallToolResult, error) {
		invoked = true
		if bridge != connection {
			t.Fatal("invoked a different connection from the snapshot")
		}
		return agent.Result(map[string]any{}, nil), nil
	})
	if err != nil || out.IsError || !invoked {
		t.Fatal(out, err, invoked)
	}
	// A queued caller's old address must not select a newer Agent snapshot.
	stale := f.peers[0]
	stale.Address = "http://stale.invalid/mcp"
	invoked = false
	out, err = f.g.opts.PTYBudget.call(context.Background(), current, stale, "pty.open", nil, func(*mcpmux.Bridge) (*mcp.CallToolResult, error) {
		invoked = true
		return nil, nil
	})
	if err != nil || invoked {
		t.Fatal("stale target invoked", err)
	}
	requireBudgetRule(t, out, "quota.unavailable")
}
