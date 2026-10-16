package pty

import (
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
)

func TestBudgetSnapshotReservations(t *testing.T) {
	registry := identity.NewRegistry()
	caller := identity.Caller{ID: "ingress/session", OAuthClient: "client"}
	if err := registry.Add(caller); err != nil {
		t.Fatal(err)
	}
	m := New(config.PTY{}, registry)
	t.Cleanup(m.Shutdown)
	m.mu.Lock()
	for id, state := range map[string]string{"p-starting": "active", "p-orphan": "orphaned", "p-closed": "closed"} {
		m.sessions[id] = &Session{ID: id, Budget: identity.Budget(caller), State: state, started: make(chan struct{})}
		close(m.sessions[id].started)
	}
	m.mu.Unlock()
	snapshot := m.BudgetSnapshot()
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if snapshot.Counts["oauth:client"] != 2 || len(snapshot.Sessions) != 2 || snapshot.Callers[caller.ID] != "oauth:client" {
		t.Fatal(snapshot)
	}
	delete(snapshot.Sessions, "p-starting")
	if err := snapshot.Validate(); err == nil {
		t.Fatal("inconsistent snapshot accepted")
	}
	if m.BudgetSnapshot().Counts["oauth:client"] != 2 {
		t.Fatal("snapshot mutation changed live state")
	}
	other := New(config.PTY{}, registry)
	t.Cleanup(other.Shutdown)
	if snapshot.Epoch == other.BudgetSnapshot().Epoch {
		t.Fatal("manager restart reused epoch")
	}
}
