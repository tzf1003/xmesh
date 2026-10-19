package computer

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"sync"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

type fakeDesktop struct {
	mu       sync.Mutex
	info     Info
	calls    int
	releases []Execution
	start    chan struct{}
	unblock  chan struct{}
	err      error
}

func newFake() *fakeDesktop {
	return &fakeDesktop{info: Info{ProtocolVersion: 1, Available: true, State: "ready", DesktopID: "desktop", Epoch: "boot", LayoutVersion: "layout1", Capabilities: []string{"screenshot", "pointer", "keyboard", "text"}, Displays: []Display{{ID: "display", Width: 100, Height: 80, Scale: 1}}}}
}
func (f *fakeDesktop) Info(context.Context) (Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, nil
}
func (f *fakeDesktop) Capture(context.Context, CaptureRequest) (Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 100, 80)))
	return Frame{Epoch: f.info.Epoch, DisplayID: "display", LayoutVersion: f.info.LayoutVersion, Width: 100, Height: 80, Region: Region{Width: 200, Height: 160}, Image: b.Bytes()}, nil
}
func (f *fakeDesktop) Execute(ctx context.Context, e Execution) (ActionResult, error) {
	f.mu.Lock()
	f.calls++
	start, unblock, err := f.start, f.unblock, f.err
	f.mu.Unlock()
	if start != nil {
		select {
		case start <- struct{}{}:
		default:
		}
	}
	if unblock != nil {
		select {
		case <-unblock:
		case <-ctx.Done():
			return ActionResult{}, ctx.Err()
		}
	}
	return ActionResult{State: "executed", Executed: len(e.Actions)}, err
}
func (f *fakeDesktop) Release(_ context.Context, e Execution) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, e)
	return nil
}
func (f *fakeDesktop) Close() error { return nil }

func manager(t *testing.T) (*Manager, *fakeDesktop) {
	t.Helper()
	p := newFake()
	m := New(p, Settings{Enabled: true})
	t.Cleanup(func() { _ = m.Close() })
	return m, p
}
func open(t *testing.T, m *Manager, owner, mode string) Session {
	t.Helper()
	s, e := m.Open(context.Background(), owner, OpenRequest{Mode: mode})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func request(s Session) SessionRequest {
	return SessionRequest{SessionID: s.ID, Epoch: s.Epoch, Token: s.Token}
}
func code(err error) string {
	var e *toolerr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
func point(n int) *int { return &n }
func click(t *testing.T, m *Manager, owner string, s Session, id string) ActRequest {
	t.Helper()
	f, e := m.Observe(context.Background(), owner, ObserveRequest{SessionRequest: request(s)})
	if e != nil {
		t.Fatal(e)
	}
	return ActRequest{SessionRequest: request(s), ActionID: id, FrameID: f.ID, Actions: []Action{{Type: "click", X: point(50), Y: point(40)}}}
}

func TestDesktopControlIsExclusiveAndOwned(t *testing.T) {
	m, _ := manager(t)
	s := open(t, m, "a/1", "control")
	_ = open(t, m, "b/2", "observe")
	_, err := m.Open(context.Background(), "b/2", OpenRequest{Mode: "control"})
	if code(err) != "resource_busy" {
		t.Fatal(err)
	}
	_, err = m.Renew("b/2", RenewRequest{SessionRequest: request(s)})
	if code(err) != "not_owner" {
		t.Fatal(err)
	}
	wrong := request(s)
	wrong.Token = "forged"
	if code(m.CloseSession("a/1", wrong)) != "lease_required" {
		t.Fatal("forged token closed control")
	}
	if err = m.CloseSession("a/1", request(s)); err != nil {
		t.Fatal(err)
	}
	next := open(t, m, "b/2", "control")
	if next.Token == s.Token {
		t.Fatal("fence reused")
	}
	if len(m.Sessions("a/1")) != 0 || len(m.Sessions("b/2")) != 2 {
		t.Fatal("session filtering failed")
	}
}

func TestActionsAreIdempotentAndPayloadBound(t *testing.T) {
	m, p := manager(t)
	s := open(t, m, "a/1", "control")
	r := click(t, m, "a/1", s, "one")
	r.ObserveAfter = true
	for range 2 {
		v, e := m.Act(context.Background(), "a/1", r)
		if e != nil || v.State != "executed" || v.Frame == nil || len(v.Frame.Image) == 0 {
			t.Fatal(v, e)
		}
	}
	p.mu.Lock()
	n := p.calls
	p.mu.Unlock()
	if n != 1 {
		t.Fatal("repeated input", n)
	}
	r.Actions[0].X = point(51)
	if _, err := m.Act(context.Background(), "a/1", r); code(err) != "conflict" {
		t.Fatal(err)
	}
	v, err := m.ActionStatus("a/1", StatusRequest{SessionRequest: request(s), ActionID: "one"})
	if err != nil || v.State != "executed" {
		t.Fatal(v, err)
	}
}

func TestLostResponseCanBeQueriedWithoutRepeatingInput(t *testing.T) {
	m, p := manager(t)
	p.start = make(chan struct{}, 1)
	p.unblock = make(chan struct{})
	s := open(t, m, "a/1", "control")
	r := click(t, m, "a/1", s, "slow")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v, e := m.Act(ctx, "a/1", r)
	if e != nil || v.State != "accepted" {
		t.Fatal(v, e)
	}
	select {
	case <-p.start:
	case <-time.After(time.Second):
		t.Fatal("execution did not start")
	}
	close(p.unblock)
	v, e = m.Act(context.Background(), "a/1", r)
	if e != nil || v.State != "executed" {
		t.Fatal(v, e)
	}
	p.mu.Lock()
	n := p.calls
	p.mu.Unlock()
	if n != 1 {
		t.Fatal(n)
	}
}

func TestDispatchFailureIsUnknown(t *testing.T) {
	m, p := manager(t)
	p.err = errors.New("transport lost after input")
	s := open(t, m, "a/1", "control")
	r := click(t, m, "a/1", s, "uncertain")
	v, e := m.Act(context.Background(), "a/1", r)
	if e != nil || v.State != "unknown" {
		t.Fatal(v, e)
	}
	_, _ = m.Act(context.Background(), "a/1", r)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != 1 {
		t.Fatal("unknown input retried")
	}
}

func TestLayoutAndDesktopEpochFenceOldObservations(t *testing.T) {
	m, p := manager(t)
	s := open(t, m, "a/1", "control")
	r := click(t, m, "a/1", s, "old-layout")
	p.mu.Lock()
	p.info.LayoutVersion = "layout2"
	p.mu.Unlock()
	v, e := m.Act(context.Background(), "a/1", r)
	if e != nil || v.State != "failed" || v.Error != "stale_frame" {
		t.Fatal(v, e)
	}
	p.mu.Lock()
	p.info.Epoch = "newboot"
	p.mu.Unlock()
	_, e = m.Observe(context.Background(), "a/1", ObserveRequest{SessionRequest: request(s)})
	if code(e) != "conflict" {
		t.Fatal(e)
	}
	if len(m.Sessions("a/1")) != 0 {
		t.Fatal("stale session retained")
	}
}

func TestPauseAndDisconnectCancelQueuedActions(t *testing.T) {
	m, p := manager(t)
	p.start = make(chan struct{}, 1)
	p.unblock = make(chan struct{})
	s := open(t, m, "a/1", "control")
	r := click(t, m, "a/1", s, "running")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = m.Act(ctx, "a/1", r)
	select {
	case <-p.start:
	case <-time.After(time.Second):
		t.Fatal("execution did not start")
	}
	r.ActionID = "queued"
	_, _ = m.Act(ctx, "a/1", r)
	m.Pause(true)
	if len(m.Sessions("a/1")) != 0 {
		t.Fatal("pause retained sessions")
	}
	_, err := m.Open(context.Background(), "b/2", OpenRequest{Mode: "control"})
	if code(err) != "policy_denied" {
		t.Fatal(err)
	}
	m.Pause(false)
	other := open(t, m, "b/2", "control")
	m.Disconnect("b/2")
	if len(m.Sessions("b/2")) != 0 {
		t.Fatal("disconnect retained control", other)
	}
	close(p.unblock)
}

func TestLeaseExpiryAndConfigurationDisable(t *testing.T) {
	m, _ := manager(t)
	s := open(t, m, "a/1", "control")
	m.mu.Lock()
	m.sessions[s.ID].ExpiresAt = time.Now().Add(-time.Second)
	m.mu.Unlock()
	_, err := m.Renew("a/1", RenewRequest{SessionRequest: request(s)})
	if code(err) != "session_not_found" {
		t.Fatal(err)
	}
	_ = open(t, m, "b/2", "control")
	m.Configure(Settings{})
	if len(m.Sessions("")) != 0 {
		t.Fatal("disabled retained control")
	}
	if _, err = m.Info(context.Background()); code(err) != "policy_denied" {
		t.Fatal(err)
	}
}

func TestActionValidationAndObservationRequired(t *testing.T) {
	m, _ := manager(t)
	s := open(t, m, "a/1", "control")
	for _, a := range []Action{{Type: "shell", Text: "x"}, {Type: "text", Text: "x", X: point(1)}, {Type: "click", X: point(1)}, {Type: "drag", X: point(1), Y: point(1), ToX: point(2)}, {Type: "key"}, {Type: "text", Text: "\x00"}} {
		_, err := m.Act(context.Background(), "a/1", ActRequest{SessionRequest: request(s), ActionID: "bad", Actions: []Action{a}})
		if code(err) != "invalid_args" {
			t.Fatal(a, err)
		}
	}
	_, err := m.Act(context.Background(), "a/1", ActRequest{SessionRequest: request(s), ActionID: "no-frame", Actions: []Action{{Type: "click", X: point(1), Y: point(1)}}})
	if code(err) != "conflict" {
		t.Fatal(err)
	}
	observer := open(t, m, "b/2", "observe")
	_, err = m.Act(context.Background(), "b/2", ActRequest{SessionRequest: request(observer), ActionID: "read-only", Actions: []Action{{Type: "text", Text: "x"}}})
	if code(err) != "lease_required" {
		t.Fatal(err)
	}
}

func TestImagesAreBoundedAndNotSerializedInMetadata(t *testing.T) {
	m, _ := manager(t)
	s := open(t, m, "a/1", "observe")
	for range 10 {
		if _, err := m.Observe(context.Background(), "a/1", ObserveRequest{SessionRequest: request(s)}); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	count := len(m.sessions[s.ID].frames)
	m.mu.Unlock()
	if count != 4 {
		t.Fatal(count)
	}
	m.Configure(Settings{Enabled: true, MaxImageBytes: 1})
	if _, err := m.Observe(context.Background(), "a/1", ObserveRequest{SessionRequest: request(s)}); code(err) != "too_large" {
		t.Fatal(err)
	}
}
