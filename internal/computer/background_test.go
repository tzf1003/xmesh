package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestNativeReleaseMetadataOmitsAbsentActions(t *testing.T) {
	raw, err := json.Marshal(Execution{Epoch: "epoch", Token: "1"})
	if err != nil || bytes.Contains(raw, []byte(`"actions":null`)) {
		t.Fatal("native Vec<Action> cannot decode a null actions field", string(raw), err)
	}
}

func backgroundManager(t *testing.T) (*Manager, *fakeDesktop) {
	t.Helper()
	manager, provider := manager(t)
	provider.info.DefaultInteraction = "background"
	provider.info.Capabilities = append(provider.info.Capabilities, "background:screenshot", "background:pointer", "background:keyboard")
	provider.info.UserInputMonitor = true
	provider.info.Targets = []Target{{ID: "window-one", AppID: "test.app", Name: "Test", LayoutVersion: "window-layout", Width: 200, Height: 160, UserInputSerial: 7}, {ID: "window-two", AppID: "other.app", LayoutVersion: "other-layout", Width: 200, Height: 160}}
	return manager, provider
}

func TestBackgroundSessionBindsTargetAndRequiresExplicitForeground(t *testing.T) {
	m, p := backgroundManager(t)
	if _, err := m.Open(context.Background(), "owner", OpenRequest{Mode: "control"}); code(err) != "invalid_args" {
		t.Fatal("background default accepted missing target", err)
	}
	if _, err := m.Open(context.Background(), "owner", OpenRequest{Mode: "control", TargetID: "unknown"}); code(err) != "not_found" {
		t.Fatal(err)
	}
	if _, err := m.Open(context.Background(), "owner", OpenRequest{Mode: "control", Interaction: "foreground", TargetID: "window-one"}); code(err) != "invalid_args" {
		t.Fatal(err)
	}
	session, err := m.Open(context.Background(), "owner", OpenRequest{Mode: "control", TargetID: "window-one"})
	if err != nil || session.Interaction != "background" || session.TargetID != "window-one" {
		t.Fatal(session, err)
	}
	if _, err = m.Observe(context.Background(), "owner", ObserveRequest{SessionRequest: request(session), CaptureRequest: CaptureRequest{TargetID: "window-two"}}); code(err) != "invalid_args" {
		t.Fatal("session target could be changed", err)
	}
	frame, err := m.Observe(context.Background(), "owner", ObserveRequest{SessionRequest: request(session)})
	if err != nil || frame.TargetID != "window-one" || frame.LayoutVersion != "window-layout" {
		t.Fatal(frame, err)
	}
	result, err := m.Act(context.Background(), "owner", ActRequest{SessionRequest: request(session), FrameID: frame.ID, ActionID: "background-click", Actions: []Action{{Type: "click", X: point(10), Y: point(10)}}})
	if err != nil || result.State != "executed" {
		t.Fatal(result, err)
	}
	p.mu.Lock()
	executed := p.executions[0]
	p.mu.Unlock()
	if executed.Interaction != "background" || executed.TargetID != "window-one" || executed.Frame.TargetID != "window-one" {
		t.Fatal("target did not reach provider", executed)
	}
	if err = m.CloseSession("owner", request(session)); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Open(context.Background(), "owner", OpenRequest{Mode: "control", Interaction: "foreground"}); code(err) != "policy_denied" {
		t.Fatal("remote caller enabled shared foreground input", err)
	}
	m.Configure(Settings{Enabled: true, AllowForeground: true})
	foreground, err := m.Open(context.Background(), "owner", OpenRequest{Mode: "control", Interaction: "foreground"})
	if err != nil || foreground.Interaction != "foreground" || foreground.TargetID != "" {
		t.Fatal(foreground, err)
	}
	m.Configure(Settings{Enabled: true})
	if len(m.Sessions("owner")) != 0 {
		t.Fatal("revoked local foreground permission retained its controller")
	}
}

func TestBackgroundInterventionUsesTargetAppAndInvalidatesClosedWindow(t *testing.T) {
	m, p := backgroundManager(t)
	m.Configure(Settings{Enabled: true, PauseOnUserInput: true})
	session, err := m.Open(context.Background(), "owner", OpenRequest{Mode: "control", TargetID: "window-one"})
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.info.UserInputSerial++
	p.info.Targets[1].UserInputSerial++
	p.mu.Unlock()
	if _, err = m.Info(context.Background()); err != nil || len(m.Sessions("owner")) != 1 {
		t.Fatal("unrelated app input revoked target session", err)
	}
	p.mu.Lock()
	p.info.Targets[0].UserInputSerial++
	p.mu.Unlock()
	if _, err = m.Info(context.Background()); err != nil || len(m.Sessions("owner")) != 0 {
		t.Fatal("target intervention retained control", err)
	}
	m.Pause(false)
	session, err = m.Open(context.Background(), "owner", OpenRequest{Mode: "observe", TargetID: "window-one"})
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.info.Targets = p.info.Targets[1:]
	p.mu.Unlock()
	if _, err = m.Info(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Observe(context.Background(), "owner", ObserveRequest{SessionRequest: request(session)}); code(err) != "session_not_found" {
		t.Fatal("closed target retained observation", err)
	}
}

func TestBackgroundWindowResizeInvalidatesFrameAndBackendCannotFallback(t *testing.T) {
	m, p := backgroundManager(t)
	session, err := m.Open(context.Background(), "owner", OpenRequest{Mode: "control", TargetID: "window-one"})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := m.Observe(context.Background(), "owner", ObserveRequest{SessionRequest: request(session)})
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.info.Targets[0].LayoutVersion = "resized"
	p.mu.Unlock()
	result, err := m.Act(context.Background(), "owner", ActRequest{SessionRequest: request(session), FrameID: frame.ID, ActionID: "stale-window", Actions: []Action{{Type: "click", X: point(10), Y: point(10)}}})
	if err != nil || result.State != "failed" || result.Error != "stale_frame" {
		t.Fatal(result, err)
	}
	p.mu.Lock()
	p.info.Capabilities = []string{"screenshot", "pointer", "keyboard"}
	calls := p.calls
	p.mu.Unlock()
	if _, err = m.Info(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(m.Sessions("owner")) != 0 {
		t.Fatal("background backend fell back to foreground")
	}
	if _, err = m.Open(context.Background(), "other", OpenRequest{Mode: "control", Interaction: "background", TargetID: "window-one"}); code(err) != "desktop_unavailable" {
		t.Fatal(err)
	}
}
