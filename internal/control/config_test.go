package control

import (
	"os"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/audit"
)

func (f *fixture) etag() string {
	f.t.Helper()
	r := f.do("GET", "/v1/config", nil, nil)
	if r.Status != 200 || r.Header.Get("ETag") == "" {
		f.t.Fatalf("GET /v1/config: %d %s", r.Status, r.Body)
	}
	return r.Header.Get("ETag")
}
func (f *fixture) file() string {
	f.t.Helper()
	b, err := os.ReadFile(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}
func put(f *fixture, etag string, body any) *Response {
	f.t.Helper()
	headers := map[string]string{}
	if etag != "" {
		headers["If-Match"] = etag
	}
	return f.do("PUT", "/v1/config", body, headers)
}

func Test_ControlConfigGet(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do("GET", "/v1/config", nil, nil))
	if body["yaml"] != testConfig || body["path"] != f.path || body["restart_required"] != false {
		t.Fatal(body)
	}
	if cfg, _ := body["config"].(map[string]any); cfg["node"].(map[string]any)["name"] != "ctl-node" {
		t.Fatal(body["config"])
	}
}

func Test_ControlConfigSetKeepsComments(t *testing.T) {
	f := newFixture(t)
	r := put(f, f.etag(), map[string]any{"set": []Edit{{Path: "/agent/policy/allow_dangerous", Value: true}, {Path: "/agent/exec/max_concurrent", Value: 2}}})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	got := decode(t, r)
	if got["applied"] != true || got["restart_required"] != false {
		t.Fatal(got)
	}
	changes := got["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("changes: %v", changes)
	}
	first := changes[0].(map[string]any)
	if first["path"] != "/agent/exec/max_concurrent" || first["before"] != 4.0 || first["after"] != 2.0 {
		t.Fatalf("diff: %v", first)
	}
	if second := changes[1].(map[string]any); second["path"] != "/agent/policy/allow_dangerous" || second["sensitive"] != true || second["after"] != true {
		t.Fatalf("diff: %v", second)
	}
	text := f.file()
	for _, keep := range []string{"# 节点配置", "# 节点名", "# 危险命令开关", "allow_dangerous: true", "max_concurrent: 2"} {
		if !strings.Contains(text, keep) {
			t.Errorf("%q lost:\n%s", keep, text)
		}
	}
	// the node itself sees the new values at its next Get
	if c := f.store.Get(); !c.Agent.Policy.AllowDangerous || c.Agent.Exec.MaxConcurrent != 2 {
		t.Fatalf("store not reloaded: %+v", c.Agent.Policy)
	}
	if etag := f.etag(); etag != got["etag"] {
		t.Fatalf("etag %v != %v", etag, got["etag"])
	}
}

func Test_ControlConfigDelete(t *testing.T) {
	f := newFixture(t)
	r := put(f, f.etag(), map[string]any{"set": []Edit{{Path: "/agent/policy/allow_dangerous", Delete: true}, {Path: "/agent/policy/nothing/here", Delete: true}}})
	if r.Status != 200 || strings.Contains(f.file(), "allow_dangerous") {
		t.Fatalf("%d %s\n%s", r.Status, r.Body, f.file())
	}
}

func Test_ControlConfigPreconditions(t *testing.T) {
	f := newFixture(t)
	etag := f.etag()
	edit := map[string]any{"set": []Edit{{Path: "/agent/exec/max_concurrent", Value: 3}}}
	if r := put(f, "", edit); r.Status != 428 || decode(t, r)["error"] != "invalid_args" {
		t.Fatalf("missing If-Match: %d %s", r.Status, r.Body)
	}
	if r := put(f, `"`+strings.Repeat("0", 64)+`"`, edit); r.Status != 412 || decode(t, r)["error"] != "conflict" {
		t.Fatalf("stale etag: %d %s", r.Status, r.Body)
	}
	if f.file() != testConfig {
		t.Fatal("rejected write changed the file")
	}
	if r := put(f, etag, edit); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// a second writer holding the old etag loses
	if r := put(f, etag, edit); r.Status != 412 {
		t.Fatalf("lost update accepted: %d %s", r.Status, r.Body)
	}
	// someone else edits the file between GET and PUT
	etag = f.etag()
	if err := os.WriteFile(f.path, []byte(testConfig+"\n# edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := put(f, etag, edit); r.Status != 412 {
		t.Fatalf("concurrent edit accepted: %d", r.Status)
	}
}

func Test_ControlConfigRejectsInvalid(t *testing.T) {
	f := newFixture(t)
	etag := f.etag()
	for name, body := range map[string]any{
		"schema violation": map[string]any{"set": []Edit{{Path: "/agent/exec/max_concurrent", Value: "many"}}},
		"unknown key":      map[string]any{"set": []Edit{{Path: "/agent/nonsense", Value: 1}}},
		"broken yaml":      map[string]any{"yaml": "node: [unclosed"},
		"bad pointer":      map[string]any{"set": []Edit{{Path: "agent/exec", Value: 1}}},
		"through scalar":   map[string]any{"set": []Edit{{Path: "/node/name/deeper", Value: 1}}},
		"both":             map[string]any{"yaml": testConfig, "set": []Edit{{Path: "/node/name", Value: "x"}}},
		"neither":          map[string]any{},
		"unknown field":    map[string]any{"yaml": testConfig, "force": true},
	} {
		r := put(f, etag, body)
		if r.Status != 400 || decode(t, r)["error"] != "invalid_args" {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	if f.file() != testConfig {
		t.Fatal("an invalid write changed the file")
	}
}

func Test_ControlConfigDryRunAndNoop(t *testing.T) {
	f := newFixture(t)
	etag := f.etag()
	r := put(f, etag, map[string]any{"dry_run": true, "set": []Edit{{Path: "/node/name", Value: "renamed"}}})
	got := decode(t, r)
	if r.Status != 200 || got["applied"] != false || got["restart_required"] != true || len(got["changes"].([]any)) != 1 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if f.file() != testConfig {
		t.Fatal("dry run wrote the file")
	}
	got = decode(t, put(f, etag, map[string]any{"yaml": testConfig}))
	if got["applied"] != false || len(got["changes"].([]any)) != 0 {
		t.Fatalf("identical text should be a no-op: %v", got)
	}
}

func Test_ControlConfigWholeFileAndRestart(t *testing.T) {
	f := newFixture(t)
	next := strings.Replace(testConfig, "name: ctl-node", "name: renamed", 1)
	events := f.stream("")
	r := put(f, f.etag(), map[string]any{"yaml": next})
	got := decode(t, r)
	if r.Status != 200 || got["applied"] != true || got["restart_required"] != true {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if f.file() != next {
		t.Fatal("whole-file replacement not stored verbatim")
	}
	// the store pins the node name until restart, and says so
	if c := f.store.Get(); c.Node.Name != "ctl-node" {
		t.Fatalf("node name changed live: %s", c.Node.Name)
	}
	if st := decode(t, f.do("GET", "/v1/config", nil, nil)); st["restart_required"] != true {
		t.Fatal(st)
	}
	e := events.next(t, ofType("config"))
	if e.Data["source"] != "control" || e.Data["restart_required"] != true || e.Data["changes"] != 1.0 {
		t.Fatal(e)
	}
}

func Test_ControlConfigFileEditAnnounced(t *testing.T) {
	f := newFixture(t)
	events := f.stream("")
	if err := os.WriteFile(f.path, []byte(testConfig+"# touched\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if e := events.next(t, ofType("config")); e.Data["source"] != "file" {
		t.Fatal(e)
	}
}

// conformance: CON-SEC-12 changes made through the channel are audited with actor=control, metadata only
func Test_CONSEC12_ConfigChangeAuditedAsControl(t *testing.T) {
	var log *audit.Logger
	f := newFixture(t, func(o *Options) {
		var err error
		if log, err = audit.New(o.AuditDir, 0); err != nil {
			t.Fatal(err)
		}
		o.Audit = log
	})
	defer log.Close()
	r := put(f, f.etag(), map[string]any{"set": []Edit{{Path: "/agent/policy/allow_dangerous", Value: true}}})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	f.do("POST", "/v1/drain", nil, nil)
	if r := put(f, f.etag(), map[string]any{"yaml": "node: {}"}); r.Status != 400 {
		t.Fatal(r.Status)
	}
	got := decode(t, f.do("GET", "/v1/audit?event=control", nil, nil))
	records := got["records"].([]any)
	if len(records) != 3 || got["rejected"] != 0.0 {
		t.Fatalf("records: %v", got)
	}
	tools := map[string]map[string]any{}
	for _, raw := range records {
		rec := raw.(map[string]any)
		if rec["actor"] != "control" || rec["event"] != "control" {
			t.Fatalf("not attributed to control: %v", rec)
		}
		tools[rec["tool"].(string)+"/"+rec["status"].(string)] = rec
	}
	ok := tools["control.config.put/ok"]
	if ok == nil || tools["control.drain/ok"] == nil || tools["control.config.put/error"] == nil {
		t.Fatalf("records: %v", records)
	}
	keys := ok["args_digest"].(map[string]any)["keys"].([]any)
	if len(keys) != 1 || keys[0] != "/agent/policy/allow_dangerous" {
		t.Fatalf("audit keys: %v", keys)
	}
}

func Test_ControlConfigStaticStore(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Store = staticStore(t) })
	for _, r := range []*Response{f.do("GET", "/v1/config", nil, nil), put(f, `"x"`, map[string]any{"yaml": "a: 1"})} {
		if r.Status != 404 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
	}
}
