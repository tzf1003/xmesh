package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// reloadFixture is the example configuration written to a temporary file, with edit applied to
// its JSON document on each write.
type reloadFixture struct {
	t    *testing.T
	path string
	base []byte
}

func newReloadFixture(t *testing.T) *reloadFixture {
	t.Helper()
	b, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if err != nil {
		t.Fatal(err)
	}
	f := &reloadFixture{t: t, path: filepath.Join(t.TempDir(), "config.json"), base: b}
	f.write(nil)
	return f
}

func (f *reloadFixture) write(edit func(doc map[string]any)) {
	f.t.Helper()
	body := f.base
	if edit != nil {
		var doc map[string]any
		if err := json.Unmarshal(f.base, &doc); err != nil {
			f.t.Fatal(err)
		}
		edit(doc)
		var err error
		if body, err = json.Marshal(doc); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.path, body, 0600); err != nil {
		f.t.Fatal(err)
	}
}

func section(doc map[string]any, name string) map[string]any {
	m, _ := doc[name].(map[string]any)
	if m == nil {
		m = map[string]any{}
		doc[name] = m
	}
	return m
}

func Test_NeedsRestartPointers(t *testing.T) {
	restart := []string{"", "/node", "/node/name", "/node/roles", "/mesh/mode", "/ingress/oauth/issuer", "/audit/retention", "/agent/policy/windows_appcontainer", "/agent/policy"}
	for _, ptr := range restart {
		if !NeedsRestart(ptr) {
			t.Errorf("%q should need a restart", ptr)
		}
	}
	hot := []string{"/agent/policy/allow_dangerous", "/agent/policy/exec/deny_patterns", "/gateway", "/gateway/listen", "/gateway/expose_backends", "/gateway/quota/calls_per_hour", "/gateway/pats/ci", "/gateway/listen_x", "/agent/quota", "/agent/authz/tag:peer", "/node_x"}
	for _, ptr := range hot {
		if NeedsRestart(ptr) {
			t.Errorf("%q should apply without a restart", ptr)
		}
	}
}

// Every section restartChanged reports must be one NeedsRestart recognises, so the store's
// pending list and the control diff agree.
func Test_RestartChangedUsesRestartPaths(t *testing.T) {
	f := newReloadFixture(t)
	startup, err := Parse(f.base)
	if err != nil {
		t.Fatal(err)
	}
	edits := map[string]func(doc map[string]any){
		"/node/name":                         func(doc map[string]any) { section(doc, "node")["name"] = "other-node" },
		"/node/roles":                        func(doc map[string]any) { section(doc, "node")["roles"] = []string{"gateway"} },
		"/audit":                             func(doc map[string]any) { section(doc, "audit")["retention"] = "24h" },
		"/agent/policy/windows_appcontainer": func(doc map[string]any) { section(section(doc, "agent"), "policy")["windows_appcontainer"] = true },
	}
	for want, edit := range edits {
		var doc map[string]any
		_ = json.Unmarshal(f.base, &doc)
		edit(doc)
		body, _ := json.Marshal(doc)
		next, err := Parse(body)
		if err != nil {
			t.Fatal(want, err)
		}
		got := restartChanged(startup, next)
		if !slices.Contains(got, want) {
			t.Fatalf("%s: restartChanged = %v", want, got)
		}
		for _, ptr := range got {
			if !NeedsRestart(ptr) || !slices.Contains(RestartPaths, ptr) {
				t.Fatalf("%s: %q is not a restart path", want, ptr)
			}
		}
	}
}

func Test_RolesNeedRestart(t *testing.T) {
	cases := []struct {
		before, after []string
		restart       bool
	}{
		{[]string{"agent", "gateway"}, []string{"agent"}, false},
		{[]string{"agent"}, []string{"agent", "gateway"}, false},
		{[]string{"gateway", "agent"}, []string{"agent", "gateway"}, false},
		{[]string{"agent", "gateway"}, []string{"gateway"}, true},
		{[]string{"gateway"}, []string{"gateway", "agent"}, true},
		{[]string{"agent", "gateway"}, []string{"agent", "ingress"}, true},
	}
	for _, c := range cases {
		if got := RolesNeedRestart(c.before, c.after); got != c.restart {
			t.Errorf("%v -> %v: restart = %v", c.before, c.after, got)
		}
	}
}

// The gateway role comes and goes with the generation: it is in the roles of the generation in
// effect at once and asks for no restart, while a role outside HotRoles waits for one.
func Test_HotRolesFollowTheGeneration(t *testing.T) {
	f := newReloadFixture(t)
	s, err := NewStore(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.write(func(doc map[string]any) { section(doc, "node")["roles"] = []string{"agent"} })
	if got := s.Get().Node.Roles; !slices.Equal(got, []string{"agent"}) {
		t.Fatalf("gateway not removed from the roles in effect: %v", got)
	}
	if pending := s.RestartPending(); len(pending) != 0 {
		t.Fatalf("removing the gateway asked for a restart: %v", pending)
	}
	f.write(nil)
	if got := s.Get().Node.Roles; !slices.Equal(got, []string{"agent", "gateway"}) {
		t.Fatalf("gateway not added back: %v", got)
	}
	f.write(func(doc map[string]any) { section(doc, "node")["roles"] = []string{"gateway"} })
	if got := s.Get().Node.Roles; !slices.Equal(got, []string{"agent", "gateway"}) {
		t.Fatalf("a role that needs a restart left the running roles: %v", got)
	}
	if pending := s.RestartPending(); !slices.Equal(pending, []string{"/node/roles"}) {
		t.Fatalf("RestartPending = %v", pending)
	}
}

func Test_RestartPendingListsSections(t *testing.T) {
	f := newReloadFixture(t)
	s, err := NewStore(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.write(func(doc map[string]any) {
		section(doc, "node")["name"] = "renamed"
		section(doc, "agent")["policy"].(map[string]any)["allow_dangerous"] = true
	})
	if !s.Refresh() {
		t.Fatal("a valid edit did not take effect")
	}
	if got := s.RestartPending(); !slices.Equal(got, []string{"/node/name"}) {
		t.Fatalf("RestartPending = %v", got)
	}
	if !s.Get().Agent.Policy.AllowDangerous {
		t.Fatal("hot section did not apply")
	}
	if s.Get().Node.Name == "renamed" {
		t.Fatal("restart-only section applied before restart")
	}
}

func Test_RefreshReportsAdoption(t *testing.T) {
	f := newReloadFixture(t)
	s, err := NewStore(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Refresh() {
		t.Fatal("an unchanged file reported adoption")
	}
	f.write(func(doc map[string]any) { section(section(doc, "agent"), "policy")["allow_dangerous"] = true })
	if !s.Refresh() {
		t.Fatal("a changed file was not adopted")
	}
	if s.Refresh() {
		t.Fatal("the same generation was adopted twice")
	}
	if err := os.WriteFile(f.path, []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if s.Refresh() {
		t.Fatal("an invalid file was adopted")
	}
	if !s.Failing() {
		t.Fatal("invalid file not reported as failing")
	}
}

func Test_SubscribeReceivesEachAdoptedGeneration(t *testing.T) {
	f := newReloadFixture(t)
	s, err := NewStore(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var seen []bool
	unsubscribe := s.Subscribe(func(c *Config) { seen = append(seen, c.Agent.Policy.AllowDangerous) })
	f.write(func(doc map[string]any) { section(section(doc, "agent"), "policy")["allow_dangerous"] = true })
	s.Get()
	s.Get()
	if err := os.WriteFile(f.path, []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	s.Get()
	f.write(nil)
	s.Get()
	unsubscribe()
	f.write(func(doc map[string]any) { section(section(doc, "agent"), "policy")["allow_dangerous"] = true })
	s.Get()
	if want := []bool{true, false}; !slices.Equal(seen, want) {
		t.Fatalf("subscriber saw %v, want %v", seen, want)
	}
}

func Test_WatchDeliversEditsWithoutRequests(t *testing.T) {
	f := newReloadFixture(t)
	s, err := NewStore(f.path)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan struct{}, 1)
	s.Subscribe(func(c *Config) {
		if c.Agent.Policy.AllowDangerous {
			select {
			case delivered <- struct{}{}:
			default:
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Watch(ctx, 5*time.Millisecond)
		close(done)
	}()
	f.write(func(doc map[string]any) { section(section(doc, "agent"), "policy")["allow_dangerous"] = true })
	select {
	case <-delivered:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not deliver the edit")
	}
	cancel()
	<-done
}

func Test_WatchReturnsForStaticStore(t *testing.T) {
	b, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	s := Static(c)
	done := make(chan struct{})
	go func() {
		s.Watch(context.Background(), time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch on a static store did not return")
	}
	if s.Refresh() {
		t.Fatal("static store adopted a generation")
	}
}
