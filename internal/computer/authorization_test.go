package computer

import (
	"context"
	"sync"
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

func TestConcurrentControlOpenAndPolicyReload(t *testing.T) {
	m, p := manager(t)
	p.mu.Lock()
	p.info.UserInputMonitor = true
	p.mu.Unlock()
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for i := range 100 {
			m.Configure(Settings{Enabled: true, PauseOnUserInput: i%2 == 0})
		}
	}()
	go func() {
		defer workers.Done()
		for range 100 {
			s, err := m.Open(context.Background(), "race/session", OpenRequest{Mode: "control"})
			if err == nil {
				_ = m.CloseSession("race/session", request(s))
			}
		}
	}()
	workers.Wait()
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

func TestScreenPermissionAllowsObservationWhileInputIsUnavailable(t *testing.T) {
	m, p := manager(t)
	p.mu.Lock()
	p.info.Capabilities = []string{"screenshot"}
	p.mu.Unlock()
	_ = open(t, m, "viewer/session", "observe")
	_, err := m.Open(context.Background(), "actor/session", OpenRequest{Mode: "control"})
	if code(err) != "permission_required" {
		t.Fatal(err)
	}
	if len(m.Sessions("viewer/session")) != 1 {
		t.Fatal("input permission affected observation")
	}
}

func TestLocalUserInputPausesControlAndPreservesObservers(t *testing.T) {
	m, p := manager(t)
	p.mu.Lock()
	p.info.UserInputMonitor = true
	p.info.UserInputSerial = 7
	p.mu.Unlock()
	m.Configure(Settings{Enabled: true, PauseOnUserInput: true})
	_ = open(t, m, "agent/session", "control")
	_ = open(t, m, "observer/session", "observe")
	p.mu.Lock()
	p.info.UserInputSerial++
	p.mu.Unlock()
	if _, err := m.Info(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.Sessions("agent/session")) != 0 || len(m.Sessions("observer/session")) != 1 {
		t.Fatal("human input did not revoke only the controller")
	}
	if _, err := m.Open(context.Background(), "agent/session", OpenRequest{Mode: "control"}); code(err) != "policy_denied" {
		t.Fatal(err)
	}
}

func TestUnsupportedInputMonitoringFailsControlExplicitly(t *testing.T) {
	m, _ := manager(t)
	m.Configure(Settings{Enabled: true, PauseOnUserInput: true})
	if _, err := m.Open(context.Background(), "agent/session", OpenRequest{Mode: "control"}); code(err) != "permission_required" {
		t.Fatal(err)
	}
	_ = open(t, m, "viewer/session", "observe")
}

func TestInputMonitoringPolicyChangeRequiresFreshControlLease(t *testing.T) {
	m, p := manager(t)
	p.mu.Lock()
	p.info.UserInputMonitor = true
	p.info.UserInputSerial = 8
	p.mu.Unlock()
	old := open(t, m, "agent/session", "control")
	_ = open(t, m, "observer/session", "observe")
	m.Configure(Settings{Enabled: true, PauseOnUserInput: true})
	if len(m.Sessions("agent/session")) != 0 || len(m.Sessions("observer/session")) != 1 {
		t.Fatal("policy change retained old control or revoked the observer")
	}
	next := open(t, m, "agent/session", "control")
	if next.Token == old.Token {
		t.Fatal("new policy reused the old lease")
	}
	if state := m.LocalState(context.Background()); state.Paused || len(state.Sessions) != 2 {
		t.Fatal("fresh monitoring baseline was not established", state)
	}
}
