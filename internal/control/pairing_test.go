package control

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/mesh"
)

const (
	pairPeerFingerprint  = "3f2a9c4e1b7d8a60f5e2c9b1d4a7e8f03c6b5a29d1e4f7a8b0c3d6e9f2a5b8c1"
	pairOtherFingerprint = "9b1e4c7a2d5f8e3b6a9c0d4f7e1b8a5c2d6f9e3b7a0c4d8e1f5a9b2c6d0e3f7a"
	pairTestPassword     = "correct horse battery staple"
)

// fakePairing is the LAN pairing with the behaviour the handlers rely on. connectErr, when set,
// is what PairWith returns instead of pairing.
type fakePairing struct {
	mu         sync.Mutex
	set        bool
	updated    time.Time
	locked     time.Time
	failures   int
	found      []mesh.DiscoveredPeer
	connectErr error
	pairedWith string
}

func (p *fakePairing) PairPasswordState() mesh.PairPasswordState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return mesh.PairPasswordState{Set: p.set, UpdatedAt: p.updated, FailedAttempts: p.failures, LockedUntil: p.locked}
}

func (p *fakePairing) SetPairPassword(password string) (mesh.PairPasswordState, error) {
	if err := mesh.ValidatePairPassword(password); err != nil {
		return mesh.PairPasswordState{}, err
	}
	p.mu.Lock()
	p.set, p.updated = true, time.Now().UTC().Truncate(time.Millisecond)
	p.mu.Unlock()
	return p.PairPasswordState(), nil
}

func (p *fakePairing) ClearPairPassword() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.set = false
	return nil
}

func (p *fakePairing) Discovered(context.Context) ([]mesh.DiscoveredPeer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]mesh.DiscoveredPeer(nil), p.found...), nil
}

func (p *fakePairing) PairWith(_ context.Context, address, password, expected string) (mesh.Trusted, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pairedWith = address
	if p.connectErr != nil {
		return mesh.Trusted{}, p.connectErr
	}
	if password != pairTestPassword {
		return mesh.Trusted{}, &mesh.PairError{Reason: mesh.PairWrongPassword}
	}
	if expected != "" && expected != pairPeerFingerprint {
		return mesh.Trusted{}, &mesh.PairError{Reason: mesh.PairFingerprintMismatch}
	}
	return mesh.Trusted{Name: "peer-b", Fingerprint: pairPeerFingerprint, CertificatePEM: "-----BEGIN CERTIFICATE-----\n", Tags: []string{"tag:peer", "tag:device"}}, nil
}

// conformance: CTR-CTL-08 the connection-password routes exist only in lan mode, and the password
// never comes back in a response.
func Test_CTRCTL08_PairPasswordRoutesNeedLANMode(t *testing.T) {
	f := newFixtureConfig(t, tailnetConfig)
	apiError(t, f.do("GET", "/v1/pair/password", nil, nil), v1.ErrUnsupportedMode)
	apiError(t, f.do("PUT", "/v1/pair/password", v1.PairPasswordRequest{Password: pairTestPassword}, nil), v1.ErrUnsupportedMode)
	apiError(t, f.do("DELETE", "/v1/pair/password", nil, nil), v1.ErrUnsupportedMode)
	apiError(t, f.do("GET", "/v1/pair/discovered", nil, nil), v1.ErrUnsupportedMode)
	apiError(t, f.do("POST", "/v1/pair/connect", v1.PairConnectRequest{Address: "10.0.0.2", Password: pairTestPassword}, nil), v1.ErrUnsupportedMode)
}

// conformance: CTR-CTL-08
func Test_CTRCTL08_PairPasswordLifecycle(t *testing.T) {
	plain := newFixture(t)
	apiError(t, plain.do("GET", "/v1/pair/password", nil, nil), v1.ErrUnavailable)
	plain.server.Close() // the pipe name is global on Windows: one server per node at a time

	admin := &fakePairing{}
	f := newFixture(t, func(o *Options) { o.Pairing = admin })

	var state v1.PairPasswordState
	decodeAs(t, f.ok("GET", "/v1/pair/password", nil, "PairPasswordState"), &state)
	if state.Set || state.UpdatedAt != nil || state.LockedUntil != nil || state.FailedAttempts != 0 {
		t.Fatalf("a new node has no password: %+v", state)
	}

	apiError(t, f.do("PUT", "/v1/pair/password", v1.PairPasswordRequest{Password: "short"}, nil), v1.ErrInvalidArgs)
	apiError(t, f.do("PUT", "/v1/pair/password", v1.PairPasswordRequest{Password: " " + pairTestPassword}, nil), v1.ErrInvalidArgs)

	put := f.ok("PUT", "/v1/pair/password", v1.PairPasswordRequest{Password: pairTestPassword}, "PairPasswordState")
	if strings.Contains(string(put.Body), pairTestPassword) {
		t.Fatalf("the response echoes the password: %s", put.Body)
	}
	decodeAs(t, put, &state)
	if !state.Set || state.UpdatedAt == nil {
		t.Fatalf("after a set the password is set: %+v", state)
	}

	if r := f.do("DELETE", "/v1/pair/password", nil, nil); r.Status != 204 {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	decodeAs(t, f.ok("GET", "/v1/pair/password", nil, "PairPasswordState"), &state)
	if state.Set {
		t.Fatalf("after a delete the password is not set: %+v", state)
	}
}

// conformance: CTR-CTL-08
func Test_CTRCTL08_PairPasswordReportsLockout(t *testing.T) {
	until := time.Now().Add(time.Minute).UTC().Truncate(time.Millisecond)
	admin := &fakePairing{set: true, updated: time.Now().UTC().Truncate(time.Millisecond), failures: 5, locked: until}
	f := newFixture(t, func(o *Options) { o.Pairing = admin })
	var state v1.PairPasswordState
	decodeAs(t, f.ok("GET", "/v1/pair/password", nil, "PairPasswordState"), &state)
	if state.FailedAttempts != 5 || state.LockedUntil == nil || !state.LockedUntil.Equal(until) {
		t.Fatalf("lockout is not reported: %+v", state)
	}
}

// conformance: CTR-CTL-08
func Test_CTRCTL08_PairDiscoveryAndConnect(t *testing.T) {
	admin := &fakePairing{found: []mesh.DiscoveredPeer{{Name: "peer-b", Fingerprint: pairPeerFingerprint, Address: "192.168.31.15:7433", SeenAt: time.Now()}}}
	f := newFixture(t, func(o *Options) { o.Pairing = admin })

	var listed v1.PairDiscoveredResponse
	decodeAs(t, f.ok("GET", "/v1/pair/discovered", nil, "PairDiscoveredResponse"), &listed)
	if len(listed.Devices) != 1 || listed.Devices[0].Name != "peer-b" || listed.Devices[0].Address != "192.168.31.15:7433" {
		t.Fatalf("discovered: %+v", listed)
	}

	apiError(t, f.do("POST", "/v1/pair/connect", v1.PairConnectRequest{Password: pairTestPassword}, nil), v1.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v1/pair/connect", v1.PairConnectRequest{Fingerprint: "not-a-fingerprint", Password: pairTestPassword}, nil), v1.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v1/pair/connect", v1.PairConnectRequest{Address: "192.168.31.15:7433", Password: "short"}, nil), v1.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v1/pair/connect", v1.PairConnectRequest{Fingerprint: pairOtherFingerprint, Password: pairTestPassword}, nil), v1.ErrNotFound)
	// A named address with a different expected certificate stops before anything is confirmed.
	apiError(t, f.do("POST", "/v1/pair/connect", v1.PairConnectRequest{Address: "192.168.31.15:7433", Fingerprint: pairOtherFingerprint, Password: pairTestPassword}, nil), v1.ErrInvalidArgs)

	// With only a fingerprint, the address comes from the discovery list.
	var connected v1.PairConnectResponse
	decodeAs(t, f.ok("POST", "/v1/pair/connect", v1.PairConnectRequest{Fingerprint: pairPeerFingerprint, Password: pairTestPassword}, "PairConnectResponse"), &connected)
	if admin.pairedWith != "192.168.31.15:7433" || connected.Name != "peer-b" || connected.Address != "192.168.31.15:7433" || len(connected.Tags) != 2 {
		t.Fatalf("connect: paired with %q, response %+v", admin.pairedWith, connected)
	}
	if strings.Contains(string(f.do("GET", "/v1/pair/discovered", nil, nil).Body), pairTestPassword) {
		t.Fatal("discovery leaks the password")
	}
}

// conformance: CTR-CTL-08
func Test_CTRCTL08_PairConnectErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		code       v1.ErrorCode
		reason     string
		retryAfter bool
	}{
		{"wrong password", &mesh.PairError{Reason: mesh.PairWrongPassword}, v1.ErrPairRejected, "wrong_password", false},
		{"no password on the peer", &mesh.PairError{Reason: mesh.PairNoPassword}, v1.ErrPairRejected, "no_password", false},
		{"old peer", &mesh.PairError{Reason: mesh.PairUnsupportedBy}, v1.ErrPairRejected, "unsupported_peer", false},
		{"unreachable", &mesh.PairError{Reason: mesh.PairUnreachable}, v1.ErrPairRejected, "unreachable", false},
		{"bad peer", &mesh.PairError{Reason: mesh.PairBadPeer}, v1.ErrPairRejected, "bad_peer", false},
		{"rate limited", &mesh.PairError{Reason: mesh.PairRateLimited, RetryAfter: 30 * time.Second}, v1.ErrRateLimited, "", true},
		{"fingerprint mismatch", &mesh.PairError{Reason: mesh.PairFingerprintMismatch}, v1.ErrInvalidArgs, "fingerprint_mismatch", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			admin := &fakePairing{connectErr: c.err}
			f := newFixture(t, func(o *Options) { o.Pairing = admin })
			e := apiError(t, f.do("POST", "/v1/pair/connect", v1.PairConnectRequest{Address: "192.168.31.15", Password: pairTestPassword}, nil), c.code)
			if e.Reason != c.reason {
				t.Errorf("reason = %q, want %q", e.Reason, c.reason)
			}
			if c.retryAfter != (e.RetryAfterMS != nil && *e.RetryAfterMS == 30000) {
				t.Errorf("retry_after_ms = %v", e.RetryAfterMS)
			}
			if c.code == v1.ErrInvalidArgs && e.Field != "fingerprint" {
				t.Errorf("field = %q, want fingerprint", e.Field)
			}
		})
	}
}

// conformance: CTR-CTL-08 the password is write-only: no response body carries it.
func Test_CTRCTL08_PasswordIsWriteOnly(t *testing.T) {
	admin := &fakePairing{}
	f := newFixture(t, func(o *Options) { o.Pairing = admin })
	f.ok("PUT", "/v1/pair/password", v1.PairPasswordRequest{Password: pairTestPassword}, "PairPasswordState")
	for _, path := range []string{"/v1/pair/password", "/v1/pair/discovered", "/v1/status"} {
		if body := string(f.do("GET", path, nil, nil).Body); strings.Contains(body, pairTestPassword) {
			t.Errorf("%s leaks the password: %s", path, body)
		}
	}
}
