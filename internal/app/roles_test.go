package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
	"go.yaml.in/yaml/v3"
)

// node is a running kernel in local mode with a configuration file the test edits.
type hotNode struct {
	t         *testing.T
	root      string
	state     string
	path      string
	agentAddr string
}

func startHotNode(t *testing.T, roles []string, gatewayListen string) *hotNode {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	n := &hotNode{t: t, root: root, state: filepath.Join(root, "state"), path: filepath.Join(root, "config.json"), agentAddr: address(t)}
	doc := map[string]any{"node": map[string]any{"name": "local-node", "roles": roles}, "mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}}, "gateway": map[string]any{"listen": gatewayListen}, "agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "authz": map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}}}
	b, _ := json.Marshal(doc)
	if e := os.WriteFile(n.path, b, 0600); e != nil {
		t.Fatal(e)
	}
	store, e := config.NewStore(n.path)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, store, Options{Local: true, Listen: n.agentAddr, StateDir: n.state})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(15 * time.Second):
			t.Error("runtime shutdown hung")
		}
	})
	return n
}

// edit rewrites the configuration file; the kernel notices it by itself.
func (n *hotNode) edit(change func(doc map[string]any)) {
	n.t.Helper()
	raw, e := os.ReadFile(n.path)
	if e != nil {
		n.t.Fatal(e)
	}
	var doc map[string]any
	if e = yaml.Unmarshal(raw, &doc); e != nil {
		n.t.Fatal(e)
	}
	change(doc)
	out, e := json.Marshal(doc)
	if e != nil {
		n.t.Fatal(e)
	}
	if e = os.WriteFile(n.path, out, 0600); e != nil {
		n.t.Fatal(e)
	}
}

// connect opens an MCP session at endpoint, retrying until the listener is up.
func (n *hotNode) connect(endpoint string) *mcp.ClientSession {
	n.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if s := n.tryConnect(endpoint); s != nil {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	n.t.Fatal("listener not ready", endpoint)
	return nil
}

func (n *hotNode) tryConnect(endpoint string) *mcp.ClientSession {
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	attempt, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	// The agent listener in local mode takes no token; the gateway needs the one the kernel wrote.
	httpClient := gatewayHTTPClient(n.state)
	if endpoint == "http://"+n.agentAddr+"/mcp" {
		httpClient = http.DefaultClient
	}
	s, e := client.Connect(attempt, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if e != nil {
		return nil
	}
	return s
}

func (n *hotNode) call(s *mcp.ClientSession, name string, args any) map[string]any {
	n.t.Helper()
	timeout := 10 * time.Second
	if os.PathSeparator == '\\' && name == "pty.open" {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	o, e := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil || o.IsError {
		n.t.Fatalf("%s %v %v", name, o, e)
	}
	b, _ := json.Marshal(o.StructuredContent)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	return v
}

// eventually polls until ok, which a hot change needs a moment to reach.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("not reached:", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func refusesConnections(address string) bool {
	c, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
	if err != nil {
		return true
	}
	_ = c.Close()
	return false
}

// A change of gateway.listen moves the running gateway to the new address. The agent and the PTY
// session on it are not touched, and the old address stops answering.
func Test_Runtime_GatewayMovesToNewListenAddress(t *testing.T) {
	first, second := address(t), address(t)
	n := startHotNode(t, []string{"agent", "gateway"}, first)
	direct := n.connect("http://" + n.agentAddr + "/mcp")
	defer direct.Close()
	opened := n.call(direct, "pty.open", map[string]any{"cols": 80, "rows": 24})
	gateway := n.connect("http://" + first + "/mcp")
	defer gateway.Close()
	n.call(gateway, "local/sys.info", map[string]any{})

	n.edit(func(doc map[string]any) { doc["gateway"].(map[string]any)["listen"] = second })
	moved := n.connect("http://" + second + "/mcp")
	defer moved.Close()
	n.call(moved, "local/sys.info", map[string]any{})
	eventually(t, "the old address stops accepting", func() bool { return refusesConnections(first) })

	list := n.call(direct, "pty.list", map[string]any{})
	sessions, _ := list["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("the PTY session did not survive the move: %v (opened %v)", list, opened)
	}
}

// node.roles can add and remove the gateway while the node runs; the agent carries on.
func Test_Runtime_GatewayRoleStartsAndStops(t *testing.T) {
	listen := address(t)
	n := startHotNode(t, []string{"agent"}, listen)
	direct := n.connect("http://" + n.agentAddr + "/mcp")
	defer direct.Close()
	if !refusesConnections(listen) {
		t.Fatal("a gateway is listening on a node without the role")
	}

	n.edit(func(doc map[string]any) { doc["node"].(map[string]any)["roles"] = []string{"agent", "gateway"} })
	gateway := n.connect("http://" + listen + "/mcp")
	n.call(gateway, "local/sys.info", map[string]any{})
	gateway.Close()
	if _, err := os.Stat(filepath.Join(n.state, "pats", "default.token")); err != nil {
		t.Fatal("the gateway started without a token:", err)
	}

	n.edit(func(doc map[string]any) { doc["node"].(map[string]any)["roles"] = []string{"agent"} })
	eventually(t, "the gateway stops accepting", func() bool { return refusesConnections(listen) })
	n.call(direct, "sys.info", map[string]any{})

	n.edit(func(doc map[string]any) { doc["node"].(map[string]any)["roles"] = []string{"agent", "gateway"} })
	again := n.connect("http://" + listen + "/mcp")
	defer again.Close()
	n.call(again, "local/sys.info", map[string]any{})
}

func gatewayDepsFor(t *testing.T, cfg *config.Config) gatewayDeps {
	t.Helper()
	return gatewayDeps{store: config.Static(cfg), registry: newGatewayRegistry(), mode: "lan"}
}

func parseConfig(t *testing.T, roles []string, listen string) *config.Config {
	t.Helper()
	doc := map[string]any{"node": map[string]any{"name": "role-node", "roles": roles}, "mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}}, "gateway": map[string]any{"listen": listen}, "agent": map[string]any{"cwd_policy": map[string]any{"allow": []string{}}}}
	b, _ := json.Marshal(doc)
	cfg, err := config.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// answers reports whether something serves HTTP at address. The gateway answers without a token with 401.
func answers(address string) bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + address + "/mcp")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// A move to an address that is taken fails and leaves the gateway where it was; once the address is
// free the same move works.
func Test_GatewayRoleKeepsItsAddressWhenTheMoveFails(t *testing.T) {
	first, taken := address(t), address(t)
	cfg := parseConfig(t, []string{"gateway"}, first)
	role, err := startGatewayRole(gatewayDepsFor(t, cfg), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(role.stop)
	blocker, err := net.Listen("tcp", taken)
	if err != nil {
		t.Fatal(err)
	}
	if err = role.rebind(taken); err == nil {
		t.Fatal("moved onto an address that is in use")
	}
	if !answers(first) || role.bindAddress() != first {
		t.Fatal("a failed move took the gateway down")
	}
	_ = blocker.Close()
	if err = role.rebind(taken); err != nil {
		t.Fatal(err)
	}
	if !answers(taken) || role.bindAddress() != taken {
		t.Fatal("the move did not happen")
	}
	eventually(t, "the old address stops accepting", func() bool { return refusesConnections(first) })
}

// 127.0.0.1:P becoming :P holds the same port, which some systems do not allow side by side. The
// move still happens, and the gateway answers on the port afterwards.
func Test_GatewayRoleWidensItsBindOnTheSamePort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	loopback := l.Addr().String()
	_ = l.Close()
	_, port, _ := net.SplitHostPort(loopback)
	cfg := parseConfig(t, []string{"gateway"}, loopback)
	role, err := startGatewayRole(gatewayDepsFor(t, cfg), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(role.stop)
	if err = role.rebind(":" + port); err != nil {
		t.Fatal(err)
	}
	if !answers(loopback) || role.bindAddress() != ":"+port {
		t.Fatal("the widened gateway does not answer")
	}
}

// The runtime reports what it could not apply, keeps the gateway where it was, and tries again when
// asked.
func Test_RoleRuntimeReportsAndRetriesAFailedMove(t *testing.T) {
	first, taken := address(t), address(t)
	cfg := parseConfig(t, []string{"gateway"}, first)
	rt := newRoleRuntime(gatewayDepsFor(t, cfg), t.TempDir(), func(e error) { t.Error(e) })
	if err := rt.startGateway(cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		rt.close()
	})
	rt.begin(ctx)

	blocker, err := net.Listen("tcp", taken)
	if err != nil {
		t.Fatal(err)
	}
	rt.submit(parseConfig(t, []string{"gateway"}, taken))
	problems := rt.Settle(ctx, false)
	if len(problems) != 1 || problems[0].Section != "gateway" || problems[0].Message == "" {
		t.Fatalf("problems = %+v", problems)
	}
	if listen, ok := rt.gatewayListen(); !ok || listen != first || !answers(first) {
		t.Fatalf("the gateway left its address: %q %v", listen, ok)
	}
	if rt.failureCount() != 1 {
		t.Fatalf("failures = %d", rt.failureCount())
	}
	// Asking again while the address is still taken keeps the report and does not count it again.
	if problems = rt.Settle(ctx, true); len(problems) != 1 || rt.failureCount() != 1 {
		t.Fatalf("problems = %+v, failures = %d", problems, rt.failureCount())
	}

	_ = blocker.Close()
	if problems = rt.Settle(ctx, true); len(problems) != 0 {
		t.Fatalf("a retry on a free address still fails: %+v", problems)
	}
	if listen, _ := rt.gatewayListen(); listen != taken || !answers(taken) {
		t.Fatalf("the gateway did not move: %q", listen)
	}
	if len(rt.problemMessages()) != 0 {
		t.Fatal("the report outlived the repair")
	}
}

func Test_GatewayRoleRebindsAfterListenerFailure(t *testing.T) {
	addr := address(t)
	cfg := parseConfig(t, []string{"gateway"}, addr)
	role, err := startGatewayRole(gatewayDepsFor(t, cfg), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(role.stop)
	role.mu.Lock()
	listener := role.bound[0]
	role.mu.Unlock()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "failed listener no longer serving", func() bool { return role.bindAddress() == "" })
	if err := role.rebind(addr); err != nil {
		t.Fatal(err)
	}
	if !answers(addr) || role.bindAddress() != addr {
		t.Fatal("same-address retry did not restore gateway")
	}
}

type partialGatewayNetwork struct {
	mesh.Network
	address     string
	unavailable atomic.Bool
	multiPath   bool
}

func (n *partialGatewayNetwork) MultiPath() bool { return n.multiPath }

func (n *partialGatewayNetwork) ListenPort(int) (net.Listener, error) {
	if n.unavailable.Load() {
		return nil, errors.New("tailnet listener unavailable")
	}
	return net.Listen("tcp", n.address)
}

func Test_GatewayRolePreservesPartialAvailabilityOnFailedRetry(t *testing.T) {
	for _, scenario := range []struct {
		failedEndpoint, mode string
		multiPath            bool
	}{
		{"os", "tailnet", false}, {"tailnet", "tailnet", false},
		{"os", "", true}, {"tailnet", "", true},
	} {
		t.Run(fmt.Sprintf("%s-%s-%t", scenario.failedEndpoint, scenario.mode, scenario.multiPath), func(t *testing.T) {
			osAddress, tailnetAddress := address(t), address(t)
			_, port, _ := net.SplitHostPort(osAddress)
			listen := "0.0.0.0:" + port
			cfg := parseConfig(t, []string{"gateway"}, listen)
			network := &partialGatewayNetwork{address: tailnetAddress, multiPath: scenario.multiPath}
			deps := gatewayDepsFor(t, cfg)
			deps.mode, deps.network = scenario.mode, network
			role, err := startGatewayRole(deps, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(role.stop)
			idx, surviving := 0, tailnetAddress
			if scenario.failedEndpoint == "tailnet" {
				idx, surviving = 1, osAddress
				network.unavailable.Store(true)
			}
			role.mu.Lock()
			failed := role.bound[idx]
			role.mu.Unlock()
			_ = failed.Close()
			eventually(t, "one surviving listener", func() bool {
				role.mu.Lock()
				defer role.mu.Unlock()
				return role.failed && len(role.bound) == 1
			})
			var blocker net.Listener
			if scenario.failedEndpoint == "os" {
				blocker, err = net.Listen("tcp", listen)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = blocker.Close() })
			}
			taken := address(t)
			other, err := net.Listen("tcp", taken)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if err = role.rebind(taken); err == nil || !answers(surviving) {
				t.Fatal("failed move discarded the surviving listener", err)
			}
			if err = role.rebind(listen); err == nil || !answers(surviving) {
				t.Fatal("failed same-address retry discarded the surviving listener", err)
			}
			if blocker != nil {
				_ = blocker.Close()
			}
			network.unavailable.Store(false)
			if err = role.rebind(listen); err != nil || !answers(osAddress) || !answers(tailnetAddress) {
				t.Fatal("retry did not restore both listeners", err)
			}
		})
	}
}
