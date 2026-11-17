package control

import (
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// fakeDelegate is an agent with one running task of another caller and one runner.
type fakeDelegate struct{}

func (fakeDelegate) DelegateState() v2.DelegateState {
	return v2.DelegateState{
		Enabled: true, AllowWrite: true, AllowSessions: false, MaxConcurrent: 2, Running: 1,
		Runners:  []v2.DelegateRunner{{Name: "claude", Available: true, Path: "/usr/bin/claude"}, {Name: "codex", Available: false}},
		Sessions: []v2.DelegateSession{},
		Tasks: []v2.DelegateTask{{
			TaskID: "t-5e0a7c41b9d24f6e8a13c0d7f2e4b916", Caller: "node-b/bob", Runner: "claude", State: "running", CWD: "/w/a",
			StartedAt: v2.Now(), DurationMS: 5,
		}},
	}
}

// conformance: CTR-CTL-12
//
// GET /v2/delegate is served, and declares the delegate capability, only when the node has an agent to answer it.
func TestDelegateRouteNeedsAnAgent(t *testing.T) {
	f := newFixture(t)
	apiError(t, f.do("GET", "/v2/delegate", nil, nil), v2.ErrNotImplemented)

	if f.server.hasCapability(v2.CapDelegate) {
		t.Fatal("delegate capability declared without an agent")
	}
	f = newFixture(t, func(o *Options) { o.Delegate = fakeDelegate{} })
	if !f.server.hasCapability(v2.CapDelegate) {
		t.Fatal("delegate capability missing with an agent")
	}
	f.ok("GET", "/v2/delegate", nil, "DelegateState")
}
