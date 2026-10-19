package device

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

func identity(t *testing.T) *Identity {
	t.Helper()
	i, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func trustStore(t *testing.T) *TrustStore {
	t.Helper()
	s, err := OpenTrust(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTrustConcurrentIndependentWritersKeepEveryPair(t *testing.T) {
	root := t.TempDir()
	var writers []*TrustStore
	var docs []Document
	for range 8 {
		s, err := OpenTrust(root)
		if err != nil {
			t.Fatal(err)
		}
		writers = append(writers, s)
		docs = append(docs, identity(t).Document("workstation"))
	}
	var wg sync.WaitGroup
	for n, s := range writers {
		wg.Go(func() {
			if err := s.Grant(t.Context(), docs[n], []string{"tag:peer", "tag:device"}, nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	s, err := OpenTrust(root)
	if err != nil {
		t.Fatal(err)
	}
	all := s.Snapshot()
	if len(all) != len(writers) {
		t.Fatalf("pairs lost: %d", len(all))
	}
	aliases := map[string]bool{}
	for _, p := range all {
		if aliases[p.Alias] || !deviceName.MatchString(p.Alias) {
			t.Fatal("duplicate or invalid alias", p.Alias)
		}
		aliases[p.Alias] = true
	}
}

func TestRevocationFencesPendingPairingAndPreservesAlias(t *testing.T) {
	s := trustStore(t)
	doc := identity(t).Document("before")
	if err := s.Grant(t.Context(), doc, []string{"tag:peer"}, nil); err != nil {
		t.Fatal(err)
	}
	revision := s.Revision()
	if err := s.Revoke(t.Context(), doc.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(t.Context(), doc, []string{"tag:admin"}, &revision); !errors.Is(err, ErrGeneration) {
		t.Fatal("stale pairing accepted", err)
	}
	if _, ok := s.Find(doc.DeviceID); ok {
		t.Fatal("revoked device trusted")
	}
	doc.Name = "renamed"
	current := s.Revision()
	if err := s.Grant(t.Context(), doc, []string{"tag:peer"}, &current); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Find(doc.DeviceID)
	if !ok || got.Alias != "before" || got.Document.Name != "renamed" {
		t.Fatalf("alias changed: %+v", got)
	}
}

func TestTrustSubscribersReceiveLatestRevisionIndependently(t *testing.T) {
	s := trustStore(t)
	a, stopA := s.Subscribe()
	b, stopB := s.Subscribe()
	defer stopA()
	defer stopB()
	for range 3 {
		if err := s.Grant(t.Context(), identity(t).Document("device"), []string{"tag:peer"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, ch := range []<-chan uint64{a, b} {
		select {
		case got := <-ch:
			if got != s.Revision() {
				t.Fatal("old revision", got)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber missed change")
		}
	}
	stopA()
	stopA()
}

func TestTrustSnapshotsCannotMutateRolesOrKeys(t *testing.T) {
	s := trustStore(t)
	doc := identity(t).Document("device")
	if err := s.Grant(t.Context(), doc, []string{"tag:peer"}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Find(doc.DeviceID)
	got.Tags[0] = "tag:admin"
	got.Document.PublicKey[0] ^= 1
	all := s.Snapshot()
	if !slices.Equal(all[0].Tags, []string{"tag:peer"}) || all[0].Document.Validate() != nil {
		t.Fatal("mutable trust escaped")
	}
}

func TestDeviceMTLSWorksAfterCertificateRenewalAndRevocation(t *testing.T) {
	a, b := identity(t), identity(t)
	as, bs := trustStore(t), trustStore(t)
	if err := as.Grant(t.Context(), b.Document("target"), []string{"tag:peer"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := bs.Grant(t.Context(), a.Document("source"), []string{"tag:peer"}, nil); err != nil {
		t.Fatal(err)
	}
	// A new certificate on the same persistent key has the same trust on either path.
	b, err := identityForKey(b.privateKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := bs.VerifyCertificate(r.TLS.PeerCertificates[0], a.ID); err != nil {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(204)
	}))
	server.TLS = bs.TLS(b, "", true)
	server.StartTLS()
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: as.TLS(a, b.ID, false)}}
	defer client.CloseIdleConnections()
	r, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	if err := bs.Revoke(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	r, err = client.Get(server.URL)
	if err == nil {
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatal("existing TLS connection kept revoked trust", r.StatusCode)
		}
	}
	client.CloseIdleConnections()
	if r, err := client.Get(server.URL); err == nil {
		r.Body.Close()
		t.Fatal("revoked key opened a new TLS connection")
	}
	wrong := identity(t)
	if err := as.TLS(a, wrong.ID, false).VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{b.Certificate.Leaf}}); err == nil {
		t.Fatal("wrong expected device accepted")
	}
}
