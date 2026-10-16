package meshadmin

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"tailscale.com/net/stun"
)

// fakeDERP serves what derper serves for health probes: /derp/probe over TLS and
// STUN over UDP. The httptest certificate is valid for example.com and 127.0.0.1.
func fakeDERP(t *testing.T, withSTUN bool) (spec DERPRegionSpec, roots *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/derp/probe" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	roots = x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	spec = DERPRegionSpec{ID: 900, Code: "lab", Name: "Lab", Hostname: "example.com", IPv4: "127.0.0.1", DERPPort: port, STUNPort: 1}
	if withSTUN {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pc.Close() })
		spec.STUNPort = pc.LocalAddr().(*net.UDPAddr).Port
		go func() {
			buf := make([]byte, 1500)
			for {
				n, addr, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				if txid, err := stun.ParseBindingRequest(buf[:n]); err == nil {
					ap := addr.(*net.UDPAddr).AddrPort()
					_, _ = pc.WriteTo(stun.Response(txid, ap), addr)
				}
			}
		}()
	}
	return spec, roots
}

func checkNamed(r ProbeResult, name string) ProbeCheck {
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	return ProbeCheck{Name: name + "-missing"}
}

// conformance: CTR-MESH-04
func TestProbeHealthyRegion(t *testing.T) {
	spec, roots := fakeDERP(t, true)
	r := ProbeDERP(context.Background(), spec, ProbeOptions{RootCAs: roots, Timeout: 3 * time.Second})
	if !r.OK {
		t.Fatalf("healthy region failed: %+v", r)
	}
	for _, n := range []string{"tcp", "tls", "http", "stun"} {
		if c := checkNamed(r, n); !c.OK {
			t.Errorf("%s: %+v", n, c)
		}
	}
}

// conformance: CTR-MESH-04
func TestProbeCertificateNameMismatch(t *testing.T) {
	spec, roots := fakeDERP(t, true)
	spec.Hostname = "derp.other.test" // the certificate is for example.com
	r := ProbeDERP(context.Background(), spec, ProbeOptions{RootCAs: roots, Timeout: 3 * time.Second})
	if r.OK {
		t.Fatal("mismatched certificate passed")
	}
	c := checkNamed(r, "tls")
	if c.OK || !strings.Contains(c.Detail, "derp.other.test") || !strings.Contains(c.Detail, "x509") {
		t.Fatalf("tls detail should name the verification failure: %+v", c)
	}
	if !checkNamed(r, "tcp").OK {
		t.Fatal("tcp should still pass")
	}
	// A CertName override fixes it.
	spec.CertName = "example.com"
	if r := ProbeDERP(context.Background(), spec, ProbeOptions{RootCAs: roots, Timeout: 3 * time.Second}); !r.OK {
		t.Fatalf("CertName override: %+v", r)
	}
}

func TestProbeUnreachableAndNoSTUN(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	r := ProbeDERP(context.Background(), DERPRegionSpec{Hostname: "example.com", IPv4: "127.0.0.1", DERPPort: port}, ProbeOptions{Timeout: time.Second})
	if r.OK || checkNamed(r, "tcp").OK || !strings.Contains(checkNamed(r, "tcp").Detail, "cannot connect") {
		t.Fatalf("closed port: %+v", r)
	}
	spec, roots := fakeDERP(t, false) // STUN port has nothing listening
	r = ProbeDERP(context.Background(), spec, ProbeOptions{RootCAs: roots, Timeout: 700 * time.Millisecond})
	if r.OK || checkNamed(r, "stun").OK || !checkNamed(r, "http").OK {
		t.Fatalf("missing STUN should fail only the stun check: %+v", r)
	}
	if r := ProbeDERP(context.Background(), spec, ProbeOptions{RootCAs: roots, SkipSTUN: true, Timeout: time.Second}); !r.OK {
		t.Fatalf("SkipSTUN: %+v", r)
	}
}
