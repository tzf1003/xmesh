package onboarding

import (
	"bytes"
	"context"
	"github.com/tzf1003/xmesh/internal/mesh"
	"os"
	"path/filepath"
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

func TestIdleCancellationRollsBackCompletedSetup(t *testing.T) {
	for _, baseline := range []string{"new", "existing-running", "existing-stopped", "no-snapshot"} {
		t.Run(baseline, func(t *testing.T) {
			s, in, h, _ := setupFixture(t)
			in.Clients = []string{"codex"}
			clientHome := filepath.Join(t.TempDir(), "client")
			t.Setenv("CODEX_HOME", clientHome)
			clientPath := filepath.Join(clientHome, "config.toml")
			originalClient := []byte("model='existing'\n")
			putClientConfig(t, clientPath, string(originalClient))
			first, err := s.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			var original []byte
			existing := baseline == "existing-running" || baseline == "existing-stopped"
			if existing {
				original = []byte(first.Config + "# original\n")
				putClientConfig(t, first.ConfigPath, string(original))
			}
			p, err := s.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			installed, running := existing, baseline == "existing-running"
			before := ServiceSnapshot{Installed: installed, Running: running}
			h.Installed = func(*Plan) (bool, error) { return installed, nil }
			h.Install = func(context.Context, *Plan) error { installed = true; return nil }
			h.Start = func(context.Context, *Plan) error { running = true; return nil }
			h.Stop = func(context.Context, *Plan) error { running = false; return nil }
			h.Configure = func(context.Context, *Plan) error { return nil }
			h.Health = func(context.Context, *Plan) error { return nil }
			h.Remove = func(ctx context.Context, _ *Plan) error {
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				installed = false
				running = false
				return nil
			}
			if baseline != "no-snapshot" {
				h.Snapshot = func(context.Context, *Plan) (ServiceSnapshot, error) {
					return ServiceSnapshot{Installed: installed, Running: running}, nil
				}
				h.Restore = func(ctx context.Context, _ *Plan, snapshot ServiceSnapshot) error {
					if ctx.Err() != nil {
						t.Fatal(ctx.Err())
					}
					installed = snapshot.Installed
					running = snapshot.Running
					return nil
				}
			}
			j, err := s.CreateJob(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Run(t.Context(), j.ID, h); err != nil {
				t.Fatal(err)
			}
			j, _ = s.LoadJob(j.ID)
			if j.Status != "waiting_user" || !j.Readiness.ServiceReady || len(j.Receipts) != 1 {
				t.Fatalf("setup did not reach idle verification: %+v", j)
			}
			pending, err := s.Cancel(j.ID)
			if err != nil || pending.Status != "pending" {
				t.Fatalf("cleanup not queued: %+v %v", pending, err)
			}
			if err = s.Run(t.Context(), j.ID, h); err != nil {
				t.Fatal(err)
			}
			j, _ = s.LoadJob(j.ID)
			if j.Status != "cancelled" || j.Readiness.ServiceReady || len(j.Receipts) != 0 || installed != before.Installed || running != before.Running {
				t.Fatalf("baseline not restored: %+v installed=%v running=%v", j, installed, running)
			}
			got, err := os.ReadFile(p.ConfigPath)
			if existing {
				if err != nil || !bytes.Equal(got, original) {
					t.Fatal("existing config not restored", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("new config retained", err)
			}
			got, err = os.ReadFile(clientPath)
			if err != nil || !bytes.Equal(got, originalClient) {
				t.Fatal("client credentials not restored", err)
			}
			if _, err = s.Cancel(j.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompletedSetupCannotBeCancelled(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	h.Evidence = func(context.Context, *Plan, *Job) (bool, error) { return true, nil }
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Run(t.Context(), j.ID, h); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel(j.ID); err == nil {
		t.Fatal("completed setup cancelled")
	}
	if s.cancelRequested(j.ID) {
		t.Fatal("completed setup received cancellation marker")
	}
}

func TestCancellationReconcilesInterruptedConfigCommit(t *testing.T) {
	for _, written := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-rename", true: "after-rename"}[written], func(t *testing.T) {
			s, in, h, _ := setupFixture(t)
			p, err := s.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			j, err := s.CreateJob(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if written {
				if err = s.writeConfig(p, j); err != nil {
					t.Fatal(err)
				}
				j.ConfigHash = ""
			}
			j.PendingConfigHash = digest([]byte(p.Config))
			if err = s.SaveJob(j); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Cancel(j.ID); err != nil {
				t.Fatal(err)
			}
			if err = s.Run(t.Context(), j.ID, h); err != nil {
				t.Fatal(err)
			}
			j, _ = s.LoadJob(j.ID)
			if j.Status != "cancelled" || j.ConfigHash != "" || j.PendingConfigHash != "" {
				t.Fatalf("interrupted write not restored: %+v", j)
			}
			if _, err = os.Stat(p.ConfigPath); !os.IsNotExist(err) {
				t.Fatal("new config retained", err)
			}
		})
	}
}

func TestCancellationRetainsClientRollbackFailure(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	in.Clients = []string{"codex"}
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	client := filepath.Join(dir, "config.toml")
	putClientConfig(t, client, "model='before'\n")
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Run(t.Context(), j.ID, h); err != nil {
		t.Fatal(err)
	}
	putClientConfig(t, client, "model='concurrent'\n")
	if _, err = s.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Run(t.Context(), j.ID, h); err != nil {
		t.Fatal(err)
	}
	j, _ = s.LoadJob(j.ID)
	if j.Status != "failed" || len(j.Receipts) != 1 {
		t.Fatalf("client conflict lost: %+v", j)
	}
	if string(readClientConfig(t, client)) != "model='concurrent'\n" {
		t.Fatal("concurrent client edit overwritten")
	}
}
