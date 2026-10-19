package connectivity

import (
	"context"
	"encoding/json"
	"errors"
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
