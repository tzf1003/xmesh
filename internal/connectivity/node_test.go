package connectivity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/devicelink"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

func node(t *testing.T, name string) (*Node, *config.Config) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"schema_version": 2, "node": map[string]any{"name": name, "roles": []string{"agent"}}, "mesh": map[string]any{}, "agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "authz": map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}}}
	raw, _ := json.Marshal(doc)
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	n, err := open(t.Context(), cfg, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if err := n.directory.SetProvider(device.LAN, 1, true); err != nil {
		t.Fatal(err)
	}
	return n, cfg
}

func serveNode(t *testing.T, n *Node) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(n.Handler(device.LAN))
	s.TLS = n.pairing.ServerTLS()
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

func TestUnifiedPairingConnectsToRealAgentAndKeepsCallerOnReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a, _ := node(t, "client")
	b, cfg := node(t, "workstation")
	ag, err := agent.New(config.Static(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ag.Close() })
	b.SetAgent(func(ctx context.Context, p identity.Principal, transport mcp.Transport) error {
		_, err := ag.ConnectDevice(ctx, p, transport)
		return err
	})
	server := serveNode(t, b)
	if err := b.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PairWith(ctx, device.LAN, server.URL, "correct horse battery", b.identity.ID); err != nil {
		t.Fatal(err)
	}
	if candidates := a.Candidates(); len(candidates) != 1 || candidates[0].DeviceID != b.identity.ID || candidates[0].Address != server.URL {
		t.Fatal("manual pairing address not retained", candidates)
	}
	peers, err := a.Peers(ctx)
	if err != nil || len(peers) != 1 || peers[0].ID != b.identity.ID {
		t.Fatal(peers, err)
	}
	transport, err := a.Transport(ctx, mesh.Peer{ID: b.identity.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "2"}, nil)
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args any) map[string]any {
		t.Helper()
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || out.IsError {
			t.Fatal(name, out, err)
		}
		raw, _ := json.Marshal(out.StructuredContent)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		return body
	}
	opened := call("pty.open", map[string]any{"cols": 80, "rows": 24})
	before := call("pty.list", map[string]any{})
	caller := before["caller_id"]
	// Replacing the physical carrier keeps the same Agent MCP instance and PTY owner.
	a.links.DisablePath(device.LAN)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.links.Activity(b.identity.ID).Epoch >= 2 && a.links.Activity(b.identity.ID).State == "online" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	after := call("pty.list", map[string]any{})
	if after["caller_id"] != caller {
		t.Fatal("caller changed", caller, after)
	}
	list, _ := after["sessions"].([]any)
	if len(list) != 1 {
		t.Fatal("PTY lost", opened, after)
	}
	s := list[0].(map[string]any)
	if s["owner"] != caller {
		t.Fatal("PTY owner changed", caller, s)
	}
	if err := b.trust.Revoke(ctx, a.identity.ID); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		if transport.(*devicelink.Transport).Session.Closed() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("revocation did not close the logical session")
}

func TestRuntimeLockRejectsConcurrentService(t *testing.T) {
	n, cfg := node(t, "one")
	if other, err := open(t.Context(), cfg, n.stateDir, false); err == nil {
		_ = other.Close()
		t.Fatal("two instances share device identity state")
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := open(t.Context(), cfg, n.stateDir, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
}

func TestProviderRetirementFencesLateDiscoveryBeforeRetry(t *testing.T) {
	n, _ := node(t, "local")
	remote, _ := node(t, "remote")
	if err := n.trust.Grant(t.Context(), remote.identity.Document("remote"), []string{"tag:peer"}, nil); err != nil {
		t.Fatal(err)
	}
	p := &provider{generation: 1, ready: true}
	n.providers[device.LAN] = p
	n.retireProvider(device.LAN, p)
	if generation, enabled := n.directory.Provider(device.LAN); generation != 2 || enabled || p.ready {
		t.Fatal("provider remained enabled while awaiting retry", generation, enabled, p.ready)
	}
	trusted, _ := n.trust.Find(remote.identity.ID)
	now := time.Now()
	path := device.Path{Kind: device.LAN, Address: "https://192.168.1.1:7433", BootID: "remote-boot", ProviderGeneration: 1, VerifiedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := n.directory.Observe(remote.identity.Certificate.Leaf, trusted.Generation, path); !errors.Is(err, device.ErrStalePath) {
		t.Fatal("late probe survived provider retirement", err)
	}
	n.retireProvider(device.LAN, p)
	if p.generation != 2 {
		t.Fatal("retirement repeated", p.generation)
	}
}

func TestProbeSchedulingSeparatesDevicesAfterAddressReuse(t *testing.T) {
	n, _ := node(t, "local")
	n.cancel()
	n.workers.Wait()
	a := Candidate{DeviceID: "device-a", Kind: device.LAN, Address: "https://192.168.1.2:7433"}
	b := a
	b.DeviceID = "device-b"
	n.mu.Lock()
	n.probing[probeKey(a)] = true
	n.mu.Unlock()
	n.enqueueProbe(b)
	n.mu.Lock()
	queued := n.probing[probeKey(b)]
	n.mu.Unlock()
	if !queued {
		t.Fatal("old device's probe suppressed the current occupant")
	}
}

func TestRetiredListenerRejectsRequestsAfterReplacementIsReady(t *testing.T) {
	n, _ := node(t, "local")
	old := n.handler(device.LAN, 1)
	if err := n.directory.SetProvider(device.LAN, 2, true); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{device.PairPath + "identity", devicelink.LinkPath} {
		response := httptest.NewRecorder()
		old.ServeHTTP(response, httptest.NewRequest("POST", route, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatal("retired listener served request", route, response.Code)
		}
	}
}

func TestRemoteDrainUsesDeviceTrustAndPersistentTargetID(t *testing.T) {
	a, _ := node(t, "admin")
	b, cfg := node(t, "workstation")
	ag, err := agent.New(config.Static(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ag.Close)
	ag.SetNetwork("lan", identity.Principal{Node: b.identity.ID})
	b.SetAdmin(ag.Control(b.Authenticate, nil))
	server := serveNode(t, b)
	if err := b.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PairWith(t.Context(), device.LAN, server.URL, "correct horse battery", b.identity.ID); err != nil {
		t.Fatal(err)
	}
	peer := mesh.Peer{ID: b.identity.ID, Name: "local-alias"}
	req := agent.DrainRequest{Scope: "device", Device: peer.ID, Drain: "keep"}
	if err := a.SendDrain(t.Context(), peer, req); err == nil || ag.Draining() {
		t.Fatal("network reachability granted administration", err)
	}
	if err := b.trust.Grant(t.Context(), a.identity.Document("admin"), []string{"tag:admin"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.SendDrain(t.Context(), peer, req); err != nil || !ag.Draining() {
		t.Fatal("paired administrator could not drain by device ID", err)
	}
	ag.Resume()
	if err := b.trust.Revoke(t.Context(), a.identity.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.SendDrain(t.Context(), peer, req); err == nil || ag.Draining() {
		t.Fatal("revoked administrator drained device", err)
	}
}

func TestSameNameDevicesKeepSeparateRoutingIdentity(t *testing.T) {
	local, _ := node(t, "workstation")
	remote, _ := node(t, "workstation")
	if err := local.trust.Grant(t.Context(), remote.identity.Document("workstation"), []string{"tag:peer", "tag:device"}, nil); err != nil {
		t.Fatal(err)
	}
	peers, err := local.Peers(t.Context())
	if err != nil || len(peers) != 1 {
		t.Fatal(peers, err)
	}
	p := peers[0]
	if p.ID != remote.identity.ID || p.Name == local.name || p.DisplayName != local.name || len(p.Name) > 63 {
		t.Fatalf("same-name device lost its route: %+v", p)
	}
	again, err := local.Peers(t.Context())
	if err != nil || again[0].Name != p.Name {
		t.Fatal("route changed", again, err)
	}
	aliases, err := routeAliases([]device.Device{
		{DeviceID: remote.identity.ID, Alias: "workstation"},
		{DeviceID: local.identity.ID, Alias: p.Name},
	}, "workstation")
	if err != nil || aliases[remote.identity.ID] == p.Name || aliases[local.identity.ID] != p.Name {
		t.Fatal("route alias collision", aliases, err)
	}
}

func TestFunnelWaitsForProviderAndClosesWhilePending(t *testing.T) {
	n, cfg := node(t, "funnel-node")
	cfg.Mesh.Tailnet.Enabled = true
	n.Reconfigure(cfg.Mesh)
	listener, err := n.Funnel()
	if err != nil {
		t.Fatal("Funnel rejected normal provider startup", err)
	}
	accepted := make(chan error, 1)
	go func() { _, err := listener.Accept(); accepted <- err }()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("closed listener accepted a connection")
		}
	case <-time.After(time.Second):
		t.Fatal("pending Funnel listener did not stop")
	}
}

func TestNetworkSettleReportsProviderApplyErrors(t *testing.T) {
	n, cfg := node(t, "settle-node")
	n.mu.Lock()
	n.providers[device.LAN] = &provider{cfg: cfg.Mesh, err: "LAN interface is unavailable"}
	n.mu.Unlock()
	defer func() { n.mu.Lock(); delete(n.providers, device.LAN); n.mu.Unlock() }()
	problems := n.Settle(t.Context())
	if len(problems) != 1 || problems[0].Section != "mesh.lan" || problems[0].Message != "LAN interface is unavailable" {
		t.Fatal(problems)
	}
	n.mu.Lock()
	n.providers[device.LAN].ready = true
	n.providers[device.LAN].err = ""
	n.mu.Unlock()
	if problems := n.Settle(t.Context()); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestSettleWaitsForDisabledPathAndReportsFunnelErrors(t *testing.T) {
	n, cfg := node(t, "settle-disabled")
	before := cfg.Mesh
	disabled := false
	before.LAN.Enabled = &disabled
	before.Tailnet.Enabled = true
	n.mu.Lock()
	n.cfg = before
	n.providers[device.Tailnet] = &provider{cfg: before, ready: true}
	n.mu.Unlock()
	defer func() { n.mu.Lock(); delete(n.providers, device.Tailnet); n.listeners = nil; n.mu.Unlock() }()
	next := before
	next.Tailnet.Enabled = false
	n.Reconfigure(next)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	problems := n.Settle(ctx)
	if len(problems) != 1 || problems[0].Section != "mesh.tailnet" {
		t.Fatal("path disable reported before the provider stopped", problems)
	}
	n.mu.Lock()
	n.providers[device.Tailnet] = &provider{cfg: next}
	listener := &portListener{funnel: true, err: "Funnel permission denied"}
	n.listeners = map[*portListener]bool{listener: true}
	n.mu.Unlock()
	problems = n.Settle(t.Context())
	if len(problems) != 1 || problems[0].Section != "ingress" || len(n.ListenerErrors()) != 1 {
		t.Fatal("Funnel failure hidden", problems)
	}
	n.mu.Lock()
	listener.ready = true
	listener.err = ""
	n.mu.Unlock()
	if problems := n.Settle(t.Context()); len(problems) != 0 || len(n.ListenerErrors()) != 0 {
		t.Fatal("recovered listener remains degraded", problems)
	}
	n.mu.Lock()
	n.cfg.Tailnet.Enabled = true
	n.providers[device.Tailnet] = &provider{cfg: n.cfg, ready: true}
	listener.funnel, listener.ready, listener.err = false, false, "Tailnet gateway port occupied"
	n.mu.Unlock()
	problems = n.Settle(t.Context())
	if len(problems) != 1 || problems[0].Section != "gateway" || len(n.ListenerErrors()) != 1 {
		t.Fatal("gateway listener error hidden", problems)
	}

}

func TestOAuthDeviceSessionUsesOnlyTargetIngressGrant(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	clientNode, _ := node(t, "ingress-client")
	target, cfg := node(t, "target")
	cfg.Agent.Authz = map[string]config.Grant{
		"tag:peer":    {Tools: []string{"*"}, Backends: []string{"*"}},
		"tag:ingress": {Tools: []string{"sys.info", "fs.stat"}},
	}
	ag, err := agent.New(config.Static(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ag.Close)
	principals := make(chan identity.Principal, 1)
	target.SetAgent(func(ctx context.Context, p identity.Principal, transport mcp.Transport) error {
		principals <- p
		_, err := ag.ConnectDevice(ctx, p, transport)
		return err
	})
	server := serveNode(t, target)
	if err := target.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := clientNode.PairWith(ctx, device.LAN, server.URL, "correct horse battery", target.identity.ID); err != nil {
		t.Fatal(err)
	}
	if err := target.trust.Grant(ctx, clientNode.identity.Document("ingress-client"), []string{"tag:peer", "tag:device", "tag:ingress"}, nil); err != nil {
		t.Fatal(err)
	}
	transport, err := clientNode.Transport(ctx, mesh.Peer{ID: target.identity.ID}, "oauth-client")
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "oauth", Version: "2"}, nil)
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	p := <-principals
	if p.OAuthClient != "oauth-client" || len(p.Tags) != 1 || p.Tags[0] != "tag:ingress" {
		t.Fatal(p)
	}
	caller := identity.Caller{ID: p.Node + "/" + session.ID(), Tags: p.Tags, OAuthClient: p.OAuthClient}
	if agent.Allowed(cfg, caller, "exec.run", "") || !agent.Allowed(cfg, caller, "fs.stat", "") {
		t.Fatal("delegated grant boundary failed", caller)
	}
	allowed, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "sys.info", Arguments: map[string]any{}})
	if err != nil || allowed.IsError {
		t.Fatal("legitimate ingress tool failed", allowed, err)
	}
	denied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "exec.run", Arguments: map[string]any{"argv": []string{"must-not-run"}}})
	if err == nil && !denied.IsError {
		t.Fatal("peer-only tool reached delegated caller", denied)
	}
}

func TestDeviceDrainUsesCallerDeadlineForResponse(t *testing.T) {
	a, _ := node(t, "admin")
	b, _ := node(t, "drain-target")
	server := serveNode(t, b)
	if err := b.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PairWith(t.Context(), device.LAN, server.URL, "correct horse battery", b.identity.ID); err != nil {
		t.Fatal(err)
	}
	b.SetAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2200 * time.Millisecond):
			w.WriteHeader(200)
		case <-r.Context().Done():
		}
	}))
	// Pairing can be slow under race instrumentation; start the response budget
	// after setup, while still waiting past the LAN connection budget.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := a.SendDrain(ctx, mesh.Peer{ID: b.identity.ID}, agent.DrainRequest{Scope: "device", Device: b.identity.ID, Drain: "keep"}); err != nil {
		t.Fatal("LAN response cut off before caller deadline", err)
	}
	short, stop := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer stop()
	if err := a.SendDrain(short, mesh.Peer{ID: b.identity.ID}, agent.DrainRequest{Scope: "mesh", Drain: "keep"}); err == nil {
		t.Fatal("caller deadline ignored")
	}
}
