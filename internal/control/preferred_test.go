package control

import (
	"context"
	"errors"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

type fakePreferred struct{ ran []string }

func (f *fakePreferred) PreferredState() v2.PreferredIPState {
	return v2.PreferredIPState{Enabled: true, Mode: "smart", Pool: []v2.PreferredIPEntry{}, Sources: []v2.PreferredIPSourceState{}, Peers: []v2.PreferredIPPeerState{}}
}
func (f *fakePreferred) PreferredInspect(text string) v2.PreferredIPInspectResponse {
	return v2.PreferredIPInspectResponse{Items: []v2.PreferredIPInspectItem{{Line: 1, IP: text, Status: "rejected", Note: "no"}}, Accepted: []string{}}
}
func (f *fakePreferred) PreferredRun(_ context.Context, task string) (v2.PreferredIPState, error) {
	if task == "scan" && len(f.ran) > 0 {
		return v2.PreferredIPState{}, errors.New("busy")
	}
	f.ran = append(f.ran, task)
	return f.PreferredState(), nil
}

func TestPreferredIPRoutes(t *testing.T) {
	fake := &fakePreferred{}
	f := newFixture(t, func(o *Options) { o.Preferred = fake })
	f.ok("GET", "/v2/preferred-ip", nil, "PreferredIPState")
	f.ok("POST", "/v2/preferred-ip/run", v2.PreferredIPRunRequest{Task: "scan"}, "PreferredIPState")
	f.ok("POST", "/v2/preferred-ip/run", v2.PreferredIPRunRequest{Task: "refresh"}, "PreferredIPState")
	if len(fake.ran) != 2 {
		t.Fatal(fake.ran)
	}
	apiError(t, f.do("POST", "/v2/preferred-ip/run", v2.PreferredIPRunRequest{Task: "stop"}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v2/preferred-ip/run", v2.PreferredIPRunRequest{Task: "scan"}, nil), v2.ErrInvalidArgs)
	f.ok("POST", "/v2/preferred-ip/inspect", v2.PreferredIPInspectRequest{Text: "8.8.8.8"}, "PreferredIPInspectResponse")
}

func TestPreferredIPUnavailableWithoutNode(t *testing.T) {
	f := newFixture(t)
	apiError(t, f.do("GET", "/v2/preferred-ip", nil, nil), v2.ErrUnavailable)
}
