package cfpreferred

import (
	"context"
	"io"
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

func TestRegionOf(t *testing.T) {
	for colo, want := range map[string]string{"hkg": "hk", "NRT": "jp", "LAX": "us-w", "FRA": "eu", "ZZZ": RegionOther, "": ""} {
		if got := RegionOf(colo); got != want {
			t.Errorf("%q: %q, want %q", colo, got, want)
		}
	}
	if !ValidRegion("us-e") || !ValidRegion("hkg") || ValidRegion("hongkong") || ValidRegion("h1g") {
		t.Fatal("ValidRegion")
	}
}

func TestSampleNetsStaysInNets(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, a := range SampleNets([]string{"104.16.9.0/24", "8.8.8.0/24", "bad"}, 20, rng) {
		if NetKey(a) != "104.16.9.0/24" {
			t.Fatal(a)
		}
	}
	if SampleNets(nil, 5, rng) != nil {
		t.Fatal("no nets, no addresses")
	}
}

func TestReadColo(t *testing.T) {
	read := func(resp string) string {
		c, s := net.Pipe()
		go func() {
			buf := make([]byte, 512)
			s.Read(buf)
			io.WriteString(s, resp)
			s.Close()
		}()
		defer c.Close()
		return readColo(context.Background(), c, "x.example.com")
	}
	if got := read("HTTP/1.1 200 OK\r\nContent-Length: 26\r\n\r\nfl=1\nip=1.1.1.1\ncolo=hkg\n"); got != "HKG" {
		t.Fatal(got)
	}
	if got := read("HTTP/1.1 403 Forbidden\r\nCf-Ray: 8c3f1a2b3c4d5e6f-NRT\r\nContent-Length: 0\r\n\r\n"); got != "NRT" {
		t.Fatal(got)
	}
	if got := read("garbage"); got != "" {
		t.Fatal(got)
	}
}
