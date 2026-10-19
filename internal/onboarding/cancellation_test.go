package onboarding

import (
	"context"
	"github.com/tzf1003/xmesh/internal/mesh"
	"os"
	"testing"
	"time"
)

func TestCancelRestoresExistingRunningService(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	in.Purpose = "multi-device"
	in.Network = NetworkInput{Mode: "tailnet", AuthMethod: "browser"}
	first, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	original := first.Config + "# original\n"
	putClientConfig(t, first.ConfigPath, original)
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	running := true
	h.Snapshot = func(context.Context, *Plan) (ServiceSnapshot, error) {
		return ServiceSnapshot{Installed: true, Running: running}, nil
	}
	h.Stop = func(context.Context, *Plan) error { running = false; return nil }
	restored := false
	h.Restore = func(ctx context.Context, _ *Plan, _ ServiceSnapshot) error {
		if ctx.Err() != nil {
			t.Fatal("restore inherited cancelled context")
		}
		raw, _ := os.ReadFile(p.ConfigPath)
		if string(raw) != original {
			t.Fatal("restore did not recover configuration")
		}
		running = true
		restored = true
		return nil
	}
	h.Network = func(ctx context.Context, _ *Plan, _ func(mesh.LoginProgress) error) (string, error) {
		if _, err := s.Cancel(j.ID); err != nil {
			return "", err
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	if err = s.Run(t.Context(), j.ID, h); err != nil {
		t.Fatal(err)
	}
	final, err := s.LoadJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != "cancelled" || !running || !restored || final.ServiceRestorePending {
		t.Fatalf("baseline lost on cancel: %+v", final)
	}
}

func TestCancellationBeforeVerificationWaitIsTerminal(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.Evidence = func(context.Context, *Plan, *Job) (bool, error) { _, err := s.Cancel(j.ID); return false, err }
	if err = s.Run(t.Context(), j.ID, h); err != nil {
		t.Fatal(err)
	}
	final, err := s.LoadJob(j.ID)
	if err != nil || final.Status != "cancelled" {
		t.Fatalf("verification stranded cancellation: %+v %v", final, err)
	}
}

func TestCancelActiveAuthorizationWait(t *testing.T) {
	s, in, h, calls := setupFixture(t)
	in.Purpose = "multi-device"
	in.Network = NetworkInput{Mode: "tailnet", AuthMethod: "browser"}
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	h.Network = func(ctx context.Context, _ *Plan, progress func(mesh.LoginProgress) error) (string, error) {
		if err := progress(mesh.LoginProgress{State: "needs_login", URL: "https://login.tailscale.com/a/test"}); err != nil {
			return "", err
		}
		close(ready)
		<-ctx.Done()
		return "", ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(t.Context(), j.ID, h) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("authorization did not wait")
	}
	if _, err := s.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop worker")
	}
	final, err := s.LoadJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != "cancelled" || final.AuthURL != "" || len(*calls) != 0 {
		t.Fatalf("cancelled job continued: %+v calls=%v", final, *calls)
	}
	if _, err = s.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.QueueResume(j.ID); err == nil {
		t.Fatal("cancelled task resumed")
	}
}

func TestResumeFailedTaskPublishesPending(t *testing.T) {
	s, in, _, _ := setupFixture(t)
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	j.Status = "failed"
	if err = s.SaveJob(j); err != nil {
		t.Fatal(err)
	}
	queued, launch, err := s.QueueResume(j.ID)
	if err != nil || !launch || queued.Status != "pending" {
		t.Fatalf("resume not pollable: %+v launch=%v error=%v", queued, launch, err)
	}
}
