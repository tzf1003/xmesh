package cfpreferred

import (
	"context"
	"math/rand/v2"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestParseListAcceptsPlainAndCSV(t *testing.T) {
	list, err := ParseList(strings.NewReader("# note\n104.16.1.1\n\n104.16.1.1\n2606:4700::1\n"))
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	csv := "IP 地址,已发送,已接收,丢包率,平均延迟,下载速度 (MB/s)\n172.64.0.5,4,4,0.00,120.5,10.2\n104.18.2.2,4,4,0.00,130.1,9.1\n"
	list, err = ParseList(strings.NewReader(csv))
	if err != nil || len(list) != 2 || list[0] != netip.MustParseAddr("172.64.0.5") {
		t.Fatal(list, err)
	}
}

func TestParseListRejectsForeignAddresses(t *testing.T) {
	for _, bad := range []string{"8.8.8.8", "10.0.0.1", "127.0.0.1", "not-an-ip", "104.16.1.1\n1.2.3.4"} {
		if _, err := ParseList(strings.NewReader(bad + "\n")); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSampleStaysInsideRanges(t *testing.T) {
	for _, a := range Sample(300, true, rand.New(rand.NewPCG(1, 2))) {
		if !Valid(a.String()) {
			t.Fatal("outside Cloudflare ranges:", a)
		}
	}
	for _, a := range Sample(100, false, rand.New(rand.NewPCG(3, 4))) {
		if !a.Is4() {
			t.Fatal("IPv6 sampled without being asked:", a)
		}
	}
}

func TestDialerPrefersAddressesThenFallsBack(t *testing.T) {
	var dialed []string
	base := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		if strings.HasPrefix(address, "104.16.0.2:") || address == "edge.example.com:443" {
			c, _ := net.Pipe()
			return c, nil
		}
		return nil, net.UnknownNetworkError("down")
	}
	addrs := []netip.Addr{netip.MustParseAddr("104.16.0.1"), netip.MustParseAddr("104.16.0.2")}
	if _, err := Dialer(addrs, base)(t.Context(), "tcp", "edge.example.com:443"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(dialed, ",") != "104.16.0.1:443,104.16.0.2:443" {
		t.Fatal(dialed)
	}
	dialed = nil
	if _, err := Dialer(addrs[:1], base)(t.Context(), "tcp", "edge.example.com:443"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(dialed, ",") != "104.16.0.1:443,edge.example.com:443" {
		t.Fatal("did not fall back to the normal address:", dialed)
	}
	dialed = nil
	if _, err := Dialer(nil, base)(t.Context(), "tcp", "edge.example.com:443"); err != nil || len(dialed) != 1 {
		t.Fatal(dialed, err)
	}
}
