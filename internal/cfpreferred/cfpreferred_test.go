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

func TestParseListHeaderAfterComments(t *testing.T) {
	list, err := ParseList(strings.NewReader("# generated\n\nIP 地址,已发送\n104.16.1.1,4\n"))
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if _, err := ParseList(strings.NewReader("104.16.1.1\nIP 地址,已发送\n")); err == nil {
		t.Fatal("a non-address line after the first must be rejected")
	}
}

func TestParseListRejectsOversizedInput(t *testing.T) {
	big := strings.Repeat("# padding padding padding padding\n", 40000) + "8.8.8.8\n"
	if _, err := ParseList(strings.NewReader(big)); err == nil {
		t.Fatal("a truncated list must be rejected, not partially imported")
	}
}

func TestPinDialsOnlyTheAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	c, err := Pin(netip.MustParseAddr("127.0.0.1"))(context.Background(), "tcp", "unresolvable.invalid:"+port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
