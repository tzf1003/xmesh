package device

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tzf1003/xmesh/internal/spake2"
)

const password = "correct horse battery"

func pairing(t *testing.T, name string) *Pairing {
	t.Helper()
	p, err := OpenPairing(identity(t), name, trustStore(t))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func preparePair(t *testing.T, a, b *Pairing) PairConfirmation {
	t.Helper()
	challenge, err := b.Challenge()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := spake2.NewSecret(spake2.Derive(password, challenge.Salt))
	if err != nil {
		t.Fatal(err)
	}
	initiator, x, err := spake2.NewInitiator(secret, pairTranscript(a.document))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := b.Exchange(PairRequest{Identity: a.document, X: x})
	if err != nil {
		t.Fatal(err)
	}
	confirm, err := initiator.Finish(reply.Y, pairTranscript(reply.Identity), reply.Confirm)
	if err != nil {
		t.Fatal(err)
	}
	return PairConfirmation{SID: reply.SID, Confirm: confirm}
}

func TestPairingGenerationFencesPasswordRotationAndDisable(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(map[bool]string{false: "disable", true: "rotate"}[rotate], func(t *testing.T) {
			a, b := pairing(t, "a"), pairing(t, "b")
			if err := b.SetPassword(password); err != nil {
				t.Fatal(err)
			}
			confirmation := preparePair(t, a, b)
			var err error
			if rotate {
				err = b.SetPassword("a new connection password")
			} else {
				err = b.ClearPassword()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Confirm(t.Context(), confirmation); !errors.Is(err, ErrGeneration) {
				t.Fatal("old exchange committed", err)
			}
			if _, ok := b.trust.Find(a.identity.ID); ok {
				t.Fatal("old exchange established trust")
			}
		})
	}
}

func TestPairingConfirmationIsIdempotentAndRevocationWins(t *testing.T) {
	a, b := pairing(t, "a"), pairing(t, "b")
	if err := b.SetPassword(password); err != nil {
		t.Fatal(err)
	}
	confirmation := preparePair(t, a, b)
	if err := b.Confirm(t.Context(), confirmation); err != nil {
		t.Fatal(err)
	}
	revision := b.trust.Revision()
	if err := b.Confirm(t.Context(), confirmation); err != nil || b.trust.Revision() != revision {
		t.Fatal("repeated confirm changed trust", err)
	}
	if err := b.trust.Revoke(t.Context(), a.identity.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Confirm(t.Context(), confirmation); err == nil {
		t.Fatal("confirmation replay restored revoked trust")
	}
}

func TestPendingPairingCannotUndoRevocation(t *testing.T) {
	a, b := pairing(t, "a"), pairing(t, "b")
	if err := b.SetPassword(password); err != nil {
		t.Fatal(err)
	}
	if err := b.trust.Grant(t.Context(), a.document, []string{"tag:peer"}, nil); err != nil {
		t.Fatal(err)
	}
	confirmation := preparePair(t, a, b)
	if err := b.trust.Revoke(t.Context(), a.identity.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Confirm(t.Context(), confirmation); !errors.Is(err, ErrGeneration) {
		t.Fatal("pending pair undid revocation", err)
	}
}

func servePairing(t *testing.T, p *Pairing) string {
	t.Helper()
	s := httptest.NewUnstartedServer(p.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })))
	s.TLS = p.ServerTLS()
	s.StartTLS()
	t.Cleanup(s.Close)
	return s.URL
}

func TestPairOnceAuthorizesBothNetworkEndpoints(t *testing.T) {
	a, b := pairing(t, "a"), pairing(t, "b")
	if err := b.SetPassword(password); err != nil {
		t.Fatal(err)
	}
	lan, tailnet := servePairing(t, b), servePairing(t, b)
	bootstrap := &http.Client{Transport: &http.Transport{TLSClientConfig: a.BootstrapTLS()}}
	defer bootstrap.CloseIdleConnections()
	doc, err := a.PairWith(t.Context(), bootstrap, lan, password, b.identity.ID)
	if err != nil || doc.DeviceID != b.identity.ID {
		t.Fatal(doc, err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: a.trust.TLS(a.identity, b.identity.ID, false)}}
	defer client.CloseIdleConnections()
	for _, path := range []string{lan, tailnet} {
		r, err := client.Get(path + "/application")
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 204 {
			t.Fatal("paired device refused on alternate path", r.StatusCode)
		}
	}
	if err := b.trust.Revoke(t.Context(), a.identity.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{lan, tailnet} {
		r, err := client.Get(path + "/application")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 204 {
				t.Fatal("revoked device still works on alternate path")
			}
		}
	}
}

func TestPairingRejectsWrongExpectedDevice(t *testing.T) {
	a, b := pairing(t, "a"), pairing(t, "b")
	if err := b.SetPassword(password); err != nil {
		t.Fatal(err)
	}
	url := servePairing(t, b)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: a.BootstrapTLS()}}
	defer client.CloseIdleConnections()
	if _, err := a.PairWith(t.Context(), client, url, password, identity(t).ID); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatal(err)
	}
	if len(a.trust.Snapshot()) != 0 || len(b.trust.Snapshot()) != 0 {
		t.Fatal("mismatched identity trusted")
	}
}

func TestPairingBootstrapCannotEnterApplication(t *testing.T) {
	a, b := pairing(t, "a"), pairing(t, "b")
	url := servePairing(t, b)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, NextProtos: []string{PairALPN}, Certificates: []tls.Certificate{a.identity.Certificate}}}}
	defer client.CloseIdleConnections()
	r, err := client.Get(url + "/application")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatal("bootstrap reached application", r.StatusCode)
	}
}
