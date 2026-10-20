package mesh

import (
	"crypto/tls"
	"crypto/x509"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

// conformance: CON-NET-03
func Test_CONNET03_TsnetWithoutExternalClientRootOrTUN(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "tailnet.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	usesTsnet := false
	for _, imp := range parsed.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "tailscale.com/tsnet" {
			usesTsnet = true
		}
	}
	if !usesTsnet {
		t.Fatal("tailnet mesh does not use tailscale.com/tsnet")
	}
	var hits []string
	for _, root := range []string{"../../internal", "../../cmd"} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, _ := os.ReadFile(path)
			text := string(b)
			for _, forbidden := range []string{`"tailscale"`, `"tailscaled"`, "tailscale up", "tailscale.com/net/tstun", "wireguard/tun", "/dev/net/tun", "wintun", "tsnet.Server{TUN"} {
				if strings.Contains(text, forbidden) {
					hits = append(hits, path+": "+forbidden)
				}
			}
			return nil
		})
	}
	if len(hits) != 0 {
		t.Fatalf("code requires the Tailscale client or a TUN device: %v", hits)
	}
}

// conformance: CON-NET-09 CON-NET-02 CON-SEC-03
func Test_CONNET09_LANRejectsUnidentifiedAndUnpairedPeers(t *testing.T) {
	self, err := LoadCertificate(t.TempDir(), "self")
	if err != nil {
		t.Fatal(err)
	}
	paired, err := LoadCertificate(t.TempDir(), "paired")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := LoadCertificate(t.TempDir(), "stranger")
	if err != nil {
		t.Fatal(err)
	}
	trust, err := OpenTrust(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = trust.Add(Trusted{Name: "paired", Fingerprint: paired.Fingerprint, CertificatePEM: string(paired.PEM), Tags: []string{"tag:device"}}); err != nil {
		t.Fatal(err)
	}
	l := &LAN{name: "self", cert: self, trust: trust}
	expect := func(name string, request func() (*tls.ConnectionState, bool)) {
		t.Helper()
		r := httptest.NewRequest("POST", "/mcp", nil)
		if state, tlsPresent := request(); tlsPresent {
			r.TLS = state
		}
		_, err := l.Authenticate(r)
		if e, ok := err.(*toolerr.Error); !ok || e.Code != "unauthenticated" {
			t.Fatalf("%s: want unauthenticated, got %v", name, err)
		}
	}
	expect("plain HTTP", func() (*tls.ConnectionState, bool) { return nil, false })
	expect("TLS without client certificate", func() (*tls.ConnectionState, bool) { return &tls.ConnectionState{}, true })
	expect("unpaired certificate", func() (*tls.ConnectionState, bool) {
		return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{stranger.Leaf}}, true
	})
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{paired.Leaf}}
	r.Header.Set("X-Xmesh-Oauth-Client", "forged-client")
	p, err := l.Authenticate(r)
	if err != nil || p.Node != "lan-"+paired.Fingerprint[:24] || p.OAuthClient != "" {
		t.Fatalf("paired peer: %+v %v", p, err)
	}
	if len(p.Tags) != 1 || p.Tags[0] != "tag:device" {
		t.Fatalf("tags must come from the pairing record, not the request: %v", p.Tags)
	}
}

// conformance: CON-NET-01
func Test_CONNET01_LANListenerBindsOnlyAPrivateInterfaceAddress(t *testing.T) {
	cert, err := LoadCertificate(t.TempDir(), "lan-listen")
	if err != nil {
		t.Fatal(err)
	}
	trust, err := OpenTrust(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := &LAN{name: "lan-listen", cert: cert, trust: trust}
	listener, err := l.Listen(t.Context())
	if err != nil {
		// Hosts without a private IPv4 interface (or multicast) cannot open the
		// LAN listener at all; that is the safe direction of this property.
		t.Logf("LAN listener unavailable on this host: %v", err)
		return
	}
	defer func() {
		if l.advertisement != nil {
			l.advertisement.Close()
		}
		listener.Close()
	}()
	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || !ip.IsPrivate() {
		t.Fatalf("LAN agent listener bound to %s, not a private interface address", host)
	}
}

func Test_PairingALPNRechecksTrustedCertificateValidity(t *testing.T) {
	node := newPairingNode(t, "server")
	cert, err := LoadCertificate(t.TempDir(), "peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := node.trust.Add(Trusted{Name: "peer", Fingerprint: cert.Fingerprint, CertificatePEM: string(cert.PEM), Tags: []string{"tag:device"}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/mcp", nil)
	leaf := *cert.Leaf
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{&leaf}}
	if _, err := node.Authenticate(r); err != nil || !node.trustedRequest(r) {
		t.Fatalf("valid identity: %v", err)
	}
	for _, name := range []string{"expired", "future", "wrong-name"} {
		t.Run(name, func(t *testing.T) {
			leaf = *cert.Leaf
			switch name {
			case "expired":
				leaf.NotAfter = time.Now().Add(-time.Hour)
			case "future":
				leaf.NotBefore = time.Now().Add(time.Hour)
			case "wrong-name":
				leaf.Subject.CommonName = "other"
			}
			if _, err := node.Authenticate(r); err == nil || node.trustedRequest(r) {
				t.Fatal("invalid pinned certificate accepted")
			}
		})
	}
}
