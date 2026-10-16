package mesh

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"tailscale.com/tailcfg"
)

// This opt-in test takes exclusive ownership of the supplied node state.
// conformance: CON-NET-02 CON-NET-03 CON-NET-05 CON-NET-07 CON-ARCH-08
func Test_TailnetPeerConnectivity(t *testing.T) {
	path, state, target := os.Getenv("XMESH_TEST_TAILNET_CONFIG"), os.Getenv("XMESH_TEST_TAILNET_STATE"), os.Getenv("XMESH_TEST_TAILNET_PEER")
	if path == "" || state == "" || target == "" {
		t.Skip("set XMESH_TEST_TAILNET_CONFIG, XMESH_TEST_TAILNET_STATE and XMESH_TEST_TAILNET_PEER")
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var logf func(string, ...any)
	if os.Getenv("XMESH_TEST_TAILNET_VERBOSE") == "1" {
		logf = t.Logf
	}
	network, err := openTailnet(ctx, c, state, logf)
	if err != nil {
		t.Fatal(err)
	}
	defer network.Close()
	lc, err := network.server.LocalClient()
	if err != nil {
		t.Fatal(err)
	}
	status, err := lc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("backend=%s health=%v", status.BackendState, status.Health)
	var peers []Peer
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		peers, err = network.Peers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range peers {
			if p.Name == target {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	status, err = lc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range peers {
		if peer.Name != target {
			continue
		}
		t.Logf("peer=%s address=%s direct=%t relay=%s", peer.Name, peer.Address, peer.Direct, peer.Relay)
		var ip netip.Addr
		for _, p := range status.Peer {
			if string(p.ID) == peer.ID && len(p.TailscaleIPs) > 0 {
				ip = p.TailscaleIPs[0]
				break
			}
		}
		if !ip.IsValid() {
			t.Fatal("target peer has no Tailscale address")
		}
		pingCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		ping, err := lc.Ping(pingCtx, ip, tailcfg.PingDisco)
		stop()
		if err != nil {
			t.Fatal(err)
		}
		if ping.Err != "" {
			t.Fatal(ping.Err)
		}
		t.Logf("ping direct=%s derp=%d/%s latency=%fs", ping.Endpoint, ping.DERPRegionID, ping.DERPRegionCode, ping.LatencySeconds)
		if os.Getenv("XMESH_TEST_TAILNET_REQUIRE_DERP") == "1" && (ping.Endpoint != "" || ping.DERPRegionID == 0) {
			t.Fatal("expected a DERP route")
		}
		request, err := http.NewRequestWithContext(ctx, "GET", peer.Address, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := network.HTTPClient(peer).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		// GET without an initialized MCP session is rejected by the live Agent.
		if response.StatusCode != http.StatusBadRequest && response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("Agent response: %d", response.StatusCode)
		}
		return
	}
	t.Fatal("target peer is not visible")
}
