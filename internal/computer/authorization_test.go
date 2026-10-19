package computer

import (
	"context"
	"testing"
)

func TestQueuedActionsRecheckCurrentAuthorization(t *testing.T) {
	m, _ := manager(t)
	s := open(t, m, "node/session", "control")
	r := click(t, m, "node/session", s, "revoked")
	m.SetAuthorization(func(string) bool { return false })
	_, err := m.Act(context.Background(), "node/session", r)
	if code(err) != "policy_denied" {
		t.Fatal(err)
	}
	if len(m.Sessions("")) != 0 {
		t.Fatal("revoked caller retained lease")
	}
}

func TestPermissionAndEpochChangesRevokeControl(t *testing.T) {
	m, p := manager(t)
	_ = open(t, m, "node/session", "control")
	p.mu.Lock()
	p.info.Available = false
	p.info.State = "locked"
	p.mu.Unlock()
	info, err := m.Info(context.Background())
	if err != nil || info.State != "locked" {
		t.Fatal(info, err)
	}
	if len(m.Sessions("")) != 0 {
		t.Fatal("locked desktop kept sessions")
	}
}
