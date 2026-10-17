package mdns

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func testService() Service {
	return Service{Instance: "mac-lan", Type: "_xmesh._tcp", Host: "mac-lan", Port: 7433, Text: []string{"fingerprint=abc"}, IP: net.ParseIP("192.168.31.30")}
}

func Test_HostNameCarriesTheDomainOnce(t *testing.T) {
	s := testService()
	if got := s.hostName(); got != "mac-lan.local." {
		t.Fatalf("host %q", got)
	}
	if got := s.instanceName(); got != "mac-lan._xmesh._tcp.local." {
		t.Fatalf("instance %q", got)
	}
	_, srv, _, a := s.records(recordTTL)
	if srv.Target != "mac-lan.local." || a.Hdr.Name != "mac-lan.local." {
		t.Fatalf("%v %v", srv, a)
	}
	for in, want := range map[string]string{"WIN-LAN": "WIN-LAN", "my pc.local": "my-pc-local", "": "xmesh", "..": "xmesh", "笔记本": "xmesh", "a_b": "a-b"} {
		if got := Label(in); got != want {
			t.Fatalf("Label(%q)=%q want %q", in, got, want)
		}
	}
	if got := Label(strings.Repeat("x", 80)); len(got) != 63 {
		t.Fatalf("label length %d", len(got))
	}
}

func browseQuery(class uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion("_xmesh._tcp.local.", dns.TypePTR)
	m.Question[0].Qclass |= class
	return m
}

func Test_ResponderAnswersBrowseQueries(t *testing.T) {
	s := testService()
	resp, unicast := s.reply(browseQuery(0), false)
	if resp == nil || unicast || len(resp.Question) != 0 || resp.Id != 0 {
		t.Fatalf("a multicast query is answered by multicast without a question: %+v unicast=%v", resp, unicast)
	}
	if len(resp.Answer) != 1 || len(resp.Extra) != 3 {
		t.Fatalf("PTR answer with SRV, TXT and A as additional records: %v / %v", resp.Answer, resp.Extra)
	}
	if _, unicast = s.reply(browseQuery(cacheFlush), false); !unicast {
		t.Fatal("a QU question asks for a unicast answer")
	}
	query := browseQuery(0)
	query.Id = 4242
	resp, unicast = s.reply(query, true)
	if resp == nil || !unicast || resp.Id != 4242 || len(resp.Question) != 1 {
		t.Fatalf("a resolver on a random port gets a unicast answer echoing id and question: %+v", resp)
	}
	other := new(dns.Msg)
	other.SetQuestion("_other._tcp.local.", dns.TypePTR)
	if resp, _ = s.reply(other, false); resp != nil {
		t.Fatalf("a question about another service is not ours: %v", resp)
	}
	srv := new(dns.Msg)
	srv.SetQuestion("MAC-LAN._xmesh._tcp.local.", dns.TypeSRV)
	if resp, _ = s.reply(srv, false); resp == nil || len(resp.Answer) != 1 || len(resp.Extra) != 1 {
		t.Fatalf("instance lookup is case-insensitive: %v", resp)
	}
}

func roundTrip(t *testing.T, m *dns.Msg) *dns.Msg {
	t.Helper()
	buf, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	out := new(dns.Msg)
	if err = out.Unpack(buf); err != nil {
		t.Fatal(err)
	}
	return out
}

func Test_CollectorResolvesAcrossDatagrams(t *testing.T) {
	s := testService()
	full := roundTrip(t, s.announcement(recordTTL))
	c := newCollector("_xmesh._tcp", "local")
	// SRV, TXT and PTR first; the address is still missing, so nothing is delivered yet.
	head := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true}, Answer: full.Answer[:3]}
	if got := c.add(head); len(got) != 0 {
		t.Fatalf("delivered before the address arrived: %+v", got)
	}
	tail := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true}, Extra: full.Answer[3:]}
	got := c.add(tail)
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	e := got[0]
	if e.Instance != "mac-lan" || e.HostName != "mac-lan.local." || e.Port != 7433 || e.TTL != recordTTL || len(e.AddrIPv4) != 1 || e.AddrIPv4[0].String() != "192.168.31.30" || e.Text[0] != "fingerprint=abc" {
		t.Fatalf("%+v", e)
	}
	// a repeat of the same announcement is not delivered again within one browse
	if got = c.add(full); len(got) != 0 {
		t.Fatalf("duplicate delivery: %+v", got)
	}
	// goodbye is delivered with a zero TTL, then the node can come back
	if got = c.add(roundTrip(t, s.announcement(0))); len(got) != 1 || got[0].TTL != 0 || len(got[0].AddrIPv4) != 0 || got[0].Text[0] != "fingerprint=abc" {
		t.Fatalf("goodbye: %+v", got)
	}
	if got = c.add(full); len(got) != 1 || got[0].TTL == 0 {
		t.Fatalf("a node that returned after goodbye: %+v", got)
	}
}

// Nodes that predate this package advertised "<name>.local.local." and answered with SRV, TXT and A in a
// single message; they stay discoverable.
func Test_CollectorKeepsLegacyAnnouncements(t *testing.T) {
	legacy := testService()
	legacy.Host = "win-lan.local"
	msg := legacy.announcement(3200)
	for _, rr := range msg.Answer {
		switch rr := rr.(type) {
		case *dns.SRV:
			rr.Target = "win-lan.local.local."
		case *dns.A:
			rr.Hdr.Name = "WIN-LAN.local.local."
		}
	}
	msg.Answer[3].(*dns.A).A = net.ParseIP("192.168.31.15")
	got := newCollector("_xmesh._tcp", "local.").add(roundTrip(t, msg))
	if len(got) != 1 || got[0].AddrIPv4[0].String() != "192.168.31.15" || got[0].HostName != "win-lan.local.local." {
		t.Fatalf("%+v", got)
	}
}

func Test_CollectorIgnoresOtherServicesAndQueries(t *testing.T) {
	other := Service{Instance: "x", Type: "_http._tcp", Host: "x", Port: 80, IP: net.ParseIP("10.0.0.1")}
	c := newCollector("_xmesh._tcp", "local")
	if got := c.add(roundTrip(t, other.announcement(120))); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if got := c.add(browseQuery(0)); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// multicastWorks reports whether a datagram sent to the group on iface comes back to a listener there.
func multicastWorks(iface net.Interface, ip net.IP) bool {
	listener, err := listenMulticast(iface)
	if err != nil {
		return false
	}
	defer listener.Close()
	asker, err := listenQuery(iface, ip)
	if err != nil {
		return false
	}
	defer asker.Close()
	got := make(chan struct{}, 1)
	go readLoop(listener, func(packet) {
		select {
		case got <- struct{}{}:
		default:
		}
	})
	for range 5 {
		send(asker, browseQuery(0), groupAddr)
		select {
		case <-got:
			return true
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

func testInterface(t *testing.T) (net.Interface, net.IP) {
	t.Helper()
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil && multicastWorks(iface, ipnet.IP.To4()) {
				return iface, ipnet.IP.To4()
			}
		}
	}
	t.Skip("this host has no interface with working IPv4 multicast")
	return net.Interface{}, nil
}

func Test_AdvertiseAndBrowseOverMulticast(t *testing.T) {
	iface, ip := testInterface(t)
	svc := Service{Instance: "node-under-test", Type: "_xmesh-test._tcp", Host: "node-under-test", Port: 7433, Text: []string{"fingerprint=abc123"}, IP: ip}
	responder, err := Advertise(svc, iface)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			responder.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	entries, err := Browse(ctx, svc.Type, "local.", iface, ip)
	if err != nil {
		t.Fatal(err)
	}
	wait := func(want func(*Entry) bool) *Entry {
		t.Helper()
		for {
			select {
			case e, ok := <-entries:
				if !ok {
					t.Fatal("browse ended before the expected entry")
				}
				if e.Instance == "node-under-test" && want(e) {
					return e
				}
			case <-time.After(8 * time.Second):
				t.Fatal("expected entry never arrived")
			}
		}
	}
	e := wait(func(e *Entry) bool { return e.TTL > 0 })
	if e.Port != 7433 || e.HostName != "node-under-test.local." || len(e.AddrIPv4) != 1 || !e.AddrIPv4[0].Equal(ip) || e.Text[0] != "fingerprint=abc123" {
		t.Fatalf("%+v", e)
	}
	responder.Close()
	closed = true
	wait(func(e *Entry) bool { return e.TTL == 0 })
}
