package mesh

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/mdns"
)

const testPassword = "correct horse battery"

// newPairingNode is a LAN node with its own state directory. It is not listening; startAgent does that.
func newPairingNode(t *testing.T, name string) *LAN {
	t.Helper()
	dir := t.TempDir()
	cert, err := LoadCertificate(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := OpenTrust(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &LAN{name: name, cert: cert, trust: trust, peers: map[string]lanPeer{}, presence: map[string]lanPeer{}, pairing: lanPairing{dir: dir}, changed: make(chan struct{}, 1), done: make(chan struct{})}
}

// startAgent serves the agent listener of l on loopback: the same TLS configuration and dispatch as
// the kernel, with a stub MCP handler that authenticates like the real one.
func startAgent(t *testing.T, l *LAN) string {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", l.serverTLS())
	if err != nil {
		t.Fatal(err)
	}
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := l.Authenticate(r); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	server := &http.Server{Handler: l.ServeAgent(mcp)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String()
}

func pairErrorReason(t *testing.T, err error) PairErrorReason {
	t.Helper()
	var pe *PairError
	if !errors.As(err, &pe) {
		t.Fatalf("want a *PairError, got %T %v", err, err)
	}
	return pe.Reason
}

func TestPairingWithThePasswordPinsBothSides(t *testing.T) {
	b := newPairingNode(t, "node-b")
	a := newPairingNode(t, "node-a")
	if _, err := b.SetPairPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	addr := startAgent(t, b)

	peer, err := a.PairWith(t.Context(), addr, testPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	if peer.Name != "node-b" || peer.Fingerprint != b.cert.Fingerprint {
		t.Fatalf("pinned the wrong peer: %+v", peer)
	}
	if want := []string{"tag:peer", "tag:device"}; len(peer.Tags) != 2 || peer.Tags[0] != want[0] || peer.Tags[1] != want[1] {
		t.Fatalf("tags %v, want %v", peer.Tags, want)
	}
	if _, ok := b.trust.Find(a.cert.Fingerprint); !ok {
		t.Fatal("responder did not pin the initiator")
	}
	if _, ok := a.trust.Find(b.cert.Fingerprint); !ok {
		t.Fatal("initiator did not pin the responder")
	}
	// The pinned pair must now speak mutual TLS on the ordinary path.
	client := a.HTTPClient(Peer{ID: "lan-" + b.cert.Fingerprint[:24], Name: "node-b", Fingerprint: b.cert.Fingerprint, Address: "https://" + addr + "/mcp"})
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + addr + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("paired mTLS request status %d", resp.StatusCode)
	}
}

func TestPairingWithAnotherCertificateStopsBeforeConfirming(t *testing.T) {
	b := newPairingNode(t, "node-b")
	a := newPairingNode(t, "node-a")
	if _, err := b.SetPairPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	addr := startAgent(t, b)

	// The password is right, but the user named some other device: nothing is confirmed or pinned.
	_, err := a.PairWith(t.Context(), addr, testPassword, strings.Repeat("0", 64))
	if reason := pairErrorReason(t, err); reason != PairFingerprintMismatch {
		t.Fatalf("reason %s, want fingerprint_mismatch", reason)
	}
	if _, ok := a.trust.Find(b.cert.Fingerprint); ok {
		t.Fatal("initiator pinned a device the user did not name")
	}
	if _, ok := b.trust.Find(a.cert.Fingerprint); ok {
		t.Fatal("responder pinned after a fingerprint mismatch")
	}
	// The responder cannot tell an abandoned exchange from a wrong guess, so the exchange counts.
	if got := b.PairPasswordState().FailedAttempts; got != 1 {
		t.Fatalf("failed attempts %d, want the one exchange counted", got)
	}
	if _, err := a.PairWith(t.Context(), addr, testPassword, b.cert.Fingerprint); err != nil {
		t.Fatalf("the named device pairs: %v", err)
	}
}

func TestWrongPasswordPinsNothingAndIsCounted(t *testing.T) {
	b := newPairingNode(t, "node-b")
	a := newPairingNode(t, "node-a")
	if _, err := b.SetPairPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	addr := startAgent(t, b)

	_, err := a.PairWith(t.Context(), addr, "wrong password here", "")
	if reason := pairErrorReason(t, err); reason != PairWrongPassword {
		t.Fatalf("reason %s, want wrong_password", reason)
	}
	if _, ok := a.trust.Find(b.cert.Fingerprint); ok {
		t.Fatal("initiator pinned after a wrong password")
	}
	if _, ok := b.trust.Find(a.cert.Fingerprint); ok {
		t.Fatal("responder pinned after a wrong password")
	}
	if got := b.PairPasswordState().FailedAttempts; got != 1 {
		t.Fatalf("failed attempts %d, want 1", got)
	}
}

func TestPairingWithoutAPasswordSaysSo(t *testing.T) {
	b := newPairingNode(t, "node-b")
	a := newPairingNode(t, "node-a")
	addr := startAgent(t, b)
	if _, err := a.PairWith(t.Context(), addr, testPassword, ""); pairErrorReason(t, err) != PairNoPassword {
		t.Fatalf("want no_password, got %v", err)
	}
}

func TestSuccessfulPairingClearsTheCount(t *testing.T) {
	b := newPairingNode(t, "node-b")
	a := newPairingNode(t, "node-a")
	if _, err := b.SetPairPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	addr := startAgent(t, b)
	for range 3 {
		if _, err := a.PairWith(t.Context(), addr, "wrong password here", ""); err == nil {
			t.Fatal("wrong password accepted")
		}
	}
	if got := b.PairPasswordState().FailedAttempts; got != 3 {
		t.Fatalf("failed attempts %d, want 3", got)
	}
	if _, err := a.PairWith(t.Context(), addr, testPassword, ""); err != nil {
		t.Fatal(err)
	}
	if got := b.PairPasswordState().FailedAttempts; got != 0 {
		t.Fatalf("failed attempts %d after success, want 0", got)
	}
}

func TestRepeatedGuessesAreLockedOut(t *testing.T) {
	b := newPairingNode(t, "node-b")
	a := newPairingNode(t, "node-a")
	if _, err := b.SetPairPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	addr := startAgent(t, b)
	for range pairFreeAttempts {
		if _, err := a.PairWith(t.Context(), addr, "wrong password here", ""); pairErrorReason(t, err) != PairWrongPassword {
			t.Fatalf("want wrong_password, got %v", err)
		}
	}
	_, err := a.PairWith(t.Context(), addr, testPassword, "")
	var pe *PairError
	if !errors.As(err, &pe) || pe.Reason != PairRateLimited || pe.RetryAfter <= 0 {
		t.Fatalf("locked node answered %v; want rate_limited with a retry time", err)
	}
	st := b.PairPasswordState()
	if st.LockedUntil.IsZero() || !st.LockedUntil.After(time.Now()) {
		t.Fatalf("lockout not reported: %+v", st)
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	cases := map[int]time.Duration{
		pairFreeAttempts:     30 * time.Second,
		pairFreeAttempts + 1: time.Minute,
		pairFreeAttempts + 2: 2 * time.Minute,
		pairFreeAttempts + 3: 4 * time.Minute,
		pairFreeAttempts + 4: 8 * time.Minute,
		pairFreeAttempts + 5: 16 * time.Minute,
		pairFreeAttempts + 6: pairBackoffMax,
		100:                  pairBackoffMax,
	}
	for failures, want := range cases {
		if got := pairBackoff(failures); got != want {
			t.Fatalf("pairBackoff(%d) = %v, want %v", failures, got, want)
		}
	}
}

func TestBeginRefusesUntilTheLockoutEnds(t *testing.T) {
	var p lanPairing
	now := time.Unix(1_700_000_000, 0)
	for range pairFreeAttempts {
		if _, ok := p.begin(now); !ok {
			t.Fatal("attempt refused before the limit")
		}
	}
	wait, ok := p.begin(now)
	if ok || wait != pairBackoffBase {
		t.Fatalf("locked attempt: ok=%v wait=%v", ok, wait)
	}
	if _, ok := p.begin(now.Add(pairBackoffBase)); !ok {
		t.Fatal("attempt refused after the lockout ended")
	}
}

func TestSetPairPasswordRules(t *testing.T) {
	l := newPairingNode(t, "node-p")
	for _, bad := range []string{"", "short", " leading space is not allowed", "trailing space is not allowed ", strings.Repeat("x", pairMaxRunes+1)} {
		if _, err := l.SetPairPassword(bad); !errors.Is(err, ErrPairPassword) {
			t.Fatalf("%q accepted: %v", bad, err)
		}
	}
	if _, err := l.SetPairPassword(strings.Repeat("ä", pairMinRunes)); err != nil {
		t.Fatalf("ten runes rejected: %v", err)
	}
}

func TestPasswordIsStoredDerivedAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	cert, err := LoadCertificate(dir, "node-s")
	if err != nil {
		t.Fatal(err)
	}
	trust, _ := OpenTrust(dir)
	l := &LAN{name: "node-s", cert: cert, trust: trust, pairing: lanPairing{dir: dir}, changed: make(chan struct{}, 1)}
	if _, err := l.SetPairPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, pairPasswordFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testPassword) {
		t.Fatal("the password is written to disk in plain text")
	}
	info, err := os.Stat(filepath.Join(dir, pairPasswordFile))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix mode bits; the state directory's ACL protects the file there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("password file mode %v, want 0600", info.Mode().Perm())
	}
	reopened, err := OpenLAN(t.Context(), "node-s", dir, LANOptions{MDNS: false})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.PairPasswordState().Set {
		t.Fatal("password not loaded after restart")
	}
	if err := reopened.ClearPairPassword(); err != nil {
		t.Fatal(err)
	}
	if reopened.PairPasswordState().Set {
		t.Fatal("password still set after clear")
	}
	if _, err := os.Stat(filepath.Join(dir, pairPasswordFile)); !os.IsNotExist(err) {
		t.Fatal("password file not removed")
	}
}

func TestClearedPasswordRefusesPairing(t *testing.T) {
	b := newPairingNode(t, "node-b")
	a := newPairingNode(t, "node-a")
	if _, err := b.SetPairPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := b.ClearPairPassword(); err != nil {
		t.Fatal(err)
	}
	addr := startAgent(t, b)
	if _, err := a.PairWith(t.Context(), addr, testPassword, ""); pairErrorReason(t, err) != PairNoPassword {
		t.Fatalf("cleared node answered %v", err)
	}
}

// An ordinary handshake still fails for a certificate that is not paired (CON-NET-09), and the
// pairing handshake reaches only the pairing routes.
func TestPairingHandshakeReachesOnlyPairingRoutes(t *testing.T) {
	b := newPairingNode(t, "node-b")
	stranger := newPairingNode(t, "stranger")
	addr := startAgent(t, b)

	// TLS 1.3 lets the client finish its side before the server rejects the certificate, so the
	// refusal shows on the first request rather than in tls.Dial.
	ordinary := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, Certificates: []tls.Certificate{stranger.cert.Certificate}}, DisableKeepAlives: true}}
	defer ordinary.CloseIdleConnections()
	if resp, err := ordinary.Get("https://" + addr + "/mcp"); err == nil {
		resp.Body.Close()
		t.Fatal("ordinary handshake with an unpaired certificate succeeded")
	}

	pairing := stranger.pairingClient()
	defer pairing.CloseIdleConnections()
	resp, err := pairing.Post("https://"+addr+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("pairing handshake reached /mcp: %d", resp.StatusCode)
	}
	resp, err = pairing.Post("https://"+addr+PairPath+"challenge", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("challenge without a password: %d, want 409", resp.StatusCode)
	}
}

// A certificate that is not trusted reaches the pairing routes and nothing else.
func TestUntrustedRequestsReachNothingButPairing(t *testing.T) {
	b := newPairingNode(t, "node-b")
	stranger := newPairingNode(t, "stranger")
	if _, err := b.SetPairPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	addr := startAgent(t, b)
	pairing := stranger.pairingClient()
	defer pairing.CloseIdleConnections()
	resp, err := pairing.Post("https://"+addr+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("untrusted request reached /mcp: %d", resp.StatusCode)
	}
	resp, err = pairing.Post("https://"+addr+PairPath+"challenge", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("challenge for an untrusted peer: %d, want 200", resp.StatusCode)
	}
}

// A trusted peer reaches the agent route through the same listener.
func TestTrustedRequestsReachTheAgentRoute(t *testing.T) {
	b := newPairingNode(t, "node-b")
	a := newPairingNode(t, "node-a")
	if err := b.trust.Add(Trusted{Name: a.name, Fingerprint: a.cert.Fingerprint, CertificatePEM: string(a.cert.PEM), Tags: []string{"tag:device"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.trust.Add(Trusted{Name: b.name, Fingerprint: b.cert.Fingerprint, CertificatePEM: string(b.cert.PEM), Tags: []string{"tag:device"}}); err != nil {
		t.Fatal(err)
	}
	addr := startAgent(t, b)
	client := a.HTTPClient(Peer{ID: "lan-" + b.cert.Fingerprint[:24], Name: "node-b", Fingerprint: b.cert.Fingerprint, Address: "https://" + addr + "/mcp"})
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + addr + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trusted peer refused on /mcp: %d", resp.StatusCode)
	}
}

func TestDiscoveryListsOnlyUnpairedPairingDevices(t *testing.T) {
	self := newPairingNode(t, "self")
	paired := newPairingNode(t, "paired")
	stranger := newPairingNode(t, "stranger")
	legacy := newPairingNode(t, "legacy")
	if err := self.trust.Add(Trusted{Name: paired.name, Fingerprint: paired.cert.Fingerprint, CertificatePEM: string(paired.cert.PEM), Tags: []string{"tag:device"}}); err != nil {
		t.Fatal(err)
	}
	announce := func(n *LAN, pairing bool, ttl uint32) *mdns.Entry {
		text := []string{"fingerprint=" + n.cert.Fingerprint}
		if pairing {
			text = append(text, "pairing=1")
		}
		return &mdns.Entry{Instance: n.name, Port: AgentPort, Text: text, TTL: ttl, AddrIPv4: []net.IP{net.ParseIP("192.168.31.20")}}
	}
	entries := make(chan *mdns.Entry, 8)
	entries <- announce(paired, true, 120)
	entries <- announce(stranger, true, 120)
	entries <- announce(legacy, false, 120)
	entries <- announce(self, true, 120)
	close(entries)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	self.discover(ctx, entries)

	devices, err := self.Discovered(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("discovered %+v, want only the unpaired pairing-capable stranger", devices)
	}
	if devices[0].Name != "stranger" || devices[0].Fingerprint != stranger.cert.Fingerprint || devices[0].Address != "192.168.31.20:"+strconv.Itoa(AgentPort) {
		t.Fatalf("wrong discovered device: %+v", devices[0])
	}

	goodbye := make(chan *mdns.Entry, 1)
	goodbye <- announce(stranger, true, 0)
	close(goodbye)
	self.discover(ctx, goodbye)
	if devices, _ = self.Discovered(ctx); len(devices) != 0 {
		t.Fatalf("goodbye did not remove the device: %+v", devices)
	}
}

func TestPairingRejectsAnUnreachablePeer(t *testing.T) {
	a := newPairingNode(t, "node-a")
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	server.Start()
	defer server.Close()
	_, err := a.PairWith(t.Context(), server.Listener.Addr().String(), testPassword, "")
	if err == nil {
		t.Fatal("plain HTTP peer accepted")
	}
	if _, isPair := err.(*PairError); !isPair {
		t.Fatalf("want a PairError, got %T %v", err, err)
	}
}

func Test_SelfPairingIsRejectedBeforeTrustConfirmation(t *testing.T) {
	self := newPairingNode(t, "self")
	if _, err := self.SetPairPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	addr := startAgent(t, self)
	if _, err := self.PairWith(t.Context(), addr, testPassword, ""); err == nil {
		t.Fatal("self pairing accepted")
	}
	if _, ok := self.trust.Find(self.cert.Fingerprint); ok {
		t.Fatal("self certificate pinned before rejection")
	}
}
