package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/pty"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

// authorityTestTTL is the lease length of the Agent-hosted authority in these tests. It is deliberately
// generous: a 1s lease leaves under a second of usable time after the safety margin, which a loaded
// Windows runner running with -race can spend before the first call has even been dispatched.
const authorityTestTTL = 4 * time.Second

// twoIngress returns the fixture's ingress plus a second one that has its own
// budget file, node identity and OAuth sessions, as a separate host would.
func twoIngress(t *testing.T, limit int) (*budgetFixture, *Gateway) {
	t.Helper()
	f := newBudgetFixture(t, limit)
	f.authority = "device-b"
	f.agents["device-b"].BudgetAuthority = pty.NewBudgetAuthorityTTL(authorityTestTTL)
	f.restart()
	return f, f.second()
}

func (f *budgetFixture) second() *Gateway {
	f.t.Helper()
	opts := f.opts
	opts.NodeName = "ingress-2"
	opts.Dial = func(ctx context.Context, peer mesh.Peer, p Principal, o mcpmux.Options) (*mcpmux.Bridge, error) {
		transport, err := f.agents[peer.Name].Transport(ctx, identity.Principal{Node: "ingress-node-2", Tags: []string{"tag:ingress"}, OAuthClient: p.ClientID})
		if err != nil {
			return nil, err
		}
		return mcpmux.Connect(ctx, transport, o)
	}
	var err error
	if opts.PTYBudget, err = NewPTYBudget(f.limit, filepath.Join(f.t.TempDir(), "ingress-pty.json")); err != nil {
		f.t.Fatal(err)
	}
	if err := opts.PTYBudget.UseAuthority(f.authority); err != nil {
		f.t.Fatal(err)
	}
	g := New(opts)
	g.Update(f.peers[1:])
	f.t.Cleanup(g.Close)
	return g
}

func (f *budgetFixture) clientOn(g *Gateway, id string) *mcp.ClientSession {
	f.t.Helper()
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
		return Principal{ClientID: id, Scopes: []string{"pty:*", "sys:*"}}, nil
	}))
	f.t.Cleanup(server.Close)
	return connect(f.t, server.URL+"/mcp")
}

func Test_IngressPTYCrossHostAuthorityAdmitsOnce(t *testing.T) {
	f, other := twoIngress(t, 3)
	clients := []*mcp.ClientSession{f.client("same-client"), f.clientOn(other, "same-client")}
	results := make(chan *mcp.CallToolResult, 16)
	var workers sync.WaitGroup
	for i := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			device := "device-a"
			if i%3 == 0 {
				device = "device-b"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := clients[i%2].CallTool(ctx, &mcp.CallToolParams{Name: device + "/pty.open", Arguments: map[string]any{"cols": 80, "rows": 24}})
			if err != nil {
				t.Error(err)
				return
			}
			results <- out
		}()
	}
	workers.Wait()
	close(results)
	succeeded := 0
	for out := range results {
		if !out.IsError {
			succeeded++
		} else {
			requireBudgetRule(t, out, "quota.exceeded")
		}
	}
	actual := 0
	for _, a := range f.agents {
		actual += a.PTY.BudgetSnapshot().Counts["oauth:same-client"]
	}
	if succeeded != 3 || actual != 3 {
		t.Fatal("admitted and actual terminals across two ingress hosts", succeeded, actual)
	}
}

func Test_IngressPTYAuthorityOfflineFailsClosed(t *testing.T) {
	f, other := twoIngress(t, 3)
	client := f.clientOn(other, "client")
	// device-b hosts the lease; with only device-a online nothing is admitted.
	other.Update(nil)
	f.g.Update(nil)
	requireBudgetRule(t, call(t, client, "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}), "quota.unavailable")
	if len(f.agents["device-a"].PTY.List()) != 0 {
		t.Fatal("terminal created without the budget authority")
	}
	other.Update(f.peers[1:])
	if out := call(t, client, "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}); out.IsError {
		t.Fatal("returning authority did not restore admission", out)
	}
}

func Test_IngressPTYAuthorityDisabledWhenNotConfigured(t *testing.T) {
	f := newBudgetFixture(t, 1)
	if f.opts.PTYBudget.authority != "" {
		t.Fatal("authority enabled by default")
	}
	if out := call(t, f.client("client"), "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}); out.IsError {
		t.Fatal(out)
	}
}

func Test_IngressPTYUncertainCallFencesOtherHostUntilAgentRestart(t *testing.T) {
	f, other := twoIngress(t, 3)
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
	_, err := f.g.opts.PTYBudget.call(context.Background(), view, f.peers[0], "pty.open", nil, func(context.Context, *mcpmux.Bridge) (*mcp.CallToolResult, error) {
		return nil, errors.New("transport ended before result")
	})
	if err == nil {
		t.Fatal("lost transport result was hidden")
	}
	// The second host has no local record of the call, only the authority's.
	second := f.clientOn(other, "client")
	requireBudgetRule(t, call(t, second, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}), "quota.unavailable")
	f.g.Close()
	other.Close()
	f.agents["device-a"].Close()
	replacement, err := agent.New(config.Static(f.configs["device-a"]))
	if err != nil {
		t.Fatal(err)
	}
	f.agents["device-a"] = replacement
	t.Cleanup(replacement.Close)
	f.restart()
	other = f.second()
	second = f.clientOn(other, "client")
	if out := call(t, second, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}); out.IsError {
		t.Fatal("new Agent epoch did not clear the cross-host record", out)
	}
}

func Test_IngressPTYLeaseDeadlineBoundsTheCall(t *testing.T) {
	f, _ := twoIngress(t, 3)
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
	var deadline, invoked time.Time
	_, err := f.g.opts.PTYBudget.call(context.Background(), view, f.peers[0], "pty.open", nil, func(ctx context.Context, _ *mcpmux.Bridge) (*mcp.CallToolResult, error) {
		var ok bool
		invoked = time.Now()
		if deadline, ok = ctx.Deadline(); !ok {
			t.Fatal("call is not bounded by the lease")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err == nil {
		t.Fatal("expired call reported success")
	}
	// The lease starts when the authority grants it, before the call is invoked, so on a slow runner
	// (Windows with -race) the remaining time can already be zero or negative; it must never exceed the lease.
	if remaining := deadline.Sub(invoked); remaining >= authorityTestTTL {
		t.Fatal("call is not bounded by the lease", authorityTestTTL, remaining)
	}
}
