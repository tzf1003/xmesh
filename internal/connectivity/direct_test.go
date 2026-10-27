package connectivity

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

func TestDirectPathCarriesDeviceLinkAndGuardsPairing(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
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
	for _, pair := range [][2]*Node{{a, b}, {b, a}} {
		if err := pair[0].trust.Grant(ctx, pair[1].identity.Document("device"), []string{"tag:peer", "tag:device"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listen := freeAddr(t)
	b.cfg.Direct = config.Direct{Enabled: true, Listen: listen}
	if err := b.directory.SetProvider(device.Direct, 1, true); err != nil {
		t.Fatal(err)
	}
	pb := &provider{generation: 1, cfg: b.cfg, done: make(chan struct{})}
	go func() { defer close(pb.done); _ = b.runDirect(ctx, pb) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b.mu.Lock()
		ready := pb.ready
		b.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("direct listener not ready")
		}
	}
	a.cfg.Direct = config.Direct{Enabled: true, Peers: map[string]string{b.identity.ID: listen}}
	if err := a.directory.SetProvider(device.Direct, 1, true); err != nil {
		t.Fatal(err)
	}
	candidates := a.Candidates()
	if len(candidates) != 1 || candidates[0].Kind != device.Direct || candidates[0].Address != "https://"+listen {
		t.Fatal("configured direct peer is not a candidate", candidates)
	}
	a.probe(ctx, candidates[0])
	devices, _ := a.directory.Snapshot(time.Now())
	if len(devices) != 1 || len(devices[0].Paths) != 1 || !devices[0].Paths[0].Reachable || devices[0].Paths[0].Kind != device.Direct {
		t.Fatal("direct path was not verified", devices)
	}
	transport, err := a.Transport(ctx, mesh.Peer{ID: b.identity.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "2"}, nil).Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "pty.list", Arguments: map[string]any{}}); err != nil || out.IsError {
		t.Fatal(out, err)
	}
	if got := a.links.Activity(b.identity.ID).Path.Kind; got != device.Direct {
		t.Fatal("link did not use the direct path", got)
	}

	// An unpaired device cannot pair on the public port until a connection password is set (an open invite).
	c, _ := node(t, "stranger")
	if err := c.directory.SetProvider(device.Direct, 1, true); err != nil {
		t.Fatal(err)
	}
	address := "https://" + listen
	if _, err := c.PairWith(ctx, device.Direct, address, "correct horse battery", b.identity.ID); err == nil {
		t.Fatal("pairing succeeded on the public port with no open invite")
	}
	if err := b.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PairWith(ctx, device.Direct, address, "wrong horse battery", b.identity.ID); err == nil {
		t.Fatal("pairing succeeded with the wrong password")
	}
	if _, err := c.PairWith(ctx, device.Direct, address, "correct horse battery", b.identity.ID); err != nil {
		t.Fatal("pairing failed while an invite was open:", err)
	}
	if _, ok := b.trust.Find(c.identity.ID); !ok {
		t.Fatal("the paired device is not trusted")
	}
	// Pairing remembers the address as the peer's direct path.
	trusted, _ := c.trust.Find(b.identity.ID)
	if len(trusted.Hints) != 1 || trusted.Hints[0].Kind != device.Direct || trusted.Hints[0].Address != address {
		t.Fatal("the pairing address was not remembered", trusted.Hints)
	}
}

func TestDirectProviderSameIgnoresPeers(t *testing.T) {
	var old, next config.Mesh
	old.Direct = config.Direct{Enabled: true, Listen: "0.0.0.0:7435"}
	next.Direct = config.Direct{Enabled: true, Listen: "0.0.0.0:7435", Advertise: "x.example.com:7435", Peers: map[string]string{"dev-a": "1.2.3.4:5"}}
	if !providerSame(old, next, device.Direct) {
		t.Fatal("peers and advertise must not rebuild the listener")
	}
	next.Direct.Listen = "0.0.0.0:9000"
	if providerSame(old, next, device.Direct) {
		t.Fatal("a new listen address must rebuild the listener")
	}
	if providerEnabled(config.Mesh{}, device.Direct) {
		t.Fatal("direct must be off by default")
	}
}
