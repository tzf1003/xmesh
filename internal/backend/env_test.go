package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/lease"
	"github.com/tzf1003/xmesh/internal/mcpmux"
)

func envFixture(t *testing.T, env map[string]map[string]string) *Manager {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	bin, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	leases := lease.New()
	t.Cleanup(leases.Close)
	command := []string{bin, "-test.run=TestBackendProcess", "--", "--xmesh-backend-helper"}
	c := &config.Config{}
	c.Agent.DefaultCWD = root
	c.Agent.CWDPolicy = cwd.Policy{Allow: []string{root}}
	c.Agent.Backends = map[string]config.Backend{
		"ida":   {Transport: "stdio", Command: command, Policy: "shared"},
		"other": {Transport: "stdio", Command: command, Policy: "shared"},
	}
	m := New(c, leases, nil, nil, WithEnv(env))
	t.Cleanup(m.Close)
	m.waitFor(t, "ida", "running")
	m.waitFor(t, "other", "running")
	return m
}

// waitFor blocks until the named backend reports state, which also proves a replaced actor came up.
func (m *Manager) waitFor(t *testing.T, name, state string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m.States()[name] == state {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s is %q, want %q", name, m.States()[name], state)
}

func (m *Manager) actorOf(name string) *actor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.instances[name]
}

func envValue(t *testing.T, m *Manager, backend, variable string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, e := m.Call(ctx, "n/a", backend+".env", raw(map[string]any{"name": variable}), mcpmux.Progress{})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(out.StructuredContent)
	var got struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	return got.Value
}

// conformance: CON-TOOL-12
//
// A stdio backend starts with the variables configured for it, a change restarts only that
// backend, and a provider credential name that policy refuses for callers is accepted here.
func Test_BackendEnvReachesTheProcessAndRestartsOnlyThatBackend(t *testing.T) {
	m := envFixture(t, map[string]map[string]string{"ida": {"API_KEY": "one", "OPENAI_API_KEY": "provider"}})
	if got := envValue(t, m, "ida", "API_KEY"); got != "one" {
		t.Fatalf("API_KEY = %q, want the configured value", got)
	}
	if got := envValue(t, m, "ida", "OPENAI_API_KEY"); got != "provider" {
		t.Fatalf("OPENAI_API_KEY = %q, want the configured value", got)
	}
	if got := envValue(t, m, "other", "OPENAI_API_KEY"); got != "" {
		t.Fatalf("another backend saw %q", got)
	}
	ida, other := m.actorOf("ida"), m.actorOf("other")

	m.SetEnv(map[string]map[string]string{"ida": {"OPENAI_API_KEY": "rotated", "SERVICE_URL": "https://example.com"}})
	m.waitFor(t, "ida", "running")
	if m.actorOf("ida") == ida {
		t.Fatal("ida kept its process after its variables changed")
	}
	if m.actorOf("other") != other {
		t.Fatal("other restarted although its variables did not change")
	}
	if got := envValue(t, m, "ida", "OPENAI_API_KEY"); got != "rotated" {
		t.Fatalf("after change OPENAI_API_KEY = %q", got)
	}
	if got := envValue(t, m, "ida", "SERVICE_URL"); got != "https://example.com" {
		t.Fatalf("SERVICE_URL = %q", got)
	}

	// The same variables again are not a change.
	ida = m.actorOf("ida")
	m.SetEnv(map[string]map[string]string{"ida": {"SERVICE_URL": "https://example.com", "OPENAI_API_KEY": "rotated"}})
	if m.actorOf("ida") != ida {
		t.Fatal("ida restarted for an identical set of variables")
	}

	m.SetEnv(nil)
	m.waitFor(t, "ida", "running")
	if got := envValue(t, m, "ida", "OPENAI_API_KEY"); got != "" {
		t.Fatalf("variable survived its removal: %q", got)
	}
}

// conformance: CON-TOOL-12
//
// Loader and shell hijack names stay refused even for the operator, so the backend never starts.
func Test_BackendEnvStillRefusesHijackNames(t *testing.T) {
	m := envFixture(t, nil)
	m.SetEnv(map[string]map[string]string{"ida": {"NODE_OPTIONS": "--require /tmp/x.js"}})
	m.waitFor(t, "ida", "crashed")
	if m.States()["other"] != "running" {
		t.Fatal("an unrelated backend was affected", m.States())
	}
}

// A remote backend has no environment, so a variable written for its name restarts nothing.
func Test_BackendEnvIgnoredForRemoteBackends(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	leases := lease.New()
	t.Cleanup(leases.Close)
	c := &config.Config{}
	c.Agent.DefaultCWD = root
	c.Agent.CWDPolicy = cwd.Policy{Allow: []string{root}}
	c.Agent.Backends = map[string]config.Backend{"remote": {Transport: "http", URL: "http://127.0.0.1:1/mcp", Policy: "shared"}}
	m := New(c, leases, nil, nil)
	t.Cleanup(m.Close)
	before := m.actorOf("remote")
	m.SetEnv(map[string]map[string]string{"remote": {"TOKEN": "x"}})
	if m.actorOf("remote") != before {
		t.Fatal("a remote backend restarted for a variable it cannot use")
	}
}
