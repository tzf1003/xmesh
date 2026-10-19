package computer

import "context"

// SetAuthorization installs the owning Agent's current tag/configuration check.
// It is re-evaluated immediately before each queued input operation.
func (m *Manager) SetAuthorization(check func(string) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authorize = check
}

// LocalState is metadata for the owner's local control channel. Lease tokens,
// typed text and images are deliberately absent from this view.
type LocalState struct {
	Enabled          bool      `json:"enabled"`
	Paused           bool      `json:"paused"`
	PauseOnUserInput bool      `json:"pause_on_user_input,omitempty"`
	Info             Info      `json:"info"`
	Sessions         []Session `json:"sessions"`
}

func (m *Manager) LocalState(ctx context.Context) LocalState {
	m.mu.Lock()
	v := LocalState{Enabled: m.settings.Enabled, Paused: m.paused, Info: Info{ProtocolVersion: ProtocolVersion, State: "disabled", Capabilities: []string{}, Displays: []Display{}}, Sessions: []Session{}}
	v.PauseOnUserInput = m.settings.PauseOnUserInput
	m.expireLocked()
	m.mu.Unlock()
	if v.Enabled {
		info, err := m.Info(ctx)
		if err != nil {
			v.Info.State = "desktop_unavailable"
			v.Info.Reason = errorCode(err)
		} else {
			v.Info = info
		}
	}
	// Info can revoke sessions or pause input after observing permissions and
	// local activity. Project that resulting authority in this same response.
	m.mu.Lock()
	v.Enabled = m.settings.Enabled
	v.Paused = m.paused
	v.PauseOnUserInput = m.settings.PauseOnUserInput
	m.expireLocked()
	for _, s := range m.sessions {
		session := s.Session
		session.Token = ""
		v.Sessions = append(v.Sessions, session)
	}
	m.mu.Unlock()
	if v.Info.Capabilities == nil {
		v.Info.Capabilities = []string{}
	}
	if v.Info.Displays == nil {
		v.Info.Displays = []Display{}
	}
	return v
}
