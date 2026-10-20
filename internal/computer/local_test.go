package computer

import (
	"context"
	"testing"
)

func TestLocalStateProjectsRevocationInSameResponse(t *testing.T) {
	for _, localInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "locked", true: "user_input"}[localInput], func(t *testing.T) {
			m, p := manager(t)
			p.mu.Lock()
			p.info.UserInputMonitor = true
			p.mu.Unlock()
			m.Configure(Settings{Enabled: true, PauseOnUserInput: localInput})
			_ = open(t, m, "agent/session", "control")
			p.mu.Lock()
			if localInput {
				p.info.UserInputSerial++
			} else {
				p.info.Available = false
				p.info.State = "locked"
			}
			p.mu.Unlock()
			state := m.LocalState(context.Background())
			if len(state.Sessions) != 0 || state.Paused != localInput {
				t.Fatal("local state retained authority revoked during observation", state)
			}
		})
	}
}

func TestLocalPausePreservesObservationUntilSharingDisabled(t *testing.T) {
	m, _ := manager(t)
	_ = open(t, m, "agent/session", "control")
	viewer := open(t, m, "viewer/session", "observe")
	m.Pause(true)
	if _, err := m.Observe(context.Background(), "viewer/session", ObserveRequest{SessionRequest: request(viewer)}); err != nil {
		t.Fatal("pausing input interrupted observation", err)
	}
	state := m.LocalState(context.Background())
	if !state.Paused || len(state.Sessions) != 1 || state.Sessions[0].Token != "" {
		t.Fatal("local pause projection", state)
	}
	m.Configure(Settings{Enabled: false})
	if len(m.LocalState(context.Background()).Sessions) != 0 {
		t.Fatal("disabled sharing retained observers")
	}
}
