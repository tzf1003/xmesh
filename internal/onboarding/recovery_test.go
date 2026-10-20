package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/oauth"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

func TestResumeAndVerifyRejectChangedCommittedConfig(t *testing.T) {
	for _, action := range []string{"resume", "verify"} {
		t.Run(action, func(t *testing.T) {
			s, in, h, calls := setupFixture(t)
			p, err := s.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			j, err := s.CreateJob(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Run(t.Context(), j.ID, h); err != nil {
				t.Fatal(err)
			}
			beforeCalls := len(*calls)
			changed := append(readClientConfig(t, p.ConfigPath), []byte("\n# concurrent user edit\n")...)
			if err := os.WriteFile(p.ConfigPath, changed, 0600); err != nil {
				t.Fatal(err)
			}
			h.Evidence = func(context.Context, *Plan, *Job) (bool, error) { return true, nil }
			if action == "resume" {
				err = s.Run(t.Context(), j.ID, h)
			} else {
				_, err = s.Verify(t.Context(), j.ID, h)
			}
			if err != nil {
				t.Fatal(err)
			}
			j, err = s.LoadJob(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if j.Status != "failed" || j.Readiness.ClientVerified || len(*calls) != beforeCalls || !bytes.Equal(readClientConfig(t, p.ConfigPath), changed) {
				t.Fatal("changed config was accepted or modified")
			}
		})
	}
}

func TestVerifyRequiresCompletedPrerequisites(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	j.Readiness.Installed = true
	if err := s.SaveJob(j); err != nil {
		t.Fatal(err)
	}
	called := false
	h.Health = func(context.Context, *Plan) error { called = true; return nil }
	h.Evidence = func(context.Context, *Plan, *Job) (bool, error) { called = true; return true, nil }
	if _, err := s.Verify(t.Context(), j.ID, h); err == nil || called {
		t.Fatal("pending configuration became verified")
	}
	for i := range j.Steps {
		j.Steps[i].Status = "succeeded"
	}
	j.Status = "cancelled"
	if err := s.SaveJob(j); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(t.Context(), j.ID, h); err == nil || called {
		t.Fatal("cancelled task became verified")
	}
}

func TestConfigIntentRecoversBothSidesOfAtomicWrite(t *testing.T) {
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
			j.PendingConfigHash = digest([]byte(p.Config))
			if err := s.SaveJob(j); err != nil {
				t.Fatal(err)
			}
			if written {
				if err := s.writeConfig(p, j); err != nil {
					t.Fatal(err)
				}
				j.ConfigHash = ""
				j.PendingConfigHash = digest([]byte(p.Config))
				if err := s.SaveJob(j); err != nil {
					t.Fatal(err)
				}
				if err := atomicWrite(p.ConfigPath, []byte(p.Config)); err != nil {
					t.Fatal(err)
				}
			}
			s, err = Open(s.Root)
			if err != nil {
				t.Fatal(err)
			}
			j, err = s.LoadJob(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.reconcileConfig(p, j); err != nil {
				t.Fatal(err)
			}
			if j.PendingConfigHash != "" || (written && j.ConfigHash != digest([]byte(p.Config))) {
				t.Fatal("intent was not reconciled")
			}
			if err := s.Run(t.Context(), j.ID, h); err != nil {
				t.Fatal(err)
			}
			j, err = s.LoadJob(j.ID)
			if err != nil || j.Status != "waiting_user" {
				t.Fatal("recovered task could not continue", err)
			}
		})
	}
}

func TestConfigRecoveryAcceptsOnlyJournaledRestore(t *testing.T) {
	s, in, _, _ := setupFixture(t)
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeConfig(p, j); err != nil {
		t.Fatal(err)
	}
	for i := range j.Steps {
		if j.Steps[i].ID == "write-config" {
			j.Steps[i].Status = "succeeded"
		}
	}
	if err := os.Remove(p.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileConfig(p, j); err == nil {
		t.Fatal("unexplained restoration was accepted")
	}
	j.PendingConfigHash = p.BeforeHash
	j.PendingRestore = true
	if err := s.SaveJob(j); err != nil {
		t.Fatal(err)
	}
	j, err = s.LoadJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileConfig(p, j); err != nil {
		t.Fatal(err)
	}
	if j.ConfigHash != "" || j.PendingConfigHash != "" || j.PendingRestore {
		t.Fatal("completed restore retained pending state")
	}
	for _, step := range j.Steps {
		if (step.ID == "write-config" || step.ID == "network") && step.Status != "pending" {
			t.Fatal("recovered restore did not reset dependent steps")
		}
	}
	if err := s.reconcileConfig(p, j); err != nil {
		t.Fatal(err)
	}
}

func TestIngressIntentRecoversConfigBeyondBasePlan(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	in.Purpose = "cloud"
	in.Network = NetworkInput{Mode: "tailnet"}
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
	j, err = s.LoadJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	finalHash := j.ConfigHash
	j.ConfigHash = digest([]byte(p.Config))
	j.PendingConfigHash = finalHash
	if err = s.SaveJob(j); err != nil {
		t.Fatal(err)
	}
	j, err = s.LoadJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.reconcileConfig(p, j); err != nil {
		t.Fatal(err)
	}
	if j.ConfigHash != finalHash || j.PendingConfigHash != "" {
		t.Fatal("ingress intent was not recovered")
	}
}

func TestConfigAtomicCASPreservesLaterEdit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "xmesh.yaml")
	putClientConfig(t, path, "original")
	expected := digest([]byte("original"))
	putClientConfig(t, path, "later-user-edit")
	if err := atomicWriteCAS(path, expected, []byte("replacement")); err == nil {
		t.Fatal("stale CAS write succeeded")
	}
	if string(readClientConfig(t, path)) != "later-user-edit" {
		t.Fatal("CAS overwrote later edit")
	}
	files, err := filepath.Glob(filepath.Join(root, ".setup-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("temporary config leaked")
	}
}

func TestConfigCASRechecksAfterConcurrentWriter(t *testing.T) {
	s, in, _, _ := setupFixture(t)
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	putClientConfig(t, p.ConfigPath, "original")
	lock, err := lockClientPath(p.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	done := make(chan error, 1)
	go func() { done <- atomicWriteCAS(p.ConfigPath, digest([]byte("original")), []byte("replacement")) }()
	select {
	case err := <-done:
		t.Fatalf("writer bypassed held lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	putClientConfig(t, p.ConfigPath, "concurrent-edit")
	lock.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale update accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not finish")
	}
	if string(readClientConfig(t, p.ConfigPath)) != "concurrent-edit" {
		t.Fatal("concurrent edit lost")
	}
}

func TestWriteConfigValidatesExistingPrivateFiles(t *testing.T) {
	for _, kind := range []string{"token-content", "token-mode", "token-directory", "deny-content", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && (kind == "token-mode" || kind == "symlink") {
				t.Skip("Unix file mode and link fixture")
			}
			s, in, h, calls := setupFixture(t)
			p, err := s.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			j, err := s.CreateJob(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Dir(p.ConfigPath)
			path := filepath.Join(dir, "pats", "onboarding.token")
			if kind == "deny-content" {
				path = filepath.Join(dir, "cwd-deny.yaml")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "token-directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				err = os.Symlink(filepath.Join(dir, "outside-token"), path)
			case "token-mode":
				err = os.WriteFile(path, []byte(strings.Repeat("t", 64)), 0644)
			default:
				err = os.WriteFile(path, []byte("invalid-private-fixture: ["), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Run(t.Context(), j.ID, h); err != nil {
				t.Fatal(err)
			}
			j, err = s.LoadJob(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if j.Status != "failed" || len(*calls) != 0 {
				t.Fatal("invalid private file reached service installation")
			}
			if _, err := os.Stat(p.ConfigPath); !os.IsNotExist(err) {
				t.Fatal("invalid private file produced runtime config")
			}
		})
	}
}

func TestNativeServiceBaselineRestoredAndRetried(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "running"}[running], func(t *testing.T) {
			s, in, _, _ := setupFixture(t)
			first, err := s.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			original := first.Config + "# original configuration\n"
			putClientConfig(t, first.ConfigPath, original)
			p, err := s.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			j, err := s.CreateJob(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			baseline := ServiceSnapshot{Installed: true, Running: running, Definition: []byte("original-registration"), Path: "private-registration"}
			current := baseline
			fail := true
			restores, stops := 0, 0
			h := Host{
				Snapshot:  func(context.Context, *Plan) (ServiceSnapshot, error) { return current, nil },
				Installed: func(*Plan) (bool, error) { return current.Installed, nil },
				Configure: func(context.Context, *Plan) error { current.Definition = []byte("selected-registration"); return nil },
				Stop:      func(context.Context, *Plan) error { stops++; current.Running = false; return nil },
				Start:     func(context.Context, *Plan) error { current.Running = true; return nil },
				Health: func(context.Context, *Plan) error {
					if !current.Running || fail {
						return errors.New("service not ready")
					}
					return nil
				},
				Evidence: func(context.Context, *Plan, *Job) (bool, error) { return false, nil },
				Restore: func(ctx context.Context, _ *Plan, snapshot ServiceSnapshot) error {
					if ctx.Err() != nil {
						t.Fatal("cleanup inherited expired request context")
					}
					if string(readClientConfig(t, p.ConfigPath)) != original {
						t.Fatal("native baseline restored before original config")
					}
					restores++
					current = snapshot
					return nil
				},
			}
			if err := s.Run(t.Context(), j.ID, h); err != nil {
				t.Fatal(err)
			}
			j, err = s.LoadJob(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if j.Status != "failed" || j.ServiceRestorePending || restores != 1 || current.Running != running || string(current.Definition) != "original-registration" || j.ConfigHash != "" {
				t.Fatal("native baseline did not recover")
			}
			if running && stops != 1 || !running && stops != 0 {
				t.Fatal("unexpected baseline stop count")
			}
			fail = false
			if err := s.Run(t.Context(), j.ID, h); err != nil {
				t.Fatal(err)
			}
			j, err = s.LoadJob(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if j.Status != "waiting_user" || !j.Readiness.ServiceReady || j.ServiceSnapshot.Running != running {
				t.Fatal("retry lost the native baseline")
			}
		})
	}
}

func TestFailureIsPublishedAfterServiceRecovery(t *testing.T) {
	for _, recoveryFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "restored", true: "recovery_pending"}[recoveryFails], func(t *testing.T) {
			s, in, h, _ := setupFixture(t)
			first, err := s.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			original := first.Config + "# original configuration\n"
			putClientConfig(t, first.ConfigPath, original)
			p, err := s.Plan(in)
			if err != nil {
				t.Fatal(err)
			}
			j, err := s.CreateJob(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			h.Snapshot = func(context.Context, *Plan) (ServiceSnapshot, error) {
				return ServiceSnapshot{Installed: true}, nil
			}
			h.Installed = func(*Plan) (bool, error) { return true, nil }
			h.Health = func(context.Context, *Plan) error { return errors.New("service not ready") }
			recovered := false
			h.Restore = func(context.Context, *Plan, ServiceSnapshot) error {
				// A separate CLI/UI reader must not see a terminal result while
				// the native registration is still being restored.
				visible, err := s.LoadJob(j.ID)
				if err != nil {
					t.Fatal(err)
				}
				if visible.Status != "running" || !visible.ServiceRestorePending {
					t.Fatalf("recovery published premature terminal status: %s", visible.Status)
				}
				if visible.Readiness.ServiceReady || visible.Readiness.ClientVerified {
					t.Fatal("recovery retained connection readiness")
				}
				recovered = true
				if recoveryFails {
					return errors.New("native recovery requires attention")
				}
				return nil
			}
			if err := s.Run(t.Context(), j.ID, h); err != nil {
				t.Fatal(err)
			}
			result, err := s.LoadJob(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !recovered || result.Status != "failed" || result.ServiceRestorePending != recoveryFails {
				t.Fatalf("incorrect completed recovery: %+v", result)
			}
			if string(readClientConfig(t, p.ConfigPath)) != original {
				t.Fatal("terminal failure retained the replacement config")
			}
		})
	}
}

func TestEvidenceRequiresExactChallengeAndPaginates(t *testing.T) {
	_, in, _, _ := setupFixture(t)
	p := &Plan{Input: in}
	j := &Job{CreatedAt: time.Now().Add(-time.Minute), VerificationTool: "local/fs.stat", VerificationPath: filepath.Join(in.Workspace, ".check-nonce")}
	valid := func() map[string]any {
		return map[string]any{"time": time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "event": "tool_call", "status": "ok", "tool": j.VerificationTool, "device": in.NodeName, "args_digest": map[string]any{"resolved_path": j.VerificationPath}}
	}
	mutations := map[string]func(map[string]any){
		"backend-suffix": func(e map[string]any) { e["tool"] = "local/backend-pretend-fs.stat" },
		"agent-event":    func(e map[string]any) { e["tool"] = "fs.stat" },
		"panel":          func(e map[string]any) { e["origin"] = "panel" },
		"other-device":   func(e map[string]any) { e["device"] = "other-node" },
		"other-path":     func(e map[string]any) { e["args_digest"] = map[string]any{"resolved_path": in.Workspace} },
		"old":            func(e map[string]any) { e["time"] = j.CreatedAt.Add(-time.Minute).Format(time.RFC3339Nano) },
		"invalid-time":   func(e map[string]any) { e["time"] = "invalid" },
		"other-event":    func(e map[string]any) { e["event"] = "control_call" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			event := valid()
			mutate(event)
			raw, _ := json.Marshal(event)
			found, err := findClientEvidence(t.Context(), p, j, func(string, *v2.AuditResponse) error { return nil })
			if err != nil || found {
				t.Fatal("empty evidence accepted")
			}
			found, err = findClientEvidence(t.Context(), p, j, func(_ string, out *v2.AuditResponse) error {
				out.Enabled = true
				out.Events = []json.RawMessage{raw}
				return nil
			})
			if err != nil || found {
				t.Fatal("unrelated evidence accepted")
			}
		})
	}
	raw, _ := json.Marshal(valid())
	pages := 0
	found, err := findClientEvidence(t.Context(), p, j, func(path string, out *v2.AuditResponse) error {
		pages++
		out.Enabled = true
		parsed, _ := url.Parse(path)
		if parsed.Query().Get("tool") != j.VerificationTool || parsed.Query().Get("device") != in.NodeName {
			t.Fatal("evidence query was not scoped")
		}
		if pages == 1 {
			next := "page-two"
			out.NextCursor = &next
		} else {
			if parsed.Query().Get("cursor") != "page-two" {
				t.Fatal("cursor missing")
			}
			out.Events = []json.RawMessage{raw}
		}
		return nil
	})
	if err != nil || !found || pages != 2 {
		t.Fatal("paginated challenge was not found", err)
	}
	p.Input.Purpose = "cloud"
	found, err = findClientEvidence(t.Context(), p, j, func(_ string, out *v2.AuditResponse) error {
		out.Enabled = true
		out.Events = []json.RawMessage{raw}
		return nil
	})
	if err != nil || found {
		t.Fatal("cloud evidence accepted without OAuth client")
	}
	pages = 0
	_, err = findClientEvidence(t.Context(), p, j, func(_ string, out *v2.AuditResponse) error {
		pages++
		out.Enabled = true
		next := strings.Repeat("x", pages)
		out.NextCursor = &next
		return nil
	})
	if err != nil || pages != 8 {
		t.Fatal("evidence pagination was not bounded")
	}
}

func TestProbeRequiresExpectedNodeIdentity(t *testing.T) {
	for _, result := range []*mcp.CallToolResult{nil, {IsError: true}, {StructuredContent: map[string]any{"hostname": "other"}}, {StructuredContent: map[string]any{"message": "successful"}}} {
		if validSystemInfo(result, "expected-node") {
			t.Fatal("invalid health identity accepted")
		}
	}
	if !validSystemInfo(&mcp.CallToolResult{StructuredContent: map[string]any{"hostname": "expected-node"}}, "expected-node") {
		t.Fatal("valid node identity rejected")
	}
}

func TestPublicPlanRedactsPreservedPrivateConfiguration(t *testing.T) {
	p := &Plan{Config: "agent:\n  backends:\n    existing:\n      command: [old-mcp, argv-private-fixture]\n      url: https://user:url-private-fixture@example.com/private\n      headers: {Authorization: header-private-fixture}\n      env: {API_KEY: env-private-fixture}\n# comment-private-fixture\n"}
	public := PublicPlan(p)
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"argv-private-fixture", "url-private-fixture", "header-private-fixture", "env-private-fixture", "comment-private-fixture"} {
		if bytes.Contains(encoded, []byte(marker)) {
			t.Fatal("public preview contains private config value")
		}
	}
	if !strings.Contains(p.Config, "argv-private-fixture") || public == p {
		t.Fatal("redaction changed private plan")
	}
}

func TestReadonlyPlanRestrictsBothAuthorizationLayers(t *testing.T) {
	s, in, _, _ := setupFixture(t)
	for _, purpose := range []string{"local", "multi-device", "cloud"} {
		in.Purpose = purpose
		in.Network = NetworkInput{}
		p, err := s.Plan(in)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Parse([]byte(p.Config))
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range []string{"fs.write", "fs.edit", "fs.mkdir", "fs.move", "fs.delete", "fs.upload", "exec.run", "pty.open", "backend.start"} {
			if oauth.Allows(cfg.Gateway.PATs["onboarding"].Scopes, in.NodeName, tool, "") {
				t.Fatal("read-only PAT permits mutation", tool)
			}
			for tag := range cfg.Agent.Authz {
				if agent.Allowed(cfg, identity.Caller{Tags: []string{tag}}, tool, "") {
					t.Fatal("read-only agent grant permits mutation", tool)
				}
			}
		}
	}
}

func TestGatewaySelectionSkipsReachableListeners(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if !connectedGateway(listener.Addr().String()) {
		t.Fatal("active TCP listener was not detected")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if connectedGateway(listener.Addr().String()) {
		t.Fatal("closed TCP listener remained reachable")
	}
}

func TestNewServiceClientFailureRemovesRegistrationBeforeConfig(t *testing.T) {
	paths := isolatedClientPaths(t)
	s, in, h, _ := setupFixture(t)
	in.Clients = []string{"codex"}
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	installed := false
	removed := false
	restored := false
	h.Snapshot = func(context.Context, *Plan) (ServiceSnapshot, error) {
		return ServiceSnapshot{Installed: installed}, nil
	}
	h.Restore = func(context.Context, *Plan, ServiceSnapshot) error { restored = true; return nil }
	h.Installed = func(*Plan) (bool, error) { return installed, nil }
	h.Install = func(context.Context, *Plan) error { installed = true; return nil }
	h.Remove = func(context.Context, *Plan) error {
		if _, err := os.Stat(p.ConfigPath); err != nil {
			t.Fatal("service removal lost config lookup")
		}
		installed = false
		removed = true
		return nil
	}
	h.Health = func(context.Context, *Plan) error {
		if !installed {
			return errors.New("not installed")
		}
		// A client edit after its preview causes the clients step to fail.
		putClientConfig(t, paths["codex"], "model='editor-update'\n")
		return nil
	}
	if err := s.Run(t.Context(), j.ID, h); err != nil {
		t.Fatal(err)
	}
	j, err = s.LoadJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if installed || !removed || !restored || j.Readiness.Installed || j.ServiceOwned || j.ServiceRestorePending {
		t.Fatal("new native registration survived failed setup")
	}
	if _, err := os.Stat(p.ConfigPath); !os.IsNotExist(err) {
		t.Fatal("new config survived completed recovery")
	}
	if string(readClientConfig(t, paths["codex"])) != "model='editor-update'\n" {
		t.Fatal("client editor update changed during rollback")
	}
}

func TestInterruptedNativeRestoreResumesBeforeSetup(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeConfig(p, j); err != nil {
		t.Fatal(err)
	}
	j.ServiceSnapshot = &ServiceSnapshot{Installed: true, Running: true, Definition: []byte("saved-original")}
	j.ServiceStopped = true
	j.ServiceRestorePending = true
	if err := s.restoreConfig(p, j); err != nil {
		t.Fatal(err)
	}
	// This is the state after original config recovery and before native restore.
	restored := false
	h.Restore = func(context.Context, *Plan, ServiceSnapshot) error {
		restored = true
		return errors.New("native-restore-pending")
	}
	h.Install = func(context.Context, *Plan) error {
		t.Fatal("setup continued during pending native recovery")
		return nil
	}
	if err := s.Run(t.Context(), j.ID, h); err != nil {
		t.Fatal(err)
	}
	j, err = s.LoadJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !restored || !j.ServiceRestorePending || j.Status != "failed" {
		t.Fatal("pending native recovery was skipped")
	}
}

func TestPrepareIngressPreservesOAuthClientsAndBudgets(t *testing.T) {
	s, in, _, _ := setupFixture(t)
	in.Purpose = "cloud"
	in.Network = NetworkInput{Mode: "tailnet"}
	p, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeConfig(p, j); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(p.Config), &document); err != nil {
		t.Fatal(err)
	}
	document["ingress"] = map[string]any{"listen": "127.0.0.1:443", "domain": "prior.example", "tls": "terminate", "cert_file": "prior-cert", "key_file": "prior-key", "max_pty_per_client": 3, "pty_budget_file": filepath.Join(s.Root, "pty-budget"), "oauth": map[string]any{"issuer": "https://prior.example", "clients": map[string]any{"saved-client": map[string]any{"scopes": []string{"devices:read"}}}}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	putClientConfig(t, p.ConfigPath, string(raw))
	j.ConfigHash = digest(raw)
	if err := s.SaveJob(j); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareIngress(p, j, "node.tailnet.ts.net"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Ingress.OAuth.Clients["saved-client"].Scopes) != 1 || cfg.Ingress.MaxPTYPerClient != 3 || cfg.Ingress.PTYBudgetFile != filepath.Join(s.Root, "pty-budget") {
		t.Fatal("unrelated ingress settings lost")
	}
	if cfg.Ingress.Listen != "funnel" || cfg.Ingress.OAuth.Issuer != "https://node.tailnet.ts.net" || cfg.Ingress.Domain != "" || cfg.Ingress.TLS != "" || cfg.Ingress.CertFile != "" || cfg.Ingress.KeyFile != "" {
		t.Fatal("Funnel settings not reconciled")
	}
}
