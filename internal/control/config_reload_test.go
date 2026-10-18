package control

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// quietPoll keeps the file watcher out of the way, so the explicit reload is the only thing
// that adopts the file.
func quietPoll(o *Options) { o.Poll = time.Hour }

func (f *fixture) reload() v1.ConfigReloadResponse {
	f.t.Helper()
	r := f.ok("POST", "/v1/config/reload", nil, "ConfigReloadResponse")
	var out v1.ConfigReloadResponse
	decodeAs(f.t, r, &out)
	return out
}

func Test_ControlConfigReloadAppliesFileEdit(t *testing.T) {
	f := newFixture(t, quietPoll)
	events := f.stream("")
	if err := os.WriteFile(f.path, []byte(strings.Replace(testConfig, "allow_dangerous: false", "allow_dangerous: true", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	got := f.reload()
	if !got.Applied || !got.Valid || got.LoadError != nil || got.RestartRequired || len(got.RestartPaths) != 0 {
		t.Fatalf("%+v", got)
	}
	if !slices.Equal(got.ChangedPaths, []string{"/agent/policy/allow_dangerous"}) {
		t.Fatalf("changed_paths = %v", got.ChangedPaths)
	}
	if !f.store.Get().Agent.Policy.AllowDangerous {
		t.Fatal("reload did not make the edit the generation in effect")
	}
	if got.EffectiveETag != got.ETag {
		t.Fatalf("effective %s != etag %s after an applied edit", got.EffectiveETag, got.ETag)
	}
	var data v1.ConfigEventData
	payload(t, events.next(t, ofType(v1.EventConfig)), &data)
	if data.Change != "reloaded" || data.Actor != v1.ActorControl || data.ETag == nil || *data.ETag != got.ETag {
		t.Fatalf("%+v", data)
	}

	// Nothing changed since: the call reports the state and announces nothing.
	again := f.reload()
	if again.Applied || !again.Valid || len(again.ChangedPaths) != 0 || again.ETag != got.ETag {
		t.Fatalf("%+v", again)
	}
}

// A section that needs a restart is written and reported, and the running value stays.
func Test_ControlConfigReloadReportsRestartSections(t *testing.T) {
	f := newFixture(t, quietPoll)
	if err := os.WriteFile(f.path, []byte(strings.Replace(testConfig, "name: ctl-node", "name: renamed-node", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	got := f.reload()
	if !got.Applied || !got.RestartRequired || !slices.Equal(got.RestartPaths, []string{"/node/name"}) {
		t.Fatalf("%+v", got)
	}
	if got.EffectiveETag == got.ETag {
		t.Fatal("a pending restart still reports the file as effective")
	}
	if f.store.Get().Node.Name != "ctl-node" {
		t.Fatal("restart-only section changed in the running node")
	}
}

// Invalid content is reported with a load error, the event says so, and the last good
// generation keeps running.
func Test_ControlConfigReloadRejectsInvalidFile(t *testing.T) {
	f := newFixture(t, quietPoll)
	events := f.stream("")
	if err := os.WriteFile(f.path, []byte("node: ["), 0600); err != nil {
		t.Fatal(err)
	}
	got := f.reload()
	if got.Applied || got.Valid || got.LoadError == nil || got.ETag == got.EffectiveETag {
		t.Fatalf("%+v", got)
	}
	var data v1.ConfigEventData
	payload(t, events.next(t, ofType(v1.EventConfig)), &data)
	if data.Change != "reload_failed" || data.Actor != v1.ActorControl {
		t.Fatalf("%+v", data)
	}
	if f.store.Get().Node.Name != "ctl-node" {
		t.Fatal("the node dropped its last good configuration")
	}
	if !f.store.Failing() {
		t.Fatal("store does not report the failing file")
	}
}

// Invalid content that failed earlier is still a failed reload when it comes back, even though
// the store does not count it a second time.
func Test_ControlConfigReloadRepeatedInvalidFileStillFails(t *testing.T) {
	f := newFixture(t, quietPoll)
	events := f.stream("")
	invalid := []byte("node: [")
	var data v1.ConfigEventData
	for range 2 {
		if err := os.WriteFile(f.path, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if got := f.reload(); got.Valid || got.LoadError == nil {
			t.Fatalf("%+v", got)
		}
		payload(t, events.next(t, ofType(v1.EventConfig)), &data)
		if data.Change != "reload_failed" {
			t.Fatalf("%+v", data)
		}
		if err := os.WriteFile(f.path, []byte(testConfig), 0600); err != nil {
			t.Fatal(err)
		}
		if got := f.reload(); !got.Valid || got.LoadError != nil {
			t.Fatalf("%+v", got)
		}
		payload(t, events.next(t, ofType(v1.EventConfig)), &data)
		if data.Change != "reloaded" {
			t.Fatalf("%+v", data)
		}
	}
}

// fakeApplier stands in for the node's role runtime.
type fakeApplier struct {
	errs    []v1.ApplyError
	retries []bool
}

func (a *fakeApplier) Settle(_ context.Context, retry bool) []v1.ApplyError {
	a.retries = append(a.retries, retry)
	return a.errs
}

// What the node could not put into effect comes back with the reload, and an explicit reload asks it
// to try again what failed before.
func Test_ControlConfigReloadReportsApplyErrors(t *testing.T) {
	applier := &fakeApplier{}
	f := newFixture(t, quietPoll, func(o *Options) { o.Applier = applier })
	if got := f.reload(); got.ApplyErrors == nil || len(got.ApplyErrors) != 0 {
		t.Fatalf("a clean reload must report an empty list: %+v", got)
	}
	applier.errs = []v1.ApplyError{{Section: "gateway", Message: "网关没能改到监听地址 :8080，仍在 127.0.0.1:8080 上"}}
	got := f.reload()
	if len(got.ApplyErrors) != 1 || got.ApplyErrors[0].Section != "gateway" || got.RestartRequired {
		t.Fatalf("%+v", got)
	}
	if !slices.Equal(applier.retries, []bool{true, true}) {
		t.Fatalf("an explicit reload did not ask for a retry: %v", applier.retries)
	}
}

// node.roles is one change in the diff, judged by the roles that moved: the gateway comes and goes
// without a restart, the agent does not.
func Test_ControlConfigDiffJudgesRolesBySet(t *testing.T) {
	before := map[string]any{"node": map[string]any{"name": "n", "roles": []any{"agent", "gateway"}}}
	cases := []struct {
		roles   []any
		changes int
		restart bool
	}{
		{[]any{"agent"}, 1, false},
		{[]any{"gateway", "agent"}, 1, false},
		{[]any{"agent", "gateway"}, 0, false},
		{[]any{"gateway"}, 1, true},
		{[]any{"agent", "gateway", "ingress"}, 1, true},
	}
	for _, c := range cases {
		after := map[string]any{"node": map[string]any{"name": "n", "roles": c.roles}}
		diff := diffConfigs(before, after)
		if len(diff) != c.changes {
			t.Fatalf("%v: %d changes: %+v", c.roles, len(diff), diff)
		}
		if c.changes == 1 && (diff[0].Pointer != "/node/roles" || diff[0].RestartRequired != c.restart) {
			t.Fatalf("%v: %+v", c.roles, diff[0])
		}
	}
}
