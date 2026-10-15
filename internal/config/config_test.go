package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	for _, name := range []string{"config-relay.json", "config-funnel-with-domain.json", "config-pat-empty-scopes.json"} {
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
