package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Test_CTRCFG01(t *testing.T) {
	root := "../../docs/contracts/schemas/examples"
	entries, _ := os.ReadDir(root)
	for _, ent := range entries {
		if len(ent.Name()) > 6 && ent.Name()[:6] == "xmesh." {
			t.Run(ent.Name(), func(t *testing.T) {
				if _, e := Load(filepath.Join(root, ent.Name())); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
	for _, name := range []string{"config-relay.json", "config-funnel-with-domain.json", "config-pat-empty-scopes.json", "config-lan-ephemeral.json"} {
		if _, e := Load(filepath.Join(root, "invalid", name)); e == nil {
			t.Fatal("invalid accepted", name)
		}
	}
}

func Test_CTRCFG02_ReloadState(t *testing.T) {
	b, e := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	original := s.Get()
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	doc["node"].(map[string]any)["name"] = "changed-node"
	doc["agent"].(map[string]any)["policy"].(map[string]any)["allow_dangerous"] = true
	changed, _ := json.Marshal(doc)
	if e = os.WriteFile(path, changed, 0600); e != nil {
		t.Fatal(e)
	}
	updated := s.Get()
	if updated.Node.Name != original.Node.Name || !updated.Agent.Policy.AllowDangerous {
		t.Fatal("hot/cold generation")
	}
	if restart, failures := s.State(); !restart || failures != 0 {
		t.Fatal(restart, failures)
	}
	if e = os.WriteFile(path, []byte("invalid: ["), 0600); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if s.Get() != updated {
			t.Fatal("failed config replaced generation")
		}
	}
	if _, failures := s.State(); failures != 1 {
		t.Fatal("failure generation counted repeatedly", failures)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	s.Get()
	if restart, _ := s.State(); restart {
		t.Fatal("reverted cold fields still need restart")
	}
}

func TestIngressPTYBudgetConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.ingress-public.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []any{nil, 3, 0, -1, 1.5, "8"} {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		ingress := doc["ingress"].(map[string]any)
		delete(ingress, "max_pty_per_client")
		if limit != nil {
			ingress["max_pty_per_client"] = limit
		}
		data, _ := json.Marshal(doc)
		cfg, err := Parse(data)
		if limit == nil || limit == 3 {
			if err != nil {
				t.Fatal(err)
			}
			want := 8
			if limit == 3 {
				want = 3
			}
			if cfg.Ingress.MaxPTYPerClient != want {
				t.Fatal("unexpected ingress limit", cfg.Ingress.MaxPTYPerClient)
			}
		} else if err == nil {
			t.Fatal("invalid ingress limit accepted", limit)
		}
	}
}

func TestIngressPTYBudgetFileConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.ingress-public.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "budget.json"), "relative/budget.json", ""} {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		doc["ingress"].(map[string]any)["pty_budget_file"] = path
		data, _ := json.Marshal(doc)
		cfg, err := Parse(data)
		if filepath.IsAbs(path) {
			if err != nil || cfg.Ingress.PTYBudgetFile != path {
				t.Fatal("absolute budget path rejected", err)
			}
		} else if err == nil {
			t.Fatal("invalid budget path accepted", path)
		}
	}
}

func TestIngressPTYBudgetAuthorityConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.ingress-public.json")
	if err != nil {
		t.Fatal(err)
	}
	for authority, valid := range map[string]bool{"": true, "home-server": true, "a": true, "bad/name": false, "-x": false, "with space": false, "under_score": false} {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		if authority != "" {
			doc["ingress"].(map[string]any)["pty_budget_authority"] = authority
		}
		data, _ := json.Marshal(doc)
		cfg, err := Parse(data)
		if valid && (err != nil || cfg.Ingress.PTYBudgetAuthority != authority) {
			t.Fatal("valid authority rejected", authority, err)
		}
		if !valid && err == nil {
			t.Fatal("invalid authority accepted", authority)
		}
	}
}

func Test_CONNET08_RelayMessage(t *testing.T) {
	_, e := Load(filepath.Join("../../docs/contracts/schemas/examples/invalid/config-relay.json"))
	if e == nil {
		t.Fatal("relay accepted")
	}
	if msg := e.Error(); !strings.Contains(msg, "relay") || !strings.Contains(msg, "未实现") {
		t.Fatal("error does not say relay is not implemented:", msg)
	}
	// Other unknown modes remain ordinary schema failures.
	path := filepath.Join(t.TempDir(), "config.json")
	if e = os.WriteFile(path, []byte(`{"node":{"name":"n","roles":["agent"]},"mesh":{"mode":"mesh9"},"agent":{"cwd_policy":{"allow":["/tmp"]}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(path); e == nil || strings.Contains(e.Error(), "未实现") {
		t.Fatal(e)
	}
}
