package mesh

import (
	"net"
	"strings"
	"testing"
)

func lanNIC(name string, up bool, ips ...string) lanCandidate {
	c := lanCandidate{Name: name, Up: up}
	for _, ip := range ips {
		c.IPs = append(c.IPs, net.ParseIP(ip))
	}
	return c
}

// A machine with VMware, WSL, Hyper-V, Clash and Tailscale adapters listed ahead of the real NIC.
func Test_LANPicksTheRealInterface(t *testing.T) {
	nics := []lanCandidate{
		lanNIC("Loopback Pseudo-Interface 1", true, "127.0.0.1"),
		lanNIC("VMware Network Adapter VMnet1", true, "192.168.56.1"),
		lanNIC("vEthernet (Default Switch)", true, "172.20.64.1"),
		lanNIC("vEthernet (WSL)", true, "172.28.0.1"),
		lanNIC("Clash", true, "198.18.0.1"),
		lanNIC("Mihomo", true, "192.168.99.1"),
		lanNIC("Tailscale", true, "100.64.0.5"),
		lanNIC("WLAN", true, "192.168.31.15"),
		lanNIC("Ethernet", false, "10.0.0.5"),
	}
	got, ip, err := pickLANInterface(nics, "")
	if err != nil || got.Name != "WLAN" || ip.String() != "192.168.31.15" {
		t.Fatalf("%+v %v %v", got, ip, err)
	}
	// a pinned interface wins, even a virtual one
	got, ip, err = pickLANInterface(nics, "VMware Network Adapter VMnet1")
	if err != nil || got.Name != "VMware Network Adapter VMnet1" || ip.String() != "192.168.56.1" {
		t.Fatalf("%+v %v %v", got, ip, err)
	}
	// a pinned interface that is down or unknown is an error, not another network
	for _, name := range []string{"Ethernet", "Wi-Fi 9"} {
		if _, _, err = pickLANInterface(nics, name); err == nil || !strings.Contains(err.Error(), "mesh.lan.interface") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func Test_LANFallsBackToVirtualAndRefusesNone(t *testing.T) {
	got, _, err := pickLANInterface([]lanCandidate{lanNIC("docker0", true, "172.17.0.1")}, "")
	if err != nil || got.Name != "docker0" {
		t.Fatalf("a host with only a virtual interface still works: %+v %v", got, err)
	}
	if _, _, err = pickLANInterface([]lanCandidate{lanNIC("lo", true, "127.0.0.1"), lanNIC("eth0", true, "8.8.8.8")}, ""); err == nil {
		t.Fatal("no private interface must fail")
	}
	for _, name := range []string{"eth0", "en0", "WLAN", "Wi-Fi", "Ethernet", "Local Area Connection"} {
		if looksVirtual(name) {
			t.Fatalf("%s looks virtual", name)
		}
	}
	for _, name := range []string{"veth12ab", "br-1a2b", "virbr0", "utun3", "tun0", "docker0", "vEthernet (WSL)", "Mihomo", "Clash"} {
		if !looksVirtual(name) {
			t.Fatalf("%s should look virtual", name)
		}
	}
}

func Test_LANSystemTailscaleRequiresExplicitInterface(t *testing.T) {
	for _, name := range []string{"Tailscale", "tailscale0", "utun7"} {
		t.Run(name, func(t *testing.T) {
			nics := []lanCandidate{lanNIC(name, true, "100.87.123.24")}
			if _, _, err := pickLANInterface(nics, ""); err == nil {
				t.Fatal("system Tailscale must not become the automatic LAN fallback")
			}
			got, ip, err := pickLANInterface(nics, name)
			if err != nil || got.Name != name || ip.String() != "100.87.123.24" {
				t.Fatalf("explicit system Tailscale interface: %+v %v %v", got, ip, err)
			}
		})
	}
}

func Test_LANExplicitInterfaceAddressBoundaries(t *testing.T) {
	for _, tc := range []struct {
		ip string
		ok bool
	}{
		{"100.64.0.0", true},
		{"100.127.255.255", true},
		{"100.63.255.255", false},
		{"100.128.0.0", false},
		{"8.8.8.8", false},
		{"198.18.0.1", false},
		{"169.254.1.1", false},
		{"127.0.0.1", false},
		{"0.0.0.0", false},
		{"224.0.0.251", false},
		{"fd7a:115c:a1e0::1", false}, // LAN listeners are still IPv4-only.
	} {
		t.Run(tc.ip, func(t *testing.T) {
			_, ip, err := pickLANInterface([]lanCandidate{lanNIC("pinned", true, tc.ip)}, "pinned")
			if tc.ok {
				if err != nil || ip.String() != tc.ip {
					t.Fatalf("expected %s, got %v: %v", tc.ip, ip, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "mesh.lan.interface") {
				t.Fatalf("must reject pinned interface address %s: %v", tc.ip, err)
			}
		})
	}
	for _, nic := range []lanCandidate{
		lanNIC("Tailscale", false, "100.87.123.24"),
		{Name: "Tailscale", Up: true, Loopback: true, IPs: []net.IP{net.ParseIP("100.87.123.24")}},
	} {
		if _, _, err := pickLANInterface([]lanCandidate{nic, lanNIC("WLAN", true, "192.168.1.2")}, "Tailscale"); err == nil {
			t.Fatal("unusable pinned Tailscale interface must fail without falling back to WLAN")
		}
	}
}
