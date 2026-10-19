package control

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/filelock"
)

func TestStoreConfigRechecksSharedLock(t *testing.T) {
	f := newFixture(t)
	old := []byte(f.file())
	lock, err := filelock.Acquire(t.Context(), f.path+".xmesh.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := f.server.storeConfigLocked(old, append(append([]byte{}, old...), []byte("\n# replacement\n")...), nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("write bypassed shared lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	later := append(append([]byte{}, old...), []byte("\n# later edit\n")...)
	if err = os.WriteFile(f.path, later, 0600); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	select {
	case err := <-done:
		if err != errConfigChanged {
			t.Fatal("expected stale configuration", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal(context.DeadlineExceeded)
	}
	if f.file() != string(later) {
		t.Fatal("newer config overwritten")
	}
}

func (f *fixture) config() v1.ConfigResponse {
	f.t.Helper()
	r := f.ok("GET", "/v1/config", nil, "ConfigResponse")
	var out v1.ConfigResponse
	decodeAs(f.t, r, &out)
	if tag := r.Header.Get("ETag"); tag != `"`+out.ETag+`"` {
		f.t.Fatalf("ETag header %q vs body %q", tag, out.ETag)
	}
	return out
}
func (f *fixture) file() string {
	f.t.Helper()
	b, err := os.ReadFile(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// put sends a whole-file replacement guarded by etag (empty means no If-Match header).
func (f *fixture) put(etag, content string, dry bool) *Response {
	f.t.Helper()
	headers := map[string]string{}
	if etag != "" {
		headers["If-Match"] = `"` + etag + `"`
	}
	return f.do("PUT", "/v1/config", v1.ConfigPutRequest{Content: content, DryRun: dry}, headers)
}

func (f *fixture) putOK(etag, content string, dry bool) v1.ConfigPutResponse {
	f.t.Helper()
	r := f.put(etag, content, dry)
	if r.Status != 200 {
		f.t.Fatalf("%d %s", r.Status, r.Body)
	}
	valid(f.t, "ConfigPutResponse", r.Body)
	var out v1.ConfigPutResponse
	decodeAs(f.t, r, &out)
	return out
}

func Test_ControlConfigGet(t *testing.T) {
	f := newFixture(t)
	c := f.config()
	if c.Content != testConfig || !c.Valid || c.LoadError != nil || c.RestartRequired || c.EffectiveETag != c.ETag || !strings.HasPrefix(c.ETag, "sha256-") {
		t.Fatalf("%+v", c)
	}
	if c.Parsed["node"].(map[string]any)["name"] != "ctl-node" {
		t.Fatalf("%+v", c.Parsed)
	}
	// an invalid file on disk is returned as text with a load error, and is not effective
	if err := os.WriteFile(f.path, []byte("node: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bad := f.config()
	if bad.Valid || bad.Parsed != nil || bad.LoadError == nil || bad.LoadError.Line == nil || *bad.LoadError.Line != 1 || bad.EffectiveETag != c.ETag || bad.ETag == c.ETag {
		t.Fatalf("%+v", bad)
	}
}

// The client sends the whole document it wants; comments and layout are the client's own.
func Test_ControlConfigPutKeepsContentVerbatim(t *testing.T) {
	f := newFixture(t)
	before := f.config()
	next := strings.Replace(strings.Replace(testConfig, "allow_dangerous: false", "allow_dangerous: true", 1), "default_cwd: .", "default_cwd: .\n  exec:\n    max_concurrent: 2", 1)
	got := f.putOK(before.ETag, next, false)
	if !got.Applied || got.RestartRequired || len(got.Diff) != 2 {
		t.Fatalf("%+v", got)
	}
	if f.file() != next {
		t.Fatalf("the file is not the submitted content:\n%s", f.file())
	}
	for _, keep := range []string{"# 节点配置", "# 节点名", "# 危险命令开关"} {
		if !strings.Contains(f.file(), keep) {
			t.Errorf("%q lost", keep)
		}
	}
	if c := f.store.Get(); !c.Agent.Policy.AllowDangerous || c.Agent.Exec.MaxConcurrent != 2 {
		t.Fatalf("store not reloaded: %+v", c.Agent.Policy)
	}
	after := f.config()
	if after.ETag != got.ETag || after.EffectiveETag != after.ETag {
		t.Fatalf("%+v vs %+v", after, got)
	}
	ops := map[string]v1.ConfigChange{}
	for _, c := range got.Diff {
		ops[c.Pointer] = c
	}
	danger, exec := ops["/agent/policy/allow_dangerous"], ops["/agent/exec"]
	if danger.Op != "replace" || string(danger.Before) != "false" || string(danger.After) != "true" || !danger.WeakensSecurity || danger.RestartRequired {
		t.Fatalf("%+v", danger)
	}
	if exec.Op != "add" || exec.Before != nil || !strings.Contains(string(exec.After), "max_concurrent") || exec.WeakensSecurity {
		t.Fatalf("%+v", exec)
	}
}

func Test_ControlConfigPreconditions(t *testing.T) {
	f := newFixture(t)
	etag := f.config().ETag
	next := strings.Replace(testConfig, "mdns: true", "mdns: true\n  # edited", 1)
	apiError(t, f.put("", next, false), v1.ErrPreconditionNeeded)
	e := apiError(t, f.put("sha256-"+strings.Repeat("0", 64), next, false), v1.ErrPreconditionFailed)
	if e.CurrentETag != etag {
		t.Fatalf("current_etag %q, want %q", e.CurrentETag, etag)
	}
	if f.file() != testConfig {
		t.Fatal("rejected write changed the file")
	}
	// a quoted header and a bare one both work
	if r := f.do("PUT", "/v1/config", v1.ConfigPutRequest{Content: next}, map[string]string{"If-Match": etag}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// a second writer holding the old etag loses
	e = apiError(t, f.put(etag, next+"# more\n", false), v1.ErrPreconditionFailed)
	if e.CurrentETag == etag {
		t.Fatalf("%+v", e)
	}
	// someone else edits the file between GET and PUT
	etag = f.config().ETag
	if err := os.WriteFile(f.path, []byte(testConfig+"\n# edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	apiError(t, f.put(etag, next, false), v1.ErrPreconditionFailed)
}

func Test_ControlConfigRejectsInvalid(t *testing.T) {
	f := newFixture(t)
	etag := f.config().ETag
	for name, content := range map[string]string{
		"schema violation": strings.Replace(testConfig, "default_cwd: .", "default_cwd: .\n  exec:\n    max_concurrent: many", 1),
		"unknown key":      testConfig + "nonsense: 1\n",
		"broken yaml":      "node: [unclosed",
		"empty":            "",
		"two documents":    testConfig + "---\nnode: {}\n",
	} {
		r := f.put(etag, content, false)
		e := apiError(t, r, v1.ErrConfigInvalid)
		if len(e.Diagnostics) == 0 || e.Diagnostics[0].Message == "" {
			t.Errorf("%s: no diagnostics: %s", name, r.Body)
		}
	}
	// the schema failure names the location
	r := f.put(etag, strings.Replace(testConfig, "default_cwd: .", "default_cwd: .\n  exec:\n    max_concurrent: many", 1), false)
	var e v1.Error
	decodeAs(t, r, &e)
	found := false
	for _, d := range e.Diagnostics {
		found = found || d.Pointer == "/agent/exec/max_concurrent"
	}
	if !found {
		t.Fatalf("%s", r.Body)
	}
	// yaml errors carry a line
	var y v1.Error
	decodeAs(t, f.put(etag, "node: [unclosed", false), &y)
	if y.Diagnostics[0].Line == 0 {
		t.Fatalf("%+v", y)
	}
	if f.file() != testConfig {
		t.Fatal("an invalid write changed the file")
	}
	// body errors
	e2 := apiError(t, f.do("PUT", "/v1/config", map[string]any{"content": testConfig, "force": true}, map[string]string{"If-Match": etag}), v1.ErrInvalidArgs)
	if e2.Field != "force" {
		t.Fatalf("%+v", e2)
	}
	apiError(t, f.do("PUT", "/v1/config", map[string]any{}, map[string]string{"If-Match": etag}), v1.ErrConfigInvalid)
}

func Test_ControlConfigBodyLimit(t *testing.T) {
	f := newFixture(t)
	etag := f.config().ETag
	// 70 KiB is fine for the configuration (limit 1 MiB) ...
	if r := f.put(etag, testConfig+"# "+strings.Repeat("x", 70<<10)+"\n", true); r.Status != 200 {
		t.Fatalf("%d %.200s", r.Status, r.Body)
	}
	// ... and 1 MiB plus is not.
	apiError(t, f.put(etag, testConfig+"# "+strings.Repeat("x", v1.MaxConfigBytes)+"\n", true), v1.ErrPayloadTooLarge)
}

func Test_ControlConfigDryRunAndNoop(t *testing.T) {
	f := newFixture(t)
	etag := f.config().ETag
	renamed := strings.Replace(testConfig, "name: ctl-node", "name: renamed", 1)
	got := f.putOK(etag, renamed, true)
	if got.Applied || !got.RestartRequired || len(got.Diff) != 1 || got.Diff[0].Pointer != "/node/name" || !got.Diff[0].RestartRequired || got.ETag != etag {
		t.Fatalf("%+v", got)
	}
	if f.file() != testConfig {
		t.Fatal("dry run wrote the file")
	}
	same := f.putOK(etag, testConfig, false)
	if same.Applied || len(same.Diff) != 0 || same.ETag != etag {
		t.Fatalf("identical text should be a no-op: %+v", same)
	}
	if strings.Contains(string(f.do("PUT", "/v1/config", v1.ConfigPutRequest{Content: testConfig}, map[string]string{"If-Match": etag}).Body), "null") {
		t.Fatal("diff must be an array")
	}
	// a comment-only change is written even though nothing in the data model moved
	commented := testConfig + "# note\n"
	if c := f.putOK(etag, commented, false); !c.Applied || len(c.Diff) != 0 || c.ETag == etag {
		t.Fatalf("%+v", c)
	}
}

func Test_ControlConfigRestartRequired(t *testing.T) {
	f := newFixture(t)
	events := f.stream("")
	renamed := strings.Replace(testConfig, "name: ctl-node", "name: renamed", 1)
	got := f.putOK(f.config().ETag, renamed, false)
	if !got.Applied || !got.RestartRequired || f.file() != renamed {
		t.Fatalf("%+v", got)
	}
	// the store pins the node name until restart, and says so
	if c := f.store.Get(); c.Node.Name != "ctl-node" {
		t.Fatalf("node name changed live: %s", c.Node.Name)
	}
	if c := f.config(); !c.RestartRequired {
		t.Fatal(c)
	}
	var data v1.ConfigEventData
	payload(t, events.next(t, ofType(v1.EventConfig)), &data)
	if data.Change != "written" || data.Actor != v1.ActorControl || !data.RestartRequired || len(data.ChangedPaths) != 1 || data.ChangedPaths[0] != "/node/name" || data.ETag == nil || *data.ETag != got.ETag {
		t.Fatalf("%+v", data)
	}
	if strings.Contains(string(mustJSON(t, data)), "renamed") {
		t.Fatal("the event carries a value")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func Test_ControlConfigFileEditsAnnounced(t *testing.T) {
	f := newFixture(t)
	events := f.stream("")
	edited := strings.Replace(testConfig, "allow_dangerous: false", "allow_dangerous: true", 1)
	if err := os.WriteFile(f.path, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	var data v1.ConfigEventData
	payload(t, events.next(t, ofType(v1.EventConfig)), &data)
	if data.Change != "reloaded" || data.Actor != v1.ActorFile || len(data.ChangedPaths) != 1 || data.ChangedPaths[0] != "/agent/policy/allow_dangerous" {
		t.Fatalf("%+v", data)
	}
	// a file that does not validate is reported, and the node keeps the last good generation
	if err := os.WriteFile(f.path, []byte("node: ["), 0600); err != nil {
		t.Fatal(err)
	}
	payload(t, events.next(t, ofType(v1.EventConfig)), &data)
	if data.Change != "reload_failed" || data.Actor != v1.ActorFile || len(data.ChangedPaths) != 0 {
		t.Fatalf("%+v", data)
	}
	if !f.store.Get().Agent.Policy.AllowDangerous {
		t.Fatal("the node dropped its last good configuration")
	}
}

func Test_ControlConfigDiffSecurityHints(t *testing.T) {
	json2 := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	cases := []struct {
		name          string
		before, after string
		want          map[string]bool // pointer -> weakens_security
	}{
		{"allow_dangerous on", `{"agent":{"policy":{"allow_dangerous":false}}}`, `{"agent":{"policy":{"allow_dangerous":true}}}`, map[string]bool{"/agent/policy/allow_dangerous": true}},
		{"allow_dangerous off", `{"agent":{"policy":{"allow_dangerous":true}}}`, `{"agent":{"policy":{"allow_dangerous":false}}}`, map[string]bool{"/agent/policy/allow_dangerous": false}},
		{"cwd allow grows", `{"agent":{"cwd_policy":{"allow":["a"]}}}`, `{"agent":{"cwd_policy":{"allow":["a","b"]}}}`, map[string]bool{"/agent/cwd_policy/allow/1": true}},
		{"cwd allow shrinks", `{"agent":{"cwd_policy":{"allow":["a","b"]}}}`, `{"agent":{"cwd_policy":{"allow":["a"]}}}`, map[string]bool{"/agent/cwd_policy/allow/1": false}},
		{"cwd deny shrinks", `{"agent":{"cwd_policy":{"deny":["a","b"]}}}`, `{"agent":{"cwd_policy":{"deny":["a"]}}}`, map[string]bool{"/agent/cwd_policy/deny/1": true}},
		{"pat scope widens", `{"gateway":{"pats":{"ci":{"scopes":["a:read"]}}}}`, `{"gateway":{"pats":{"ci":{"scopes":["a:read","a:write"]}}}}`, map[string]bool{"/gateway/pats/ci/scopes/1": true}},
		{"pats emptied", `{"gateway":{"pats":{"ci":{"token_file":"/t"}}}}`, `{"gateway":{"pats":{}}}`, map[string]bool{"/gateway/pats/ci": true}},
		{"one of two pats removed", `{"gateway":{"pats":{"ci":{"token_file":"/t"},"dev":{"token_file":"/u"}}}}`, `{"gateway":{"pats":{"ci":{"token_file":"/t"}}}}`, map[string]bool{"/gateway/pats/dev": false}},
		{"ingress listen", `{"ingress":{"listen":"tunnel"}}`, `{"ingress":{"listen":"funnel"}}`, map[string]bool{"/ingress/listen": true}},
		{"unrelated", `{"audit":{"retention":"1h"}}`, `{"audit":{"retention":"2h"}}`, map[string]bool{"/audit/retention": false}},
		{"pointer escaping", `{"a/b":1}`, `{"a/b":2}`, map[string]bool{"/a~1b": false}},
	}
	for _, c := range cases {
		got := map[string]bool{}
		for _, ch := range diffConfigs(json2(c.before), json2(c.after)) {
			got[ch.Pointer] = ch.WeakensSecurity
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: %v", c.name, got)
			continue
		}
		for p, w := range c.want {
			if v, ok := got[p]; !ok || v != w {
				t.Errorf("%s: %s weakens=%v (present %v), want %v", c.name, p, v, ok, w)
			}
		}
	}
}

// Without a configuration file the config capabilities are not declared and their routes say so.
func Test_ControlConfigStaticStore(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Store = staticStore(t) })
	for _, c := range []v1.Capability{v1.CapConfigRead, v1.CapConfigWrite} {
		if f.server.hasCapability(c) {
			t.Fatalf("declared %s", c)
		}
	}
	apiError(t, f.do("GET", "/v1/config", nil, nil), v1.ErrNotImplemented)
	apiError(t, f.do("PUT", "/v1/config", v1.ConfigPutRequest{Content: "a: 1"}, map[string]string{"If-Match": `"x"`}), v1.ErrNotImplemented)
}

func Test_ApplyEditsKeepsComments(t *testing.T) {
	out, err := ApplyEdits([]byte(testConfig), []Edit{{Path: "/agent/policy/allow_dangerous", Value: true}, {Path: "/agent/exec/max_concurrent", Value: 2}, {Path: "/agent/cwd_policy/allow", Delete: true}, {Path: "/nothing/here", Delete: true}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, keep := range []string{"# 节点配置", "# 节点名", "# 危险命令开关", "allow_dangerous: true", "max_concurrent: 2"} {
		if !strings.Contains(text, keep) {
			t.Errorf("%q lost:\n%s", keep, text)
		}
	}
	if strings.Contains(text, "allow:") {
		t.Errorf("delete did not apply:\n%s", text)
	}
	for _, bad := range []Edit{{Path: "agent/exec", Value: 1}, {Path: "/node/name/deeper", Value: 1}, {Path: "/", Value: 1}} {
		if _, err = ApplyEdits([]byte(testConfig), []Edit{bad}); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// Deleting the last PAT removes the empty gateway.pats key, unless a comment hangs on it or another PAT remains.
func Test_ApplyEditsDropsEmptyPats(t *testing.T) {
	const base = testConfig + "gateway:\n  listen: 127.0.0.1:8099\n  pats:\n    ci:\n      token_file: pats/ci.token\n      scopes: [\"devices:read\"]\n"
	del := Edit{Path: "/gateway/pats/ci", Delete: true}
	out, err := ApplyEdits([]byte(base), []Edit{del})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "pats") {
		t.Fatalf("empty pats kept:\n%s", out)
	}
	if _, err = config.Parse(out); err != nil {
		t.Fatalf("the kernel cannot read its own edit: %v\n%s", err, out)
	}

	// a head comment on the key is the operator's own note: the key stays
	headed := strings.Replace(base, "  pats:\n", "  # 网关令牌，请勿删除\n  pats:\n", 1)
	if out, err = ApplyEdits([]byte(headed), []Edit{del}); err != nil {
		t.Fatal(err)
	}
	if text := string(out); !strings.Contains(text, "# 网关令牌，请勿删除") || !strings.Contains(text, "pats: {}") {
		t.Fatalf("comment or key lost:\n%s", text)
	}

	// so is a line comment on the key, even when the map is empty
	lined := strings.Replace(base, "  pats:\n", "  pats: # 之后再填\n", 1)
	if out, err = ApplyEdits([]byte(lined), []Edit{del}); err != nil {
		t.Fatal(err)
	}
	if text := string(out); !strings.Contains(text, "pats: # 之后再填") || strings.Contains(text, "ci:") {
		t.Fatalf("line comment or key lost:\n%s", text)
	}

	// another PAT keeps the map, and so does its key
	out, err = ApplyEdits([]byte(base), []Edit{{Path: "/gateway/pats/ops", Value: map[string]any{"token_file": "pats/ops.token", "scopes": []string{"devices:read"}}}, del})
	if err != nil {
		t.Fatal(err)
	}
	if text := string(out); !strings.Contains(text, "ops:") || strings.Contains(text, "ci:") {
		t.Fatalf("wrong entries:\n%s", text)
	}
}

// A JSON configuration stays JSON after a control-channel edit, so other tools can still read it.
func Test_ApplyEditsKeepsJSON(t *testing.T) {
	src := `{"node": {"name": "n1", "roles": ["agent", "gateway"]}, "mesh": {"mode": "lan", "lan": {"mdns": true}}, "gateway": {"listen": "127.0.0.1:18080"}, "agent": {"cwd_policy": {"allow": ["."]}}}`
	out, err := ApplyEdits([]byte(src), []Edit{
		{Path: "/gateway/pats/ci", Value: map[string]any{"token_file": "pats/ci.token", "scopes": []string{"devices:read"}}},
		{Path: "/gateway/pats/ci", Delete: true},
		{Path: "/agent/exec/max_concurrent", Value: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	gateway, _ := got["gateway"].(map[string]any)
	if _, kept := gateway["pats"]; kept || gateway["listen"] != "127.0.0.1:18080" {
		t.Fatalf("%v", got)
	}
	if exec, _ := got["agent"].(map[string]any)["exec"].(map[string]any); exec["max_concurrent"] != float64(2) {
		t.Fatalf("%v", got)
	}
	if _, err = config.Parse(out); err != nil {
		t.Fatalf("the kernel cannot read its own edit: %v\n%s", err, out)
	}
}
