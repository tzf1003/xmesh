package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/backendenv"
)

type envFixture struct {
	*fixture
	store   *backendenv.Store
	applied atomic.Int32
}

// newEnvFixture is newFixture with a backend environment store in the state directory and an
// audit log, so the tests can look for a value anywhere the kernel might have left one.
func newEnvFixture(t *testing.T) *envFixture {
	t.Helper()
	e := &envFixture{}
	var log *audit.Logger
	e.fixture = newFixture(t, func(o *Options) {
		store, err := backendenv.Open(filepath.Join(o.StateDir, backendenv.FileName))
		if err != nil {
			t.Fatal(err)
		}
		e.store = store
		o.BackendEnv = store
		o.ApplyBackendEnv = func() { e.applied.Add(1) }
		if log, err = audit.New(o.AuditDir, 0); err != nil {
			t.Fatal(err)
		}
		o.Audit = log
	})
	t.Cleanup(func() { log.Close() })
	return e
}

func (e *envFixture) patchEnv(backend string, body any) *Response {
	e.t.Helper()
	return e.do("PATCH", "/v1/backends/"+backend+"/env", body, nil)
}

func (e *envFixture) addBackend(name string) {
	e.t.Helper()
	r := e.patch(e.config().ETag, false, v1.ConfigEdit{Path: "/agent/backends/" + name, Value: map[string]any{"transport": "stdio", "command": []string{"node", "server.js"}, "policy": "shared"}})
	if r.Status != 200 {
		e.t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// conformance: CTR-CTL-14
//
// A value is written once and never read back: not in the response, the configuration, the
// status, the audit record or the event stream; the file that holds it is private.
func Test_ControlBackendEnvKeepsValuesWriteOnly(t *testing.T) {
	const secret = "sk-test-0123456789-not-a-real-key"
	f := newEnvFixture(t)
	f.addBackend("github")

	r := f.patchEnv("github", v1.BackendEnvPatch{Set: map[string]string{"OPENAI_API_KEY": secret, "API_BASE": "https://example.com/v1"}})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	valid(t, "BackendEnvResponse", r.Body)
	var out v1.BackendEnvResponse
	decodeAs(t, r, &out)
	if out.Name != "github" || !slices.Equal(out.Names, []string{"API_BASE", "OPENAI_API_KEY"}) {
		t.Fatalf("%+v", out)
	}
	if f.applied.Load() != 1 {
		t.Fatalf("the kernel was told %d times, want 1", f.applied.Load())
	}
	if got := f.store.Snapshot()["github"]["OPENAI_API_KEY"]; got != secret {
		t.Fatalf("stored %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(f.store.Path())
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("env file %v %v, want 0600", info, err)
		}
	}

	// Nothing the channel serves carries the value.
	for _, path := range []string{"/v1/config", "/v1/status", "/v1/audit", "/v1/pats"} {
		if body := f.do("GET", path, nil, nil).Body; strings.Contains(string(body), secret) {
			t.Fatalf("GET %s returned the value: %s", path, body)
		}
	}
	if strings.Contains(f.file(), secret) {
		t.Fatal("the configuration file holds the value")
	}
	if strings.Contains(string(r.Body), secret) {
		t.Fatalf("the response holds the value: %s", r.Body)
	}

	// Unset and a new value in one call: unset first, then set.
	r = f.patchEnv("github", v1.BackendEnvPatch{Unset: []string{"API_BASE"}, Set: map[string]string{"EXTRA": "1"}})
	decodeAs(t, r, &out)
	if !slices.Equal(out.Names, []string{"EXTRA", "OPENAI_API_KEY"}) {
		t.Fatalf("%s", r.Body)
	}
	if r = f.do("DELETE", "/v1/backends/github/env", nil, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if len(f.store.Snapshot()) != 0 || f.applied.Load() != 3 {
		t.Fatalf("after DELETE: %v, applied %d", f.store.Snapshot(), f.applied.Load())
	}
	// Clearing what is not there is not an error.
	if r = f.do("DELETE", "/v1/backends/github/env", nil, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

// conformance: CTR-CTL-02 CTR-CTL-14
func Test_ControlBackendEnvAuditHoldsKeyNamesOnly(t *testing.T) {
	const secret = "audit-must-not-see-this"
	f := newEnvFixture(t)
	f.patchEnv("github", v1.BackendEnvPatch{Set: map[string]string{"TOKEN": secret}})
	r := f.ok("GET", "/v1/audit?event=control_call", nil, "AuditResponse")
	if !strings.Contains(string(r.Body), "control.backends.env.patch") {
		t.Fatalf("the change was not audited: %s", r.Body)
	}
	if strings.Contains(string(r.Body), secret) || strings.Contains(string(r.Body), "TOKEN") {
		t.Fatalf("a variable reached the audit record: %s", r.Body)
	}
}

func Test_ControlBackendEnvRejectsWithoutWriting(t *testing.T) {
	f := newEnvFixture(t)
	big := map[string]string{}
	for i := 0; i <= backendenv.MaxVars; i++ {
		big["V"+strings.Repeat("X", i)] = "1"
	}
	reason := func(r *Response) string {
		var e v1.Error
		_ = json.Unmarshal(r.Body, &e)
		return e.Reason
	}
	cases := map[string]struct {
		path string
		body any
	}{
		"empty body object":      {"github", map[string]any{}},
		"empty lists":            {"github", v1.BackendEnvPatch{Set: map[string]string{}, Unset: []string{}}},
		"bad backend name":       {"-bad", v1.BackendEnvPatch{Set: map[string]string{"A": "1"}}},
		"bad variable name":      {"github", v1.BackendEnvPatch{Set: map[string]string{"1A": "1"}}},
		"bad unset name":         {"github", v1.BackendEnvPatch{Unset: []string{"A=B"}}},
		"nul in value":           {"github", v1.BackendEnvPatch{Set: map[string]string{"A": "x\x00y"}}},
		"value too long":         {"github", v1.BackendEnvPatch{Set: map[string]string{"A": strings.Repeat("x", backendenv.MaxValueBytes+1)}}},
		"set and unset one name": {"github", v1.BackendEnvPatch{Set: map[string]string{"A": "1"}, Unset: []string{"A"}}},
		"too many variables":     {"github", v1.BackendEnvPatch{Set: big}},
		"unknown field":          {"github", map[string]any{"set": map[string]string{"A": "1"}, "values": true}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			apiError(t, f.patchEnv(c.path, c.body), v1.ErrInvalidArgs)
		})
	}
	// Policy refuses names that would hijack the process, and the caller is told when it saves.
	r := f.patchEnv("github", v1.BackendEnvPatch{Set: map[string]string{"LD_PRELOAD": "/tmp/x.so"}})
	apiError(t, r, v1.ErrInvalidArgs)
	if reason(r) != "env_denied" {
		t.Fatalf("reason %q: %s", reason(r), r.Body)
	}
	if len(f.store.Snapshot()) != 0 || f.applied.Load() != 0 {
		t.Fatalf("a rejected request changed state: %v, applied %d", f.store.Snapshot(), f.applied.Load())
	}
	if _, err := os.Stat(f.store.Path()); !os.IsNotExist(err) {
		t.Fatalf("a rejected request created the file: %v", err)
	}
	// A provider credential name is the point of the feature, so it is accepted.
	if r = f.patchEnv("github", v1.BackendEnvPatch{Set: map[string]string{"ANTHROPIC_API_KEY": "k", "AWS_REGION": "eu"}}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func Test_ControlBackendEnvUnavailableWithoutStore(t *testing.T) {
	f := newFixture(t)
	apiError(t, f.do("PATCH", "/v1/backends/github/env", v1.BackendEnvPatch{Set: map[string]string{"A": "1"}}, nil), v1.ErrUnavailable)
	apiError(t, f.do("DELETE", "/v1/backends/github/env", nil, nil), v1.ErrUnavailable)
}

// conformance: CTR-CTL-14
//
// Removing a backend through the channel removes its variables with it, while variables stored
// ahead of the backend, or for another one, stay.
func Test_ControlBackendEnvGoesWithItsBackend(t *testing.T) {
	f := newEnvFixture(t)
	f.addBackend("github")
	f.addBackend("docs")
	for _, name := range []string{"github", "docs", "planned"} {
		if r := f.patchEnv(name, v1.BackendEnvPatch{Set: map[string]string{"TOKEN": name}}); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
	before := f.applied.Load()
	if r := f.patch(f.config().ETag, false, v1.ConfigEdit{Path: "/agent/backends/github", Delete: true}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	got := f.store.Snapshot()
	if _, present := got["github"]; present || got["docs"]["TOKEN"] != "docs" || got["planned"]["TOKEN"] != "planned" {
		t.Fatalf("after deleting github: %v", got)
	}
	if f.applied.Load() != before+1 {
		t.Fatalf("the kernel was told %d times about the prune, want 1", f.applied.Load()-before)
	}
	// A change that removes no backend leaves every variable and does not notify.
	if r := f.patch(f.config().ETag, false, v1.ConfigEdit{Path: "/agent/disabled_tools", Value: []string{"exec.run"}}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if len(f.store.Snapshot()) != 2 || f.applied.Load() != before+1 {
		t.Fatalf("an unrelated change touched the variables: %v", f.store.Snapshot())
	}
}
