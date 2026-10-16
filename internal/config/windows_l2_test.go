package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func windowsL2Config(t *testing.T, enabled any) (string, map[string]any) {
	t.Helper()
	b, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	policy := doc["agent"].(map[string]any)["policy"].(map[string]any)
	if enabled != nil {
		policy["windows_appcontainer"] = enabled
	}
	path := filepath.Join(t.TempDir(), "config.json")
	write(t, path, doc)
	return path, doc
}

func write(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerSwitchDefaultsOff(t *testing.T) {
	path, _ := windowsL2Config(t, nil)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Policy.WindowsAppContainer {
		t.Fatal("the Windows AppContainer runner must be opt-in")
	}
	path, _ = windowsL2Config(t, true)
	if c, err = Load(path); err != nil || !c.Agent.Policy.WindowsAppContainer {
		t.Fatal("explicit opt-in was not read", err)
	}
	if _, err = Load(func() string { p, _ := windowsL2Config(t, "yes"); return p }()); err == nil {
		t.Fatal("a non-boolean switch was accepted")
	}
}

// The runner is installed at startup only, so a reload must neither flip the
// switch for later calls nor go unreported.
func Test_WindowsAppContainerSwitchNeedsRestart(t *testing.T) {
	path, doc := windowsL2Config(t, false)
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	doc["agent"].(map[string]any)["policy"].(map[string]any)["windows_appcontainer"] = true
	write(t, path, doc)
	if s.Get().Agent.Policy.WindowsAppContainer {
		t.Fatal("reload applied a cold switch")
	}
	if restart, _ := s.State(); !restart {
		t.Fatal("changing the switch did not require a restart")
	}
	doc["agent"].(map[string]any)["policy"].(map[string]any)["windows_appcontainer"] = false
	write(t, path, doc)
	s.Get()
	if restart, _ := s.State(); restart {
		t.Fatal("reverting the switch still requires a restart")
	}
	doc["agent"].(map[string]any)["policy"].(map[string]any)["allow_dangerous"] = true
	write(t, path, doc)
	if c := s.Get(); !c.Agent.Policy.AllowDangerous {
		t.Fatal("hot policy fields stopped reloading")
	}
	if restart, _ := s.State(); restart {
		t.Fatal("a hot policy change asked for a restart")
	}
}
