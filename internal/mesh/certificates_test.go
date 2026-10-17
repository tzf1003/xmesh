package mesh

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/grandcat/zeroconf"
	"github.com/tzf1003/xmesh/internal/identity"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// conformance: CON-NET-09 CON-NET-02
func Test_CONNET09_PairedMTLS(t *testing.T) {
	oneDir, twoDir := t.TempDir(), t.TempDir()
	one, e := LoadCertificate(oneDir, "one")
	if e != nil {
		t.Fatal(e)
	}
	two, e := LoadCertificate(twoDir, "two")
	if e != nil {
		t.Fatal(e)
	}
	oneTrust, _ := OpenTrust(oneDir)
	twoTrust, _ := OpenTrust(twoDir)
	exchange := func(cert Certificate, dir, name string, trust *TrustStore) {
		if e := trust.Add(Trusted{Name: name, Fingerprint: cert.Fingerprint, CertificatePEM: string(cert.PEM), Tags: []string{"tag:device", "tag:peer"}}); e != nil {
			t.Fatal(e)
		}
	}
	handshake := func(want bool) {
		listener, e := tls.Listen("tcp", "127.0.0.1:0", oneTrust.TLS(one, "", true))
		if e != nil {
			t.Fatal(e)
		}
		defer listener.Close()
		done := make(chan error, 1)
		go func() {
			c, e := listener.Accept()
			if e != nil {
				done <- e
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(time.Second))
			done <- c.(*tls.Conn).Handshake()
		}()
		dialer := tls.Dialer{Config: twoTrust.TLS(two, "", false), NetDialer: &net.Dialer{Timeout: time.Second}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c, e := dialer.DialContext(ctx, "tcp", listener.Addr().String())
		if c != nil {
			c.Close()
		}
		serverErr := <-done
		if want && (e != nil || serverErr != nil) {
			t.Fatal(e, serverErr)
		}
		if !want && e == nil && serverErr == nil {
			t.Fatal("unpaired connection accepted")
		}
	}
	handshake(false)
	exchange(two, twoDir, "two", oneTrust)
	exchange(one, oneDir, "one", twoTrust)
	handshake(true)
	third, _ := LoadCertificate(t.TempDir(), "third")
	two = third
	handshake(false)
}

func Test_LANRouteRequiresExpectedCertificate(t *testing.T) {
	clientCert, _ := LoadCertificate(t.TempDir(), "client")
	expectedCert, _ := LoadCertificate(t.TempDir(), "expected")
	otherCert, _ := LoadCertificate(t.TempDir(), "other")
	clientTrust, _ := OpenTrust(t.TempDir())
	serverTrust, _ := OpenTrust(t.TempDir())
	add := func(s *TrustStore, c Certificate) {
		if e := s.Add(Trusted{Name: c.Leaf.Subject.CommonName, Fingerprint: c.Fingerprint, CertificatePEM: string(c.PEM), Tags: []string{"tag:device"}}); e != nil {
			t.Fatal(e)
		}
	}
	add(clientTrust, expectedCert)
	add(clientTrust, otherCert)
	add(serverTrust, clientCert)
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	server.TLS = serverTrust.TLS(otherCert, "", true)
	server.StartTLS()
	defer server.Close()
	l := &LAN{cert: clientCert, trust: clientTrust}
	peer := Peer{ID: "lan-" + expectedCert.Fingerprint[:24], Name: "expected", Fingerprint: expectedCert.Fingerprint, Address: server.URL + "/mcp"}
	client := l.HTTPClient(peer)
	defer client.CloseIdleConnections()
	if r, e := client.Get(peer.Address); e == nil {
		r.Body.Close()
		t.Fatal("different paired node accepted as route target")
	}
	if calls.Load() != 0 {
		t.Fatal("request reached substituted node")
	}
	peer.ID, peer.Name, peer.Fingerprint = "lan-"+otherCert.Fingerprint[:24], "other", otherCert.Fingerprint
	client = l.HTTPClient(peer)
	defer client.CloseIdleConnections()
	r, e := client.Get(peer.Address)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 204 || calls.Load() != 1 {
		t.Fatal("paired route failed")
	}
	if r, e = client.Get("http://" + server.Listener.Addr().String() + "/mcp"); e == nil {
		r.Body.Close()
		t.Fatal("plaintext LAN route accepted")
	}
}

func Test_LANDiscoveryRenewsOneShotEntries(t *testing.T) {
	cert, _ := LoadCertificate(t.TempDir(), "device")
	trust, _ := OpenTrust(t.TempDir())
	if e := trust.Add(Trusted{Name: "device", Fingerprint: cert.Fingerprint, CertificatePEM: string(cert.PEM), Tags: []string{"tag:device"}}); e != nil {
		t.Fatal(e)
	}
	l := &LAN{name: "client", trust: trust, peers: map[string]lanPeer{}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-l.done }()
	var rounds atomic.Int32
	if e := l.startDiscovery(ctx, 20*time.Millisecond, func(ctx context.Context) (<-chan *zeroconf.ServiceEntry, error) {
		entries := make(chan *zeroconf.ServiceEntry, 1)
		ent := zeroconf.NewServiceEntry("device", lanService, "local.")
		ent.Text, ent.TTL, ent.Port, ent.AddrIPv4 = []string{"fingerprint=" + cert.Fingerprint}, 120, AgentPort, []net.IP{net.ParseIP("192.168.1.2")}
		entries <- ent
		rounds.Add(1)
		go func() { <-ctx.Done(); close(entries) }()
		return entries, nil
	}); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		peers, e := l.Peers(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if rounds.Load() >= 3 && len(peers) == 1 {
			if peers[0].Fingerprint != cert.Fingerprint {
				t.Fatal(peers)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("discovery was not refreshed")
}
func Test_PairFingerprintAndPermissions(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	path := filepath.Join(t.TempDir(), "peer.json")
	fp, e := ExportPair(a, "one", path)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	// the document is consumed by the UI, whose schema has no null arrays
	if !strings.Contains(string(raw), `"tags": []`) {
		t.Fatalf("exported tags must be an empty array: %s", raw)
	}
	if e = ImportPair(b, path, "wrong", []string{"tag:device"}); e == nil {
		t.Fatal("unconfirmed fingerprint accepted")
	}
	if e = ImportPair(b, path, fp, []string{"tag:device", "tag:peer"}); e != nil {
		t.Fatal(e)
	}
	s, e := OpenTrust(b)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := s.Find(fp); !ok {
		t.Fatal("trust not persisted")
	}
}

// conformance: RUL-02
func Test_RUL02_IngressIdentity(t *testing.T) {
	if _, e := identity.FromPeer("node", []string{"tag:ingress"}, ""); e == nil {
		t.Fatal("ingress identity missing")
	}
	p, e := identity.FromPeer("node", []string{"tag:peer"}, "forged")
	if e != nil || p.OAuthClient != "" {
		t.Fatal(p, e)
	}
	p, e = identity.FromPeer("node", []string{"tag:ingress"}, "client")
	if e != nil || p.OAuthClient != "client" {
		t.Fatal(fmt.Sprint(p), e)
	}
}

func Test_LANMdnsDisabled(t *testing.T) {
	l, e := OpenLAN(context.Background(), "quiet", t.TempDir(), LANOptions{MDNS: false})
	if e != nil {
		t.Fatal(e)
	}
	if l.advertisement != nil {
		t.Fatal("advertised with mdns disabled")
	}
	if _, e := l.Listen(context.Background()); e == nil {
		if l.advertisement != nil {
			t.Fatal("listener advertised with mdns disabled")
		}
	} else {
		t.Log("no private interface in this environment:", e)
	}
	online, authoritative, e := l.Online(context.Background())
	if e != nil || authoritative || len(online) != 1 || !online["lan-"+l.cert.Fingerprint[:24]] {
		t.Fatal(online, authoritative, e)
	}
	if peers, _ := l.Peers(context.Background()); len(peers) != 0 {
		t.Fatal(peers)
	}
	closed := make(chan error, 1)
	go func() { closed <- l.Close() }()
	select {
	case e := <-closed:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked without a discovery loop")
	}
}

func Test_LANPresenceFollowsAnnouncements(t *testing.T) {
	cert, _ := LoadCertificate(t.TempDir(), "laptop")
	trust, _ := OpenTrust(t.TempDir())
	// A paired client is not a device: it never appears in Peers but is still present.
	if e := trust.Add(Trusted{Name: "laptop", Fingerprint: cert.Fingerprint, CertificatePEM: string(cert.PEM), Tags: []string{"tag:peer"}}); e != nil {
		t.Fatal(e)
	}
	self, _ := LoadCertificate(t.TempDir(), "self")
	l := &LAN{name: "self", cert: self, trust: trust, peers: map[string]lanPeer{}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-l.done }()
	var rounds atomic.Int32
	if e := l.startDiscovery(ctx, 20*time.Millisecond, func(ctx context.Context) (<-chan *zeroconf.ServiceEntry, error) {
		entries := make(chan *zeroconf.ServiceEntry, 1)
		ent := zeroconf.NewServiceEntry("laptop", lanService, "local.")
		ent.Text, ent.Port, ent.AddrIPv4 = []string{"fingerprint=" + cert.Fingerprint}, AgentPort, []net.IP{net.ParseIP("192.168.1.9")}
		if rounds.Add(1) <= 2 {
			ent.TTL = 120
		} else {
			// mDNS goodbye: TTL zero, no addresses.
			ent.TTL, ent.AddrIPv4 = 0, nil
		}
		entries <- ent
		go func() { <-ctx.Done(); close(entries) }()
		return entries, nil
	}); e != nil {
		t.Fatal(e)
	}
	id := "lan-" + cert.Fingerprint[:24]
	seen := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		online, authoritative, _ := l.Online(ctx)
		if authoritative {
			t.Fatal("LAN presence must not be authoritative")
		}
		if online[id] {
			seen = true
			if peers, _ := l.Peers(ctx); len(peers) != 0 {
				t.Fatal("non-device listed as a peer", peers)
			}
		} else if seen {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("goodbye did not remove the node", seen)
}
