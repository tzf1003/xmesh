package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/device"
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
	found      []v2.PairDiscoveredDevice
	connectErr error
	pairedWith string
}

func (p *fakePairing) PairPasswordState() device.PairStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return device.PairStatus{Set: p.set, UpdatedAt: p.updated, FailedAttempts: p.failures, LockedUntil: p.locked}
}

func (p *fakePairing) SetPairPassword(password string) (device.PairStatus, error) {
	if err := device.ValidatePassword(password); err != nil {
		return device.PairStatus{}, err
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

func (p *fakePairing) Discovered(context.Context) ([]v2.PairDiscoveredDevice, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]v2.PairDiscoveredDevice(nil), p.found...), nil
}

func (p *fakePairing) PairWith(_ context.Context, _ device.PathKind, address, password, expected string) (device.Trust, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pairedWith = address
	if p.connectErr != nil {
		return device.Trust{}, p.connectErr
	}
	if password != pairTestPassword {
		return device.Trust{}, device.ErrPairProof
	}
	if expected != "" && expected != "dev-"+pairPeerFingerprint {
		return device.Trust{}, device.ErrIdentityMismatch
	}
	return device.Trust{Document: device.Document{Name: "peer-b", DeviceID: "dev-" + pairPeerFingerprint}, Tags: []string{"tag:peer", "tag:device"}}, nil
}

// conformance: CTR-CTL-08 the connection-password routes exist only in lan mode, and the password
// never comes back in a response.
func Test_CTRCTL08_PairingUnavailableWithoutRuntimeService(t *testing.T) {
	f := newFixtureConfig(t, tailnetConfig)
	apiError(t, f.do("GET", "/v2/pair/password", nil, nil), v2.ErrUnavailable)
	apiError(t, f.do("PUT", "/v2/pair/password", v2.PairPasswordRequest{Password: pairTestPassword}, nil), v2.ErrUnavailable)
	apiError(t, f.do("DELETE", "/v2/pair/password", nil, nil), v2.ErrUnavailable)
	apiError(t, f.do("GET", "/v2/pair/discovered", nil, nil), v2.ErrUnavailable)
	apiError(t, f.do("POST", "/v2/pair/connect", v2.PairConnectRequest{Address: "10.0.0.2", Password: pairTestPassword}, nil), v2.ErrUnavailable)
}

// conformance: CTR-CTL-08
func Test_CTRCTL08_PairPasswordLifecycle(t *testing.T) {
	plain := newFixture(t)
	apiError(t, plain.do("GET", "/v2/pair/password", nil, nil), v2.ErrUnavailable)
	plain.server.Close() // the pipe name is global on Windows: one server per node at a time

	admin := &fakePairing{}
	f := newFixture(t, func(o *Options) { o.Pairing = admin })

	var state v2.PairPasswordState
	decodeAs(t, f.ok("GET", "/v2/pair/password", nil, "PairPasswordState"), &state)
	if state.Set || state.UpdatedAt != nil || state.LockedUntil != nil || state.FailedAttempts != 0 {
		t.Fatalf("a new node has no password: %+v", state)
	}

	apiError(t, f.do("PUT", "/v2/pair/password", v2.PairPasswordRequest{Password: "short"}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("PUT", "/v2/pair/password", v2.PairPasswordRequest{Password: " " + pairTestPassword}, nil), v2.ErrInvalidArgs)

	put := f.ok("PUT", "/v2/pair/password", v2.PairPasswordRequest{Password: pairTestPassword}, "PairPasswordState")
	if strings.Contains(string(put.Body), pairTestPassword) {
		t.Fatalf("the response echoes the password: %s", put.Body)
	}
	decodeAs(t, put, &state)
	if !state.Set || state.UpdatedAt == nil {
		t.Fatalf("after a set the password is set: %+v", state)
	}

	if r := f.do("DELETE", "/v2/pair/password", nil, nil); r.Status != 204 {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	decodeAs(t, f.ok("GET", "/v2/pair/password", nil, "PairPasswordState"), &state)
	if state.Set {
		t.Fatalf("after a delete the password is not set: %+v", state)
	}
}

// conformance: CTR-CTL-08
func Test_CTRCTL08_PairPasswordReportsLockout(t *testing.T) {
	until := time.Now().Add(time.Minute).UTC().Truncate(time.Millisecond)
	admin := &fakePairing{set: true, updated: time.Now().UTC().Truncate(time.Millisecond), failures: 5, locked: until}
	f := newFixture(t, func(o *Options) { o.Pairing = admin })
	var state v2.PairPasswordState
	decodeAs(t, f.ok("GET", "/v2/pair/password", nil, "PairPasswordState"), &state)
	if state.FailedAttempts != 5 || state.LockedUntil == nil || !state.LockedUntil.Equal(until) {
		t.Fatalf("lockout is not reported: %+v", state)
	}
}

// conformance: CTR-CTL-08
func Test_CTRCTL08_PairDiscoveryAndConnect(t *testing.T) {
	admin := &fakePairing{found: []v2.PairDiscoveredDevice{{Name: "peer-b", Fingerprint: pairPeerFingerprint, Address: "192.168.31.15:7433", Path: "lan", DeviceID: "dev-" + pairPeerFingerprint, SeenAt: v2.Now()}}}
	f := newFixture(t, func(o *Options) { o.Pairing = admin })

	var listed v2.PairDiscoveredResponse
	decodeAs(t, f.ok("GET", "/v2/pair/discovered", nil, "PairDiscoveredResponse"), &listed)
	if len(listed.Devices) != 1 || listed.Devices[0].Name != "peer-b" || listed.Devices[0].Address != "192.168.31.15:7433" {
		t.Fatalf("discovered: %+v", listed)
	}

	apiError(t, f.do("POST", "/v2/pair/connect", v2.PairConnectRequest{Password: pairTestPassword}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v2/pair/connect", v2.PairConnectRequest{Fingerprint: "not-a-fingerprint", Password: pairTestPassword}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v2/pair/connect", v2.PairConnectRequest{Address: "192.168.31.15:7433", Password: "short"}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v2/pair/connect", v2.PairConnectRequest{Fingerprint: pairOtherFingerprint, Password: pairTestPassword}, nil), v2.ErrNotFound)
	// A named address with a different expected certificate stops before anything is confirmed.
	apiError(t, f.do("POST", "/v2/pair/connect", v2.PairConnectRequest{Address: "192.168.31.15:7433", Fingerprint: pairOtherFingerprint, Password: pairTestPassword}, nil), v2.ErrInvalidArgs)

	// With only a fingerprint, the address comes from the discovery list.
	var connected v2.PairConnectResponse
	decodeAs(t, f.ok("POST", "/v2/pair/connect", v2.PairConnectRequest{Fingerprint: pairPeerFingerprint, Password: pairTestPassword}, "PairConnectResponse"), &connected)
	if admin.pairedWith != "https://192.168.31.15:7433" || connected.Name != "peer-b" || connected.Address != "https://192.168.31.15:7433" || len(connected.Tags) != 2 {
		t.Fatalf("connect: paired with %q, response %+v", admin.pairedWith, connected)
	}
	if strings.Contains(string(f.do("GET", "/v2/pair/discovered", nil, nil).Body), pairTestPassword) {
		t.Fatal("discovery leaks the password")
	}
}

// conformance: CTR-CTL-08
func Test_CTRCTL08_PairConnectErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		code       v2.ErrorCode
		reason     string
		retryAfter bool
	}{
		{"wrong password", device.ErrPairProof, v2.ErrPairRejected, "wrong_password", false},
		{"no password on the peer", device.ErrPairDisabled, v2.ErrPairRejected, "no_password", false},
		{"old peer", device.ErrPairUnsupported, v2.ErrPairRejected, "unsupported_peer", false},
		{"unreachable", errors.New("network unreachable"), v2.ErrPairRejected, "unreachable", false},
		{"bad peer", device.ErrPairProof, v2.ErrPairRejected, "wrong_password", false},
		{"rate limited", device.ErrPairLimited, v2.ErrRateLimited, "", true},
		{"fingerprint mismatch", device.ErrIdentityMismatch, v2.ErrInvalidArgs, "fingerprint_mismatch", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			admin := &fakePairing{connectErr: c.err}
			f := newFixture(t, func(o *Options) { o.Pairing = admin })
			e := apiError(t, f.do("POST", "/v2/pair/connect", v2.PairConnectRequest{Address: "192.168.31.15", Password: pairTestPassword}, nil), c.code)
			if e.Reason != c.reason {
				t.Errorf("reason = %q, want %q", e.Reason, c.reason)
			}
			if c.retryAfter != (e.RetryAfterMS != nil && *e.RetryAfterMS == 30000) {
				t.Errorf("retry_after_ms = %v", e.RetryAfterMS)
			}
			if c.code == v2.ErrInvalidArgs && e.Field != "fingerprint" {
				t.Errorf("field = %q, want fingerprint", e.Field)
			}
		})
	}
}

// conformance: CTR-CTL-08 the password is write-only: no response body carries it.
func Test_CTRCTL08_PasswordIsWriteOnly(t *testing.T) {
	admin := &fakePairing{}
	f := newFixture(t, func(o *Options) { o.Pairing = admin })
	f.ok("PUT", "/v2/pair/password", v2.PairPasswordRequest{Password: pairTestPassword}, "PairPasswordState")
	for _, path := range []string{"/v2/pair/password", "/v2/pair/discovered", "/v2/status"} {
		if body := string(f.do("GET", path, nil, nil).Body); strings.Contains(body, pairTestPassword) {
			t.Errorf("%s leaks the password: %s", path, body)
		}
	}
}

func Test_GatewayPairingKeepsOutgoingOperations(t *testing.T) {
	f := newFixtureConfig(t, strings.Replace(gatewayConfig, "roles: [agent]", "roles: [gateway]", 1), func(o *Options) { o.Pairing = &fakePairing{} })
	f.ok("PUT", "/v2/pair/password", v2.PairPasswordRequest{Password: pairTestPassword}, "PairPasswordState")
	f.ok("GET", "/v2/pair/discovered", nil, "PairDiscoveredResponse")
}
