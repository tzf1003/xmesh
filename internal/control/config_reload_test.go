package control

import (
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
	if !got.Applied || !got.RestartRequired || !slices.Equal(got.RestartPaths, []string{"/node"}) {
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
