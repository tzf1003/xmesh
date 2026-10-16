//go:build !windows

package pty

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

func wantLimit(t *testing.T, err error, scope string, limit, inUse int) {
	t.Helper()
	v, ok := err.(*toolerr.Error)
	if !ok || v.Code != "pty_limit" {
		t.Fatalf("want pty_limit, got %v", err)
	}
	if v.Fields["scope"] != scope || v.Fields["limit"] != limit || v.Fields["in_use"] != inUse {
		t.Fatalf("pty_limit fields = %v, want scope=%s limit=%d in_use=%d", v.Fields, scope, limit, inUse)
	}
	if _, named := v.Fields["active"]; named {
		t.Fatal("pty_limit must not use the field name `active` for the count")
	}
}

func openAs(t *testing.T, m *Manager, r *cwd.Resolver, p *policy.Engine, c identity.Caller) (string, error) {
	t.Helper()
	out, err := invoke(m, r, p, c, "pty.open", Object{"cols": 80, "rows": 24})
	if err != nil {
		return "", err
	}
	return out["session_id"].(string), nil
}

func mustOpen(t *testing.T, m *Manager, r *cwd.Resolver, p *policy.Engine, c identity.Caller) string {
	t.Helper()
	id, err := openAs(t, m, r, p, c)
	if err != nil {
		t.Fatalf("open by %s: %v", c.ID, err)
	}
	return id
}

func closeSession(t *testing.T, m *Manager, r *cwd.Resolver, p *policy.Engine, c identity.Caller, id string) {
	t.Helper()
	if _, err := invoke(m, r, p, c, "pty.close", Object{"session_id": id}); err != nil {
		t.Fatalf("close %s: %v", id, err)
	}
	waitState(t, m, id, "closed")
}

func stateOf(m *Manager, id string) string {
	for _, row := range m.List() {
		if row["id"] == id {
			return row["state"].(string)
		}
	}
	return "absent"
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(lockBound); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Only active and orphaned sessions count toward max_sessions, and only a
// caller's active sessions count toward that caller's max_per_caller. Orphans
// fill the device but not their former caller's count; closed sessions fill
// neither. pty_limit reports the number it compared as `in_use`, never
// including closed sessions. When more than closed_retain_max sessions are
// closed, the oldest is released at once and later calls on it fail with
// session_not_found. pty.list never reports `released`.
// conformance: INV-02 RUL-12 RUL-07 CTR-PTY-01
func Test_INV02_RUL12_ClosedAndOrphanedQuota(t *testing.T) {
	m, r, p, reg := fixture(t)
	// Distinct nodes: the per-principal budget groups the callers of one node.
	a, b := identity.Caller{ID: "na/s"}, identity.Caller{ID: "nb/s"}
	c, d := identity.Caller{ID: "nc/s"}, identity.Caller{ID: "nd/s"}
	for _, caller := range []identity.Caller{a, b, c, d} {
		_ = reg.Add(caller)
	}
	m.Configure(config.PTY{MaxSessions: 3, MaxPerCaller: 2, IdleTimeout: "30m", OrphanGrace: "10m", ScrollbackBytes: "1MiB", DefaultShell: "/bin/sh", ClosedRetain: "5m", ClosedRetainMax: 2})

	a1, a2 := mustOpen(t, m, r, p, a), mustOpen(t, m, r, p, a)
	_, err := openAs(t, m, r, p, a)
	wantLimit(t, err, "caller", 2, 2)
	b1 := mustOpen(t, m, r, p, b)
	_, err = openAs(t, m, r, p, b)
	wantLimit(t, err, "device", 3, 3)
	if in, active, closed := m.Counts(); in != 3 || active != 3 || closed != 0 {
		t.Fatalf("counts %d/%d/%d", in, active, closed)
	}

	// Orphans keep their place on the device but not in their caller's count.
	m.Orphan(a.ID)
	if got := stateOf(m, a1) + "," + stateOf(m, a2); got != "orphaned,orphaned" {
		t.Fatal(got)
	}
	m.mu.Lock()
	ownedByA := m.byCaller[a.ID]
	m.mu.Unlock()
	if ownedByA != 0 {
		t.Fatalf("orphans still count toward their former caller's max_per_caller: %d", ownedByA)
	}
	_, err = openAs(t, m, r, p, c)
	wantLimit(t, err, "device", 3, 3)
	if in, active, _ := m.Counts(); in != 3 || active != 1 {
		t.Fatalf("orphans must count toward the device total: in_use %d active %d", in, active)
	}

	// A closed session occupies neither quota.
	closeSession(t, m, r, p, b, b1)
	if in, active, closed := m.Counts(); in != 2 || active != 0 || closed != 1 {
		t.Fatalf("counts after closing %d/%d/%d", in, active, closed)
	}
	c1 := mustOpen(t, m, r, p, c)
	// 2 orphans + c1 = 3 in use although 4 sessions exist in the table.
	_, err = openAs(t, m, r, p, d)
	wantLimit(t, err, "device", 3, 3)
	if len(m.List()) != 4 {
		t.Fatalf("the closed session must stay listed: %v", m.List())
	}
	if got := stateOf(m, b1); got != "closed" {
		t.Fatal(got)
	}

	// Closing more sessions than closed_retain_max releases the oldest at once.
	closeSession(t, m, r, p, c, c1)
	if _, err = invoke(m, r, p, a, "pty.transfer", Object{"session_id": a1, "to_caller": a.ID}); err != nil {
		t.Fatal(err)
	}
	closeSession(t, m, r, p, a, a1)
	// Three sessions have been closed (b1, c1, a1); only two may be retained.
	waitUntil(t, "the oldest closed session to be released", func() bool { return stateOf(m, b1) == "absent" })
	if got := stateOf(m, c1) + "," + stateOf(m, a1); got != "closed,closed" {
		t.Fatal(got)
	}
	if in, active, closed := m.Counts(); in != 1 || active != 0 || closed != 2 {
		t.Fatalf("counts %d/%d/%d", in, active, closed)
	}
	if _, err = invoke(m, r, p, b, "pty.read", Object{"session_id": b1, "wait_ms": 0}); errorCode(err) != "session_not_found" {
		t.Fatalf("read of a released session: %v", err)
	}
	if _, err = invoke(m, r, p, b, "pty.write", Object{"session_id": b1, "data": "x"}); errorCode(err) != "session_not_found" {
		t.Fatalf("write to a released session: %v", err)
	}
	// Released is a state, never listed.
	for _, row := range m.List() {
		if s := row["state"]; s != "active" && s != "orphaned" && s != "closed" {
			t.Fatalf("pty.list returned state %v", s)
		}
	}
	// Repeated close within the retention period is a success reporting `closed`.
	if out, err := invoke(m, r, p, a, "pty.close", Object{"session_id": a1}); err != nil || out["state"] != "closed" {
		t.Fatalf("repeat close: %v %v", out, err)
	}
	// After the retention period the entries are released and disappear.
	m.Reap(time.Now().Add(6 * time.Minute))
	if len(m.List()) != 1 {
		t.Fatalf("retention did not release closed sessions: %v", m.List())
	}
}

// Open reserves a place in one critical section; when the process cannot be
// started afterwards, the reservation is rolled back and the session removed, so
// a failed open leaves the counts as they were.
// conformance: CON-CONC-03
func Test_CONCONC03_FailedStartRollsBackReservation(t *testing.T) {
	m, r, p, reg := fixture(t)
	c := identity.Caller{ID: "node/a"}
	_ = reg.Add(c)
	m.Configure(config.PTY{MaxSessions: 3, MaxPerCaller: 2, IdleTimeout: "30m", OrphanGrace: "10m", ScrollbackBytes: "1MiB", DefaultShell: "/bin/sh", ClosedRetain: "5m", ClosedRetainMax: 20})
	// Executable, but its interpreter does not exist: LookPath succeeds, start fails.
	broken := filepath.Join(t.TempDir(), "broken-shell")
	if err := os.WriteFile(broken, []byte("#!/nonexistent/interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, err := invoke(m, r, p, c, "pty.open", Object{"cols": 80, "rows": 24, "shell": broken})
		if errorCode(err) != "internal" {
			t.Fatalf("start failure %d: %v", i, err)
		}
		m.mu.Lock()
		sessions, owned := len(m.sessions), m.byCaller[c.ID]
		m.mu.Unlock()
		if in, active, closed := m.Counts(); in != 0 || active != 0 || closed != 0 || sessions != 0 || owned != 0 {
			t.Fatalf("failed open %d left state behind: counts %d/%d/%d sessions %d owned %d", i, in, active, closed, sessions, owned)
		}
	}
	// The reserved places were returned: the full per-caller quota is still available.
	mustOpen(t, m, r, p, c)
	mustOpen(t, m, r, p, c)
	_, err := openAs(t, m, r, p, c)
	wantLimit(t, err, "caller", 2, 2)
}
