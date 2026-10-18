package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/backendenv"
	"github.com/tzf1003/xmesh/internal/config"
)

func envBackends(a *Agent) []string {
	for _, b := range a.Overview().Backends {
		if b.Name == "ida" {
			return b.EnvNames
		}
	}
	return nil
}

// conformance: CTR-CTL-14
//
// The status lists the variable names of a stdio backend and never a value, and a file edited by
// hand is picked up without a restart of the kernel.
func Test_BackendEnvNamesInOverviewAndHandEditsApply(t *testing.T) {
	path, _ := configuration(t)
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	bin, _ := os.Executable()
	doc["agent"].(map[string]any)["backends"] = map[string]any{"ida": map[string]any{"transport": "stdio", "policy": "shared", "command": []string{bin, "-test.run=TestAgentBackendProcess", "--", "--xmesh-agent-backend-helper"}}}
	raw, _ = json.Marshal(doc)
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	store, e := config.NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	envFile := filepath.Join(t.TempDir(), backendenv.FileName)
	env, e := backendenv.Open(envFile)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = env.Patch("ida", map[string]string{"API_KEY": "secret-value"}, nil); e != nil {
		t.Fatal(e)
	}
	a, e := New(store, WithBackendEnv(env))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	if got := envBackends(a); !slices.Equal(got, []string{"API_KEY"}) {
		t.Fatalf("EnvNames = %v", got)
	}
	if overview, _ := json.Marshal(a.Overview()); strings.Contains(string(overview), "secret-value") {
		t.Fatalf("the overview carries a value: %s", overview)
	}

	if e = os.WriteFile(envFile, []byte(`{"backends":{"ida":{"API_KEY":"rotated","OTHER":"x"}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !slices.Equal(envBackends(a), []string{"API_KEY", "OTHER"}) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := envBackends(a); !slices.Equal(got, []string{"API_KEY", "OTHER"}) {
		t.Fatalf("a hand edit was not applied: %v", got)
	}
	if got := a.Backends.States()["ida"]; got == "" {
		t.Fatal("the backend disappeared")
	}
}
