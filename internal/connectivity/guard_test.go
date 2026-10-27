package connectivity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
)

type fakeConn struct {
	net.Conn
	addr net.Addr
}

func (c fakeConn) RemoteAddr() net.Addr { return c.addr }

func conn(ip string) net.Conn {
	return fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 1000}}
}

func TestDirectGuardAllowList(t *testing.T) {
	g := newDirectGuard(config.Direct{AllowIPs: []string{"10.0.0.0/8", "203.0.113.7"}}, time.Now)
	for ip, want := range map[string]bool{"10.1.2.3": true, "203.0.113.7": true, "203.0.113.8": false, "8.8.8.8": false} {
		if got := g.admit(conn(ip)); got != want {
			t.Fatalf("%s: admit = %v, want %v", ip, got, want)
		}
	}
}

func TestDirectGuardCapsPreauthAndBacksOffFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	g := newDirectGuard(config.Direct{MaxPreauth: 3}, func() time.Time { return now })
	a, b, c := conn("1.1.1.1"), conn("2.2.2.2"), conn("3.3.3.3")
	if !g.admit(a) || !g.admit(b) || !g.admit(c) {
		t.Fatal("connections under the cap must be admitted")
	}
	if g.admit(conn("4.4.4.4")) {
		t.Fatal("connection over the global pre-auth cap must be refused")
	}
	g.connState(a, http.StateActive) // authenticated: slot freed, no penalty
	if !g.admit(conn("1.1.1.1")) {
		t.Fatal("a source that authenticated must not be penalised")
	}
	g.connState(b, http.StateClosed) // never sent a request
	if g.admit(conn("2.2.2.2")) {
		t.Fatal("a source whose connection failed must back off")
	}
	now = now.Add(2 * time.Second)
	if !g.admit(conn("2.2.2.2")) {
		t.Fatal("the backoff must expire")
	}
}

func TestDirectGuardPerSourceLimit(t *testing.T) {
	g := newDirectGuard(config.Direct{MaxPreauth: 100}, time.Now)
	for i := 0; i < guardPerIPPreauth; i++ {
		if !g.admit(conn("9.9.9.9")) {
			t.Fatalf("connection %d refused too early", i)
		}
	}
	if g.admit(conn("9.9.9.9")) {
		t.Fatal("one source must not exceed its pre-auth share")
	}
}

// The guard must key connections on what http.Server reports to ConnState, which is the TLS-wrapped conn.
func TestDirectGuardSettlesConnectionsOverTLSListener(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := newDirectGuard(config.Direct{MaxPreauth: 2}, time.Now)
	cert := selfSignedCert(t)
	l := g.listener(tls.NewListener(raw, &tls.Config{Certificates: []tls.Certificate{cert}}))
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), ConnState: g.connState}
	go func() { _ = server.Serve(l) }()
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableKeepAlives: true}}
	for i := 0; i < 5; i++ {
		resp, err := client.Get("https://" + raw.Addr().String())
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total != 0 || len(g.conns) != 0 {
		t.Fatalf("slots leaked after settled requests: total=%d conns=%d", g.total, len(g.conns))
	}
}

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "guard"}, NotAfter: time.Now().Add(time.Hour)}, &x509.Certificate{}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
