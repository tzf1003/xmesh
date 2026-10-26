package connectivity

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

// fakeEdge behaves like Cloudflare in front of a tunnel: it checks the Access service token, accepts a
// WebSocket and forwards its bytes to the tunnel's local listener, as cloudflared does for tcp:// rules.
func fakeEdge(t *testing.T, backend string, seen *atomic.Int32) *httptest.Server {
	t.Helper()
	edge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("CF-Access-Client-Id") != "id.access" || r.Header.Get("CF-Access-Client-Secret") != "s3cret" {
			http.Error(w, "forbidden", 403)
			return
		}
		seen.Add(1)
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		client := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
		upstream, err := net.Dial("tcp", backend)
		if err != nil {
			_ = client.Close()
			return
		}
		go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
	}))
	t.Cleanup(edge.Close)
	return edge
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestCloudflarePathCarriesDeviceLinkThroughTunnel(t *testing.T) {
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
	// b serves the tunnel's local end.
	listen := freeAddr(t)
	b.cfg.Cloudflare = config.Cloudflare{Enabled: true, Hostname: "b.example.com", Listen: listen}
	if err := b.directory.SetProvider(device.Cloudflare, 1, true); err != nil {
		t.Fatal(err)
	}
	pb := &provider{generation: 1, cfg: b.cfg, done: make(chan struct{})}
	go func() { defer close(pb.done); _ = b.runCloudflare(ctx, pb) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b.mu.Lock()
		ready := pb.ready
		b.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tunnel listener not ready")
		}
	}
	var seen atomic.Int32
	edge := fakeEdge(t, listen, &seen)
	host := edge.Listener.Addr().String()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("s3cret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// a only dials: no hostname of its own, the peer's hostname comes from configuration.
	a.cfg.Cloudflare = config.Cloudflare{Enabled: true, AccessClientID: "id.access", AccessClientSecretFile: secret, Peers: map[string]string{b.identity.ID: host}}
	a.cloudflareOuter = edge.Client()
	if err := a.directory.SetProvider(device.Cloudflare, 1, true); err != nil {
		t.Fatal(err)
	}
	candidates := a.Candidates()
	if len(candidates) != 1 || candidates[0].Kind != device.Cloudflare || candidates[0].Address != "https://"+host {
		t.Fatal("configured tunnel peer is not a candidate", candidates)
	}
	a.probe(ctx, candidates[0])
	devices, _ := a.directory.Snapshot(time.Now())
	if len(devices) != 1 || len(devices[0].Paths) != 1 || !devices[0].Paths[0].Reachable || devices[0].Paths[0].Kind != device.Cloudflare {
		t.Fatal("tunnel path was not verified", devices)
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
	if got := a.links.Activity(b.identity.ID).Path.Kind; got != device.Cloudflare {
		t.Fatal("link did not use the tunnel", got)
	}
	if seen.Load() == 0 {
		t.Fatal("edge never saw the service token")
	}

	// Without the service token the edge refuses before any device protocol runs.
	a.cfg.Cloudflare.AccessClientID, a.cfg.Cloudflare.AccessClientSecretFile = "", ""
	before := seen.Load()
	a.probe(ctx, candidates[0])
	if seen.Load() != before {
		t.Fatal("edge accepted a request without the service token")
	}

	// An unpaired device cannot pair through the tunnel until a connection password is set (an open invite).
	c, _ := node(t, "stranger")
	c.cfg.Cloudflare = a.cfg.Cloudflare
	c.cfg.Cloudflare.AccessClientID, c.cfg.Cloudflare.AccessClientSecretFile = "id.access", secret
	c.cloudflareOuter = edge.Client()
	if err := c.directory.SetProvider(device.Cloudflare, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PairWith(ctx, device.Cloudflare, "https://"+host, "correct horse battery", b.identity.ID); err == nil {
		t.Fatal("pairing succeeded through a tunnel with no open invite")
	}
	if err := b.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PairWith(ctx, device.Cloudflare, "https://"+host, "wrong horse battery", b.identity.ID); err == nil {
		t.Fatal("pairing succeeded with the wrong password")
	}
	if _, err := c.PairWith(ctx, device.Cloudflare, "https://"+host, "correct horse battery", b.identity.ID); err != nil {
		t.Fatal("pairing failed while an invite was open:", err)
	}
	if _, ok := b.trust.Find(c.identity.ID); !ok {
		t.Fatal("the paired device is not trusted")
	}

	// An ordinary trusted connection is served as before while the invite window is open.
	a.cfg.Cloudflare.AccessClientID, a.cfg.Cloudflare.AccessClientSecretFile = "id.access", secret
	a.probe(ctx, candidates[0])
	devices, _ = a.directory.Snapshot(time.Now())
	reachable := false
	for _, d := range devices {
		for _, path := range d.Paths {
			reachable = reachable || (d.DeviceID == b.identity.ID && path.Kind == device.Cloudflare && path.Reachable)
		}
	}
	if !reachable {
		t.Fatal("a trusted device could not connect while an invite was open", devices)
	}
}

func poolAddrs(cfg config.Cloudflare) []netip.Addr {
	var out []netip.Addr
	for _, m := range newPreferredPool("").members(cfg) {
		out = append(out, m.addr)
	}
	return out
}

func TestPreferredAddrsReadFileEachTime(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ips")
	cfg := config.Cloudflare{PreferredIPs: []string{"104.16.0.1"}, PreferredIPsFile: file}
	if got := poolAddrs(cfg); len(got) != 1 {
		t.Fatal("missing file must be ignored:", got)
	}
	if err := os.WriteFile(file, []byte("# list\n104.16.0.1\n172.64.0.9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := poolAddrs(cfg); len(got) != 2 || got[1].String() != "172.64.0.9" {
		t.Fatal(got)
	}
	if err := os.WriteFile(file, []byte("8.8.8.8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := poolAddrs(cfg); len(got) != 1 {
		t.Fatal("a list with a foreign address must be rejected whole:", got)
	}
}
