package app

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
)

// fakeTailnet is a network whose only job here is to report its tailnet addresses.
type fakeTailnet struct {
	mesh.Network
	ips  []netip.Addr
	name string
}

func (f fakeTailnet) Addresses(context.Context) ([]netip.Addr, string, error) {
	return f.ips, f.name, nil
}

func gatewayCfg(listen string, access []string, hosts []string) *config.Config {
	return &config.Config{Gateway: config.Gateway{Listen: listen, Access: access, Hosts: hosts}}
}

// conformance: CTR-CTL-03 the gateway lists the addresses it answers on right now. Loopback is always
// listed; the others follow the bind address and gateway.access.
func Test_GatewayAddressesFollowBindAndAccess(t *testing.T) {
	info := gatewayInfo(gatewayCfg(":8080", []string{"localhost", "lan"}, []string{"xmesh.home.example"}), nil)
	if info.Addresses[0].Kind != "localhost" || info.Addresses[0].URL != "http://127.0.0.1:8080/mcp" || info.URL != info.Addresses[0].URL {
		t.Fatalf("%+v", info)
	}
	for _, a := range info.Addresses[1:] {
		if a.Kind == "tailnet_ip" || a.Kind == "tailnet_name" {
			t.Fatalf("tailnet address without tailnet access: %+v", a)
		}
		if a.Kind == "lan" && !strings.HasPrefix(a.URL, "http://192.168.") && !strings.HasPrefix(a.URL, "http://10.") && !strings.HasPrefix(a.URL, "http://172.") {
			t.Fatalf("not a private address: %+v", a)
		}
	}
	last := info.Addresses[len(info.Addresses)-1]
	if last.Kind != "host" || last.Host != "xmesh.home.example" || last.URL != "http://xmesh.home.example:8080/mcp" {
		t.Fatalf("configured host missing: %+v", last)
	}
	if info.Auth != "pat" || strings.Join(info.Access, ",") != "localhost,lan" || info.Hosts[0] != "xmesh.home.example" {
		t.Fatalf("%+v", info)
	}
}

// A loopback bind answers on this machine only, whatever gateway.access says.
func Test_GatewayLoopbackBindListsOnlyLocalhost(t *testing.T) {
	info := gatewayInfo(gatewayCfg("127.0.0.1:9100", []string{"localhost", "lan", "tailnet"}, []string{"xmesh.home.example"}), fakeTailnet{ips: nil, name: "node.example.ts.net"})
	if len(info.Addresses) != 1 || info.Addresses[0].URL != "http://127.0.0.1:9100/mcp" {
		t.Fatalf("%+v", info.Addresses)
	}
}

// Without the lan entry no LAN address is listed; the tailnet entry lists the node's IPs and MagicDNS name.
func Test_GatewayTailnetAddresses(t *testing.T) {
	network := fakeTailnet{ips: []netip.Addr{netip.MustParseAddr("100.100.1.2"), netip.MustParseAddr("fd7a:115c:a1e0::2")}, name: "node.example.ts.net"}
	info := gatewayInfo(gatewayCfg(":8080", []string{"localhost", "tailnet"}, nil), network)
	var got []string
	for _, a := range info.Addresses {
		got = append(got, a.Kind+" "+a.URL)
	}
	want := []string{
		"localhost http://127.0.0.1:8080/mcp",
		"tailnet_ip http://100.100.1.2:8080/mcp",
		"tailnet_ip http://[fd7a:115c:a1e0::2]:8080/mcp",
		"tailnet_name http://node.example.ts.net:8080/mcp",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	none := gatewayInfo(gatewayCfg(":8080", []string{"localhost"}, nil), network)
	if len(none.Addresses) != 1 {
		t.Fatalf("lan and tailnet not in access but listed: %+v", none.Addresses)
	}
}
