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
	for _, name := range []string{"veth12ab", "br-1a2b", "virbr0", "utun3", "tun0", "docker0", "vEthernet (WSL)"} {
		if !looksVirtual(name) {
			t.Fatalf("%s should look virtual", name)
		}
	}
}
