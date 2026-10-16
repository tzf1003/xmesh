package pty

import "fmt"

// BudgetURI is an Agent control resource read by authenticated ingress nodes.
const BudgetURI = "xmesh://control/pty-budget"

// BudgetSnapshot describes live reservations, including terminal startup and
// orphaned sessions. Epoch changes when the owning manager is recreated.
type BudgetSnapshot struct {
	Epoch    string            `json:"epoch"`
	Counts   map[string]int    `json:"counts"`
	Sessions map[string]string `json:"sessions"`
	Callers  map[string]string `json:"callers"`
}

func (m *Manager) BudgetSnapshot() BudgetSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := BudgetSnapshot{Epoch: m.epoch, Counts: map[string]int{}, Sessions: map[string]string{}, Callers: m.callers.Budgets()}
	for id, s := range m.sessions {
		if s.State == "active" || s.State == "orphaned" {
			out.Counts[s.Budget]++
			out.Sessions[id] = s.Budget
		}
	}
	return out
}

func (s BudgetSnapshot) Validate() error {
	if s.Epoch == "" || s.Counts == nil || s.Sessions == nil || s.Callers == nil {
		return fmt.Errorf("incomplete PTY budget snapshot")
	}
	counts := map[string]int{}
	for id, budget := range s.Sessions {
		if id == "" || budget == "" {
			return fmt.Errorf("invalid PTY reservation")
		}
		counts[budget]++
	}
	if len(counts) != len(s.Counts) {
		return fmt.Errorf("inconsistent PTY counts")
	}
	for budget, count := range counts {
		if count != s.Counts[budget] {
			return fmt.Errorf("inconsistent PTY count")
		}
	}
	return nil
}
