package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
)

func setupFixture(t *testing.T) (*Store, Input, Host, *[]string) {
	t.Helper()
	root := t.TempDir()
	s, _ := Open(filepath.Join(root, "state"))
	workspace := filepath.Join(root, "项目 with spaces")
	if e := os.Mkdir(workspace, 0700); e != nil {
		t.Fatal(e)
	}
	in := Input{SchemaVersion: 1, Purpose: "local", NodeName: "test-node", Workspace: workspace, Permission: "readonly", Clients: []string{}, Network: NetworkInput{Mode: "lan"}}
	calls := []string{}
	installed := false
	h := Host{Installed: func(*Plan) (bool, error) { return installed, nil }, Install: func(context.Context, *Plan) error { calls = append(calls, "install"); installed = true; return nil }, Start: func(context.Context, *Plan) error { calls = append(calls, "start"); return nil }, Remove: func(context.Context, *Plan) error { calls = append(calls, "remove"); installed = false; return nil }, Network: func(context.Context, *Plan, func(mesh.LoginProgress) error) (string, error) {
		return "node.tailnet.ts.net", nil
	}, Health: func(context.Context, *Plan) error {
		if !installed {
			return errors.New("not started")
		}
		return nil
	}, Evidence: func(context.Context, *Plan, *Job) (bool, error) { return false, nil }}
	return s, in, h, &calls
}

func TestPlanProfilesAndSchema(t *testing.T) {
	s, in, _, _ := setupFixture(t)
	for _, purpose := range []string{"local", "multi-device", "cloud"} {
		t.Run(purpose, func(t *testing.T) {
			value := in
			value.Purpose = purpose
			value.Network = NetworkInput{}
			p, e := s.Plan(value)
			if e != nil {
				t.Fatal(e)
			}
			cfg, e := config.Parse([]byte(p.Config))
			if e != nil {
				t.Fatal(e)
			}
			if cfg.SchemaVersion != 2 {
				t.Fatal("setup configuration version", cfg.SchemaVersion)
			}
			if cfg.Gateway.Listen[:10] != "127.0.0.1:" {
				t.Fatal(cfg.Gateway.Listen)
			}
			if purpose == "local" && cfg.Mesh.LAN.MDNS {
				t.Fatal("local mode advertised")
			}
			if cfg.Gateway.PATs["onboarding"].Scopes[2] != "fs:read:*" {
				t.Fatal("read-only token scope missing")
			}
			for _, grant := range cfg.Agent.Authz {
				for _, tool := range grant.Tools {
					if tool == "*" || strings.HasPrefix(tool, "exec.") || strings.HasPrefix(tool, "pty.") {
						t.Fatal("read-only grant", tool)
					}
				}
			}
			if _, e := os.Stat(p.ConfigPath); !os.IsNotExist(e) {
				t.Fatal("plan wrote configuration")
			}
		})
	}
}

func TestCoexistSetupUsesSharedDeviceConfiguration(t *testing.T) {
	s, in, _, _ := setupFixture(t)
	in.Purpose = "multi-device"
	in.Network = NetworkInput{Mode: "coexist", AuthMethod: "browser", ControlURL: config.DefaultControlURL}
	plan, err := s.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(plan.Config))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != 2 || !cfg.Mesh.LANEnabled() || !cfg.Mesh.TailnetEnabled() || cfg.Mesh.Tailnet.AuthMethod != "browser" {
		t.Fatal("coexist paths", cfg.Mesh)
	}
}

func TestRunPersistsWaitAndResumeWithoutReinstall(t *testing.T) {
	s, in, h, calls := setupFixture(t)
	p, e := s.Plan(in)
	if e != nil {
		t.Fatal(e)
	}
	j, e := s.CreateJob(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Run(context.Background(), j.ID, h); e != nil {
		t.Fatal(e)
	}
	j, e = s.LoadJob(j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if j.Status != "waiting_user" || !j.Readiness.ServiceReady || j.Readiness.ClientVerified {
		t.Fatalf("incorrect readiness: %+v", j)
	}
	if len(*calls) != 1 {
		t.Fatal(*calls)
	}
	if e = s.Run(context.Background(), j.ID, h); e != nil {
		t.Fatal(e)
	}
	if len(*calls) != 1 {
		t.Fatal("reinstalled service", *calls)
	}
	h.Evidence = func(context.Context, *Plan, *Job) (bool, error) { return true, nil }
	j, e = s.Verify(context.Background(), j.ID, h)
	if e != nil {
		t.Fatal(e)
	}
	if j.Status != "succeeded" || !j.Readiness.ClientVerified {
		t.Fatal(j.Status)
	}
	if !strings.Contains(j.VerificationPath, j.ID) || j.VerificationTool != "local/fs.stat" {
		t.Fatal("missing unique challenge")
	}
}

func TestPlansRejectConcurrentChangesAndJobsSerialize(t *testing.T) {
	s, in, _, _ := setupFixture(t)
	p, e := s.Plan(in)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(p.ConfigPath), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p.ConfigPath, []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateJob(p.ID); e == nil {
		t.Fatal("accepted stale plan")
	}
	_ = os.Remove(p.ConfigPath)
	j, e := s.CreateJob(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	same, e := s.CreateJob(p.ID)
	if e != nil || same.ID != j.ID {
		t.Fatal("duplicate job", e)
	}
	p2, e := s.Plan(in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateJob(p2.ID); e == nil {
		t.Fatal("parallel node tasks accepted")
	}
	unlock, e := s.lock(in.NodeName)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.lock(in.NodeName); e == nil {
		t.Fatal("lock not exclusive")
	}
	unlock()
}

func TestFailureRestoresConfigAndCanRetry(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	p, e := s.Plan(in)
	if e != nil {
		t.Fatal(e)
	}
	j, e := s.CreateJob(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	install := h.Install
	h.Install = func(context.Context, *Plan) error { return errors.New("secret-output-not-for-report") }
	if e = s.Run(context.Background(), j.ID, h); e != nil {
		t.Fatal(e)
	}
	j, e = s.LoadJob(j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if j.Status != "failed" {
		t.Fatal(j.Status)
	}
	if _, e = os.Stat(p.ConfigPath); !os.IsNotExist(e) {
		t.Fatal("config not restored")
	}
	b, _ := json.Marshal(j)
	if strings.Contains(string(b), "secret-output") {
		t.Fatal("raw process output leaked")
	}
	h.Install = install
	if e = s.Run(context.Background(), j.ID, h); e != nil {
		t.Fatal(e)
	}
	j, _ = s.LoadJob(j.ID)
	if !j.Readiness.ServiceReady {
		t.Fatal(j)
	}
}

func TestExistingConfigPreservesBackendsAndCredentials(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	p, e := s.Plan(in)
	if e != nil {
		t.Fatal(e)
	}
	j, e := s.CreateJob(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Run(context.Background(), j.ID, h); e != nil {
		t.Fatal(e)
	}
	j, _ = s.LoadJob(j.ID)
	if _, e = s.Cancel(j.ID); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(p.ConfigPath)
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if e = json.Unmarshal(raw, &doc); e != nil {
		t.Fatal(e)
	}
	agent := doc["agent"].(map[string]any)
	agent["backends"] = map[string]any{"existing": map[string]any{"transport": "stdio", "command": []string{"old-mcp"}, "policy": "shared"}}
	raw, _ = json.Marshal(doc)
	if e = os.WriteFile(p.ConfigPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	in.Permission = "development"
	p, e = s.Plan(in)
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := config.Parse([]byte(p.Config))
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Agent.Backends["existing"].Command[0] != "old-mcp" {
		t.Fatal("backend lost")
	}
}

func TestNetworkWaitResumesIdentityAndCloudHasScopedIngress(t *testing.T) {
	s, in, h, _ := setupFixture(t)
	in.Purpose = "cloud"
	in.Network = NetworkInput{Mode: "tailnet"}
	p, e := s.Plan(in)
	if e != nil {
		t.Fatal(e)
	}
	j, e := s.CreateJob(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	h.Network = func(ctx context.Context, p *Plan, progress func(mesh.LoginProgress) error) (string, error) {
		if e := progress(mesh.LoginProgress{State: "needs_login", URL: "https://login.tailscale.com/a/test"}); e != nil {
			return "", e
		}
		saved, e := s.LoadJob(j.ID)
		if e != nil || saved.Status != "waiting_user" || saved.AuthURL == "" {
			t.Fatal("login progress not durable", e)
		}
		return "node.tailnet.ts.net", nil
	}
	if e = s.Run(context.Background(), j.ID, h); e != nil {
		t.Fatal(e)
	}
	j, _ = s.LoadJob(j.ID)
	if j.IngressURL != "https://node.tailnet.ts.net/mcp" {
		t.Fatal(j.IngressURL)
	}
	cfg, e := config.Load(p.ConfigPath)
	if e != nil {
		t.Fatal(e)
	}
	if !cfg.HasRole("ingress") || len(cfg.Ingress.OAuth.DynamicClientScopes) != 3 {
		t.Fatal("ingress scope missing")
	}
	if !strings.HasPrefix(j.VerificationTool, "test-node/") {
		t.Fatal(j.VerificationTool)
	}
}

func TestInputValidationAndPathTraversal(t *testing.T) {
	s, in, _, _ := setupFixture(t)
	for _, mutate := range []func(*Input){func(v *Input) { v.NodeName = "../escape" }, func(v *Input) { v.Workspace = "relative" }, func(v *Input) { v.Purpose = "unknown" }, func(v *Input) { v.Clients = []string{"codex", "codex"} }, func(v *Input) { v.SchemaVersion = 2 }, func(v *Input) {
		v.Purpose = "cloud"
		v.Network = NetworkInput{Mode: "tailnet", ControlURL: "https://example.com"}
	}} {
		v := in
		mutate(&v)
		if _, e := s.Plan(v); e == nil {
			t.Fatal("invalid input accepted", v)
		}
	}
	if _, e := s.LoadJob("../escape"); e == nil {
		t.Fatal("invalid id accepted")
	}
}
