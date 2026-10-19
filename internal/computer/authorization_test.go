package computer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gatedObservationDesktop struct {
	*fakeDesktop
	beforeInfo    func(context.Context) error
	beforeCapture func(context.Context) error
}

func (p *gatedObservationDesktop) Info(ctx context.Context) (Info, error) {
	if p.beforeInfo != nil {
		if err := p.beforeInfo(ctx); err != nil {
			return Info{}, err
		}
	}
	return p.fakeDesktop.Info(ctx)
}

func (p *gatedObservationDesktop) Capture(ctx context.Context, request CaptureRequest) (Frame, error) {
	if p.beforeCapture != nil {
		if err := p.beforeCapture(ctx); err != nil {
			return Frame{}, err
		}
	}
	return p.fakeDesktop.Capture(ctx, request)
}

func TestObservationRevocationFencesPreflightCaptureAndSessionCreation(t *testing.T) {
	for _, stage := range []string{"preflight", "capture", "open"} {
		t.Run(stage, func(t *testing.T) {
			var block, observe atomic.Bool
			observe.Store(true)
			entered, resume := make(chan struct{}, 1), make(chan struct{})
			gate := func(ctx context.Context) error {
				if !block.Load() {
					return nil
				}
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-resume:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			p := &gatedObservationDesktop{fakeDesktop: newFake()}
			if stage == "capture" {
				p.beforeCapture = gate
			} else {
				p.beforeInfo = gate
			}
			m := New(p, Settings{Enabled: true})
			t.Cleanup(func() { _ = m.Close() })
			m.SetAuthorization(func(_ string, tool string) bool { return tool != "computer.observe" || observe.Load() })
			s := open(t, m, "viewer/session", "observe")
			block.Store(true)
			result := make(chan error, 1)
			go func() {
				if stage == "open" {
					_, err := m.Open(context.Background(), "viewer/session", OpenRequest{Mode: "observe"})
					result <- err
				} else {
					frame, err := m.Observe(context.Background(), "viewer/session", ObserveRequest{SessionRequest: request(s)})
					if len(frame.Image) != 0 {
						result <- errors.New("revoked image returned")
						return
					}
					result <- err
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("observation gate not entered")
			}
			observe.Store(false)
			close(resume)
			select {
			case err := <-result:
				if code(err) != "policy_denied" {
					t.Fatal(stage, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("revoked observation remained blocked")
			}
			p.mu.Lock()
			captures := len(p.captures)
			p.mu.Unlock()
			if stage != "capture" && captures != 0 {
				t.Fatal("preflight revocation reached capture")
			}
			if stage == "capture" && len(m.Sessions("viewer/session")) != 0 {
				t.Fatal("revoked session retained image cache")
			}
		})
	}
}

func TestCancelledInfoDoesNotRevokeAnotherCallersControlLease(t *testing.T) {
	for _, limit := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "deadline"}[limit], func(t *testing.T) {
			var block atomic.Bool
			entered := make(chan struct{}, 1)
			p := &gatedObservationDesktop{fakeDesktop: newFake(), beforeInfo: func(ctx context.Context) error {
				if !block.Load() {
					return nil
				}
				select {
				case entered <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return ctx.Err()
			}}
			m := New(p, Settings{Enabled: true})
			t.Cleanup(func() { _ = m.Close() })
			control := open(t, m, "controller/session", "control")
			block.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			if limit {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			}
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := m.Info(ctx); result <- err }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("info did not reach the provider")
			}
			if !limit {
				cancel()
			}
			err := <-result
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if sessions := m.Sessions("controller/session"); len(sessions) != 1 || sessions[0].ID != control.ID {
				t.Fatal("request cancellation revoked another controller", sessions)
			}
			if _, err := m.Renew("controller/session", RenewRequest{SessionRequest: request(control)}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfirmedExecutorInfoFailureStillRevokesDesktopSessions(t *testing.T) {
	var failed atomic.Bool
	p := &gatedObservationDesktop{fakeDesktop: newFake(), beforeInfo: func(context.Context) error {
		if failed.Load() {
			return errors.New("executor connection closed")
		}
		return nil
	}}
	m := New(p, Settings{Enabled: true})
	t.Cleanup(func() { _ = m.Close() })
	_ = open(t, m, "controller/session", "control")
	_ = open(t, m, "viewer/session", "observe")
	failed.Store(true)
	if _, err := m.Info(context.Background()); err == nil {
		t.Fatal("executor failure was accepted")
	}
	if sessions := m.Sessions(""); len(sessions) != 0 {
		t.Fatal("executor failure retained desktop authority", sessions)
	}
}

type blockingInfoDesktop struct {
	*fakeDesktop
	block   atomic.Bool
	entered chan struct{}
	resume  chan struct{}
}

func (p *blockingInfoDesktop) Info(ctx context.Context) (Info, error) {
	if p.block.Load() {
		select {
		case p.entered <- struct{}{}:
		default:
		}
		select {
		case <-p.resume:
		case <-ctx.Done():
			return Info{}, ctx.Err()
		}
	}
	return p.fakeDesktop.Info(ctx)
}

func TestAuthorizationRevokedDuringNativePreflightPreventsDispatch(t *testing.T) {
	p := &blockingInfoDesktop{fakeDesktop: newFake(), entered: make(chan struct{}, 1), resume: make(chan struct{})}
	m := New(p, Settings{Enabled: true})
	t.Cleanup(func() { _ = m.Close() })
	allowed := atomic.Bool{}
	allowed.Store(true)
	m.SetAuthorization(func(string, string) bool { return allowed.Load() })
	s := open(t, m, "node/session", "control")
	r := click(t, m, "node/session", s, "revoked-during-info")
	p.block.Store(true)
	result := make(chan ActionResult, 1)
	go func() { out, _ := m.Act(context.Background(), "node/session", r); result <- out }()
	select {
	case <-p.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("native preflight did not start")
	}
	allowed.Store(false)
	close(p.resume)
	select {
	case out := <-result:
		if out.State != "failed" || out.Error != "policy_denied" || out.Executed != 0 {
			t.Fatal(out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked action did not complete")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != 0 {
		t.Fatal("revoked action reached native input", p.calls)
	}
}

func TestQueuedActionsRecheckCurrentAuthorization(t *testing.T) {
	m, _ := manager(t)
	s := open(t, m, "node/session", "control")
	r := click(t, m, "node/session", s, "revoked")
	m.SetAuthorization(func(string, string) bool { return false })
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
