//go:build !windows

package pty

import (
	"testing"

	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// pty.transfer targets a caller identity, not the calling one, and the target
// must be a caller the Agent has seen: an arbitrary string cannot become an
// owner. A tool argument naming an owner never overrides the transport identity.
// conformance: CTR-ID-01
func Test_CTRID01_TransferTargetMustBeAKnownCaller(t *testing.T) {
	m, r, p, reg := fixture(t)
	a := identity.Caller{ID: "node-a/s1"}
	b := identity.Caller{ID: "node-a/s2"}
	for _, c := range []identity.Caller{a, b} {
		if err := reg.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	opened, err := invoke(m, r, p, a, "pty.open", Object{"cols": 80, "rows": 24, "owner": "forged/owner", "caller_id": "forged/owner"})
	if err != nil {
		t.Fatal(err)
	}
	id := opened["session_id"].(string)
	if opened["owner"] != a.ID {
		t.Fatalf("a forged owner argument was honoured: %v", opened["owner"])
	}
	for _, target := range []string{"forged/owner", "node-a/never-seen", "no-slash", "", "node-a/s2/extra", "NODE-A/S2", "node-a/S2"} {
		_, err := invoke(m, r, p, a, "pty.transfer", Object{"session_id": id, "to_caller": target})
		if e, ok := err.(*toolerr.Error); !ok || e.Code != "invalid_args" {
			t.Errorf("transfer to %q: %v", target, err)
		}
	}
	if m.List()[0]["owner"] != a.ID {
		t.Fatal("a refused transfer changed the owner")
	}
	if _, err := invoke(m, r, p, a, "pty.transfer", Object{"session_id": id, "to_caller": b.ID}); err != nil {
		t.Fatal(err)
	}
	if m.List()[0]["owner"] != b.ID {
		t.Fatalf("owner = %v, want the transfer target", m.List()[0]["owner"])
	}
}
