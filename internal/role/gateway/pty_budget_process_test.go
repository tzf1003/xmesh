//go:build !windows

package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mesh"
)

type processBudgetConfig struct {
	Path  string
	Peers []mesh.Peer
}

func TestIngressProcessHelper(t *testing.T) {
	body := os.Getenv("XMESH_INGRESS_PROCESS_HELPER")
	if body == "" {
		return
	}
	var c processBudgetConfig
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	budget, err := NewPTYBudget(3, c.Path)
	if err != nil {
		t.Fatal(err)
	}
	g := New(Options{NodeName: "ingress", Ingress: true, LocalPeer: c.Peers[0], Dial: RemoteDial(http.DefaultClient), PTYBudget: budget})
	defer g.Close()
	g.Update(c.Peers[1:])
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
		return Principal{ClientID: "shared-client", Scopes: []string{"pty:*", "sys:*"}}, nil
	}))
	defer server.Close()
	fmt.Println(server.URL)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func startIngressProcess(t *testing.T, c processBudgetConfig) string {
	t.Helper()
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestIngressProcessHelper$")
	cmd.Env = append(os.Environ(), "XMESH_INGRESS_PROCESS_HELPER="+string(body))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- strings.TrimSpace(line) }()
	select {
	case url := <-ready:
		if !strings.HasPrefix(url, "http://127.0.0.1:") {
			t.Fatal("invalid ingress readiness", url)
		}
		return url + "/mcp"
	case <-time.After(10 * time.Second):
		t.Fatal("ingress process did not start")
		return ""
	}
}

func Test_IngressPTYConcurrentAcrossProcesses(t *testing.T) {
	f := newBudgetFixture(t, 3)
	peers := append([]mesh.Peer(nil), f.peers...)
	for i, peer := range peers {
		server := httptest.NewServer(f.agents[peer.Name].HTTP(func(r *http.Request) (identity.Principal, error) {
			return identity.Principal{Node: "ingress-node", Tags: []string{"tag:ingress"}, OAuthClient: r.Header.Get("X-Xmesh-Oauth-Client")}, nil
		}))
		t.Cleanup(server.Close)
		peers[i].Address = server.URL + "/mcp"
	}
	c := processBudgetConfig{Path: f.path, Peers: peers}
	one := connect(t, startIngressProcess(t, c))
	two := connect(t, startIngressProcess(t, c))
	type result struct {
		out *mcp.CallToolResult
		err error
	}
	results := make(chan result, 12)
	var workers sync.WaitGroup
	for i := range 12 {
		workers.Go(func() {
			client, device := one, "device-a"
			if i%2 != 0 {
				client, device = two, "device-b"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: device + "/pty.open", Arguments: map[string]any{"cols": 80, "rows": 24}})
			results <- result{out, err}
		})
	}
	workers.Wait()
	close(results)
	succeeded := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.out.IsError {
			requireBudgetRule(t, result.out, "quota.exceeded")
		} else {
			succeeded++
		}
	}
	actual := 0
	for _, a := range f.agents {
		actual += a.PTY.BudgetSnapshot().Counts["oauth:shared-client"]
	}
	if succeeded != 3 || actual != 3 {
		t.Fatal("cross-process admitted and actual terminals", succeeded, actual)
	}
}

func Test_IngressPTYSharedJournalReloadsPendingAndLimit(t *testing.T) {
	f := newBudgetFixture(t, 2)
	other := *f
	other.g = nil
	other.restart()
	client := f.client("client")
	second := other.client("client")
	if out := call(t, client, "device-a/pty.open", map[string]any{"cols": 80, "rows": 24}); out.IsError {
		t.Fatal(out)
	}
	if _, err := NewPTYBudget(3, f.path); err == nil {
		t.Fatal("inconsistent shared limit accepted")
	}
	var state budgetState
	body, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	peer := state.Peers["device-a"]
	peer.Pending = true
	state.Peers["device-a"] = peer
	if err := writeBudgetState(f.path, state); err != nil {
		t.Fatal(err)
	}
	requireBudgetRule(t, call(t, second, "device-b/pty.open", map[string]any{"cols": 80, "rows": 24}), "quota.unavailable")
	if len(f.agents["device-b"].PTY.List()) != 0 {
		t.Fatal("stale journal bypassed pending operation")
	}
}
