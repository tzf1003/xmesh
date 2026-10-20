package connectivity

import (
	"context"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

// Opt in on a host with a connected system Tailscale interface and TCP 7433 free on that IP:
// XMESH_TEST_SYSTEM_TAILSCALE_INTERFACE=Tailscale go test ./internal/connectivity -run SystemTailscale -v
func TestSystemTailscaleListenerPairingAndAgent(t *testing.T) {
	iface := os.Getenv("XMESH_TEST_SYSTEM_TAILSCALE_INTERFACE")
	if iface == "" {
		t.Skip("set XMESH_TEST_SYSTEM_TAILSCALE_INTERFACE to a connected system Tailscale interface")
	}
	_, ip, err := mesh.SelectLANInterface(iface)
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok || !config.IsTailnetAddr(addr) {
		t.Fatalf("%s must have a system Tailscale IPv4, got %v", iface, ip)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	client, _ := node(t, "system-tailscale-client")
	server, cfg := node(t, "system-tailscale-agent")
	cfg.Mesh.LAN.Interface = iface
	cfg.Mesh.LAN.MDNS = false
	cfg.Mesh.Tailnet.Enabled = false
	ag, err := agent.New(config.Static(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ag.Close() })
	server.SetAgent(func(ctx context.Context, p identity.Principal, transport mcp.Transport) error {
		_, err := ag.ConnectDevice(ctx, p, transport)
		return err
	})
	server.Reconfigure(cfg.Mesh)
	server.workers.Go(server.supervise)
	for {
		ready := false
		for _, status := range server.ProviderStatuses() {
			if status.Kind == device.LAN {
				if status.Error != "" {
					t.Fatal(status.Error)
				}
				ready = status.Ready
			}
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("system Tailscale listener did not become ready", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	address := "https://" + net.JoinHostPort(ip.String(), "7433")
	// Trust the server on the client only, so the server must still reject the unpaired client.
	if err := client.trust.Grant(ctx, server.identity.Document(server.name), []string{"tag:peer"}, nil); err != nil {
		t.Fatal(err)
	}
	unpaired, err := client.httpClient(device.LAN, server.identity.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer unpaired.CloseIdleConnections()
	unpaired.Timeout = 2 * time.Second
	response, err := unpaired.Get(address + identityPath)
	if err == nil {
		response.Body.Close()
		t.Fatal("unpaired device reached the identity endpoint without bootstrap ALPN")
	}
	if err := server.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PairWith(ctx, device.LAN, address, "correct horse battery", server.identity.ID); err != nil {
		t.Fatal(err)
	}
	if candidates := client.Candidates(); len(candidates) != 1 || candidates[0].Address != address || candidates[0].Kind != device.LAN {
		t.Fatal("system Tailscale address must persist as an OS-routed LAN hint", candidates)
	}
	transport, err := client.Transport(ctx, mesh.Peer{ID: server.identity.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "system-tailscale-test", Version: "2"}, nil)
	session, err := mcpClient.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "sys.info", Arguments: map[string]any{}})
	if err != nil || result == nil || result.IsError {
		t.Fatal("real Agent sys.info over system Tailscale", result, err)
	}
}
