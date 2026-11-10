package computer

import (
	"context"
	"slices"
	"testing"
)

// nonConsoleSession is what the native executor reports for a user that does not own the physical screen:
// screenshots and per-window input work, global pointer and keyboard do not.
func nonConsoleSession(p *fakeDesktop) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.info.Session = "background"
	p.info.Capabilities = []string{"screenshot", "background:screenshot", "background:pointer", "background:keyboard", "background:text"}
	p.info.DefaultInteraction = "background"
	p.info.UserInputMonitor = true
	p.info.Targets = []Target{{ID: "window-one", AppID: "test.app", Name: "Test", LayoutVersion: "window-layout", Width: 200, Height: 160, UserInputSerial: 7}}
}

func TestBackgroundSessionStaysUnavailableUntilLocallyAllowed(t *testing.T) {
	ctx := context.Background()
	m, p := manager(t)
	nonConsoleSession(p)
	info, err := m.Info(ctx)
	if err != nil || info.Available || info.State != "session_inactive" || info.Reason != "background_session_disabled" || len(info.Capabilities) != 0 || len(info.Targets) != 0 {
		t.Fatal("background session was usable without allow_background_session", info, err)
	}
	if _, err = m.Open(ctx, "owner", OpenRequest{Mode: "observe"}); code(err) != "desktop_unavailable" {
		t.Fatal("observation opened while background sessions were disabled", err)
	}
	m.Configure(Settings{Enabled: true, AllowBackgroundSession: true})
	info, err = m.Info(ctx)
	if err != nil || !info.Available || !slices.Contains(info.Capabilities, "background:screenshot") {
		t.Fatal("allowed background session stayed unavailable", info, err)
	}
	if _, err = m.Open(ctx, "owner", OpenRequest{Mode: "observe", TargetID: "window-one"}); err != nil {
		t.Fatal("observation was refused in an allowed background session", err)
	}
}

func TestBackgroundSessionRefusesForegroundControlInTheManager(t *testing.T) {
	ctx := context.Background()
	m, p := manager(t)
	nonConsoleSession(p)
	m.Configure(Settings{Enabled: true, AllowBackgroundSession: true, AllowForeground: true})
	if _, err := m.Open(ctx, "owner", OpenRequest{Mode: "control", Interaction: "foreground"}); code(err) != "policy_denied" {
		t.Fatal("explicit foreground control ran in a background session without global input", err)
	}
	// An engine that owns a global pointer must not drive a background session through it.
	p.mu.Lock()
	p.info.Capabilities = append(p.info.Capabilities, "pointer", "keyboard", "text")
	p.info.Engine = string(EngineAgentDesktop)
	p.mu.Unlock()
	if _, err := m.Open(ctx, "owner", OpenRequest{Mode: "control", Interaction: "foreground"}); code(err) != "policy_denied" {
		t.Fatal("agent-desktop foreground control ran in a background session", err)
	}
	if _, err := m.Open(ctx, "owner", OpenRequest{Mode: "observe", Interaction: "foreground"}); err != nil {
		t.Fatal("observation was refused in a background session", err)
	}
}

func TestDisablingBackgroundSessionRevokesItsSessions(t *testing.T) {
	ctx := context.Background()
	m, p := manager(t)
	nonConsoleSession(p)
	m.Configure(Settings{Enabled: true, AllowBackgroundSession: true})
	if _, err := m.Open(ctx, "owner", OpenRequest{Mode: "control", TargetID: "window-one"}); err != nil {
		t.Fatal(err)
	}
	m.Configure(Settings{Enabled: true})
	if sessions := m.Sessions("owner"); len(sessions) != 0 {
		t.Fatal("background session survived allow_background_session being turned off", sessions)
	}
}

func TestEngineDoesNotAdoptItsGrantWhenNativeSessionIsInactive(t *testing.T) {
	own := Info{ProtocolVersion: ProtocolVersion, Available: true, State: "ready", DesktopID: "desktop", Epoch: "e", LayoutVersion: "l", Capabilities: []string{"screenshot", "pointer", "keyboard"}}
	inactive := Info{ProtocolVersion: ProtocolVersion, State: "session_inactive", Reason: "session_inactive", Capabilities: []string{}}
	info, err := combineInfo(own, nil, inactive, nil)
	if err != nil || info.Available || info.State != "session_inactive" || slices.Contains(info.Capabilities, "pointer") {
		t.Fatal("engine grant stood in for an inactive native session", info, err)
	}
	ready := Info{ProtocolVersion: ProtocolVersion, Available: true, State: "ready", DesktopID: "desktop", Epoch: "e", LayoutVersion: "l", Capabilities: []string{"screenshot", "background:screenshot"}}
	info, err = combineInfo(own, nil, ready, nil)
	if err != nil || !info.Available || !slices.Contains(info.Capabilities, "engine:agent-desktop") || slices.Contains(info.Capabilities, "pointer") {
		t.Fatal("ready native session did not keep the native capability set", info, err)
	}
}

func TestEngineDoesNotAdoptItsGrantInALockedBackgroundSession(t *testing.T) {
	own := Info{ProtocolVersion: ProtocolVersion, Available: true, State: "ready", DesktopID: "desktop", Epoch: "e", LayoutVersion: "l", Capabilities: []string{"screenshot", "pointer", "keyboard"}}
	locked := Info{ProtocolVersion: ProtocolVersion, State: "locked", Reason: "locked", Session: "background", Capabilities: []string{}}
	info, err := combineInfo(own, nil, locked, nil)
	if err != nil || info.Available || info.State != "locked" || info.Session != "background" || slices.Contains(info.Capabilities, "pointer") {
		t.Fatal("engine grant stood in for a locked background session", info, err)
	}
}
