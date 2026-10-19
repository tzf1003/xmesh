package config

import (
	"net/netip"
	"reflect"
	"testing"
)

// conformance: CTR-GW-07 sources are checked per request; loopback always, the ranges by entry.
func Test_GatewayAccessSources(t *testing.T) {
	cases := []struct {
		name   string
		access []string
		source string
		want   bool
	}{
		{"loopback with no entries", nil, "127.0.0.1", true},
		{"loopback IPv6", nil, "::1", true},
		{"lan private range denied without lan", []string{"localhost"}, "192.168.1.5", false},
		{"lan private range", []string{"lan"}, "192.168.1.5", true},
		{"lan 10/8", []string{"lan"}, "10.20.30.40", true},
		{"lan 172.16/12", []string{"lan"}, "172.31.0.1", true},
		{"lan IPv4-mapped IPv6", []string{"lan"}, "::ffff:192.168.1.5", true},
		{"public address never by lan", []string{"lan", "tailnet"}, "8.8.8.8", false},
		{"tailnet CGNAT range", []string{"tailnet"}, "100.100.1.2", true},
		{"tailnet IPv6 prefix", []string{"tailnet"}, "fd7a:115c:a1e0::1", true},
		{"tailnet denied without tailnet", []string{"lan"}, "100.100.1.2", false},
		{"tailnet IPv6 denied without tailnet", []string{"lan"}, "fd7a:115c:a1e0::1", false},
		{"custom CIDR", []string{"203.0.113.0/24"}, "203.0.113.9", true},
		{"custom CIDR outside", []string{"203.0.113.0/24"}, "203.0.114.9", false},
		{"custom single address", []string{"198.51.100.7"}, "198.51.100.7", true},
		{"custom single address other", []string{"198.51.100.7"}, "198.51.100.8", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := ParseAccess(c.access)
			if err != nil {
				t.Fatal(err)
			}
			if got := a.AllowsSource(netip.MustParseAddr(c.source)); got != c.want {
				t.Fatalf("AllowsSource(%s) = %v, want %v", c.source, got, c.want)
			}
		})
	}
}

func Test_GatewayAccessRejectsMalformedEntries(t *testing.T) {
	for _, entry := range []string{"everyone", "0.0.0.0/33", "192.168.1", "*"} {
		if _, err := ParseAccess([]string{entry}); err == nil {
			t.Fatalf("entry %q accepted", entry)
		}
	}
}

// conformance: CTR-GW-07 a Host header must name an address, localhost, or a configured host.
func Test_GatewayHostHeader(t *testing.T) {
	hosts := []string{"xmesh.home.example"}
	cases := map[string]bool{
		"127.0.0.1:8080":               true,
		"192.168.1.5:8080":             true,
		"[::1]:8080":                   true,
		"[fd7a:115c:a1e0::1]:8080":     true,
		"localhost:8080":               true,
		"localhost":                    true,
		"xmesh.home.example:8080":      true,
		"XMESH.home.example.":          true,
		"evil.example:8080":            false,
		"xmesh.home.example.evil:8080": false,
		"":                             false,
	}
	for header, want := range cases {
		if got := HostAllowed(header, hosts); got != want {
			t.Fatalf("HostAllowed(%q) = %v, want %v", header, got, want)
		}
	}
	if HostAllowed("xmesh.home.example:8080", nil) {
		t.Fatal("a host name accepted without being configured")
	}
}

func Test_GatewayListenHosts(t *testing.T) {
	for _, listen := range []string{":8080", "0.0.0.0:8080", "[::]:8080"} {
		if _, loopback, err := GatewayListenHost(listen); err != nil || loopback {
			t.Fatalf("%s: loopback=%v err=%v", listen, loopback, err)
		}
	}
	for _, listen := range []string{"127.0.0.1:8080", "[::1]:8080"} {
		if _, loopback, err := GatewayListenHost(listen); err != nil || !loopback {
			t.Fatalf("%s: loopback=%v err=%v", listen, loopback, err)
		}
	}
}

// The defaults are the ones the panel offers: localhost, LAN and tailnet, on every interface.
func Test_GatewayDefaults(t *testing.T) {
	c, err := Parse([]byte("node:\n  name: n1\n  roles: [agent, gateway]\nmesh:\n  mode: lan\n  lan:\n    mdns: true\ngateway:\n  listen: \":8080\"\nagent:\n  default_cwd: \"~\"\n  cwd_policy:\n    allow: [\"~\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Gateway.Listen != ":8080" {
		t.Fatalf("listen %q", c.Gateway.Listen)
	}
	if want := []string{"localhost", "lan", "tailnet"}; !reflect.DeepEqual(c.Gateway.Access, want) {
		t.Fatalf("access %v", c.Gateway.Access)
	}
}
