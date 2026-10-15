package mesh

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/tzf1003/xmesh/internal/identity"
	"net"
	"path/filepath"
	"testing"
	"time"
)

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
func Test_PairFingerprintAndPermissions(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	path := filepath.Join(t.TempDir(), "peer.json")
	fp, e := ExportPair(a, "one", path)
	if e != nil {
		t.Fatal(e)
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
