package adengine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestEmbeddedManifestPinsValidatedRelease(t *testing.T) {
	m, err := Embedded()
	if err != nil {
		t.Fatalf("embedded manifest: %v", err)
	}
	if m.Version != "1.0.2" || len(m.Platforms) != 6 {
		t.Fatalf("pin = %s with %d platforms", m.Version, len(m.Platforms))
	}
	if !m.Validated("1.0.2", "darwin-arm64") || m.Validated("1.0.2", "linux-x64") || m.Validated("1.1.0", "darwin-arm64") {
		t.Fatalf("validation gate does not match the recorded acceptance")
	}
	if got := m.UpstreamLatest(); got != "1.1.0" {
		t.Fatalf("upstream latest = %q", got)
	}
	if got := m.Platforms["darwin-arm64"].Binary.SHA256; got != "bf0a3c9256512f2f57288d37ccd17d7f03665fccc53c2f74011a49b98e61ba59" {
		t.Fatalf("darwin-arm64 binary hash = %s", got)
	}
}

func TestInstallNamesDropPlatformSuffix(t *testing.T) {
	cases := map[[2]string]string{
		{"bin/agent-desktop-darwin-arm64", "darwin-arm64"}:                 "agent-desktop",
		{"bin/agent-desktop-macos-helper-darwin-arm64", "darwin-arm64"}:    "agent-desktop-macos-helper",
		{"bin/agent-desktop-linux-clipboard-owner-linux-x64", "linux-x64"}: "agent-desktop-linux-clipboard-owner",
		{"bin/agent-desktop-win32-x64.exe", "win32-x64"}:                   "agent-desktop.exe",
	}
	for in, want := range cases {
		if got := InstallName(in[0], in[1]); got != want {
			t.Errorf("InstallName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	if got := PlatformKey("windows", "amd64"); got != "win32-x64" {
		t.Errorf("PlatformKey windows/amd64 = %q", got)
	}
}

func TestParseVersionIsStrict(t *testing.T) {
	for _, bad := range []string{"1.0", "01.0.0", "1.0.0-beta", "+1.0.0", "1.0.x", ""} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
	a, _ := ParseVersion("1.0.10")
	b, _ := ParseVersion("1.0.2")
	if a.Compare(b) <= 0 {
		t.Errorf("1.0.10 must sort after 1.0.2")
	}
}

func TestParseRejectsUnsafeManifests(t *testing.T) {
	base := map[string]any{}
	if err := json.Unmarshal(embeddedManifest, &base); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(m map[string]any){
		"schema":       func(m map[string]any) { m["schema_version"] = 2 },
		"http tarball": func(m map[string]any) { m["tarball"].(map[string]any)["url"] = "http://example.invalid/a.tgz" },
		"unknown plat": func(m map[string]any) {
			m["platforms"].(map[string]any)["plan9-x64"] = m["platforms"].(map[string]any)["linux-x64"]
		},
		"path escape": func(m map[string]any) {
			bin := m["platforms"].(map[string]any)["linux-x64"].(map[string]any)["binary"].(map[string]any)
			bin["member"] = "../x"
		},
		"short sha": func(m map[string]any) {
			bin := m["platforms"].(map[string]any)["linux-x64"].(map[string]any)["binary"].(map[string]any)
			bin["sha256"] = "abcd"
		},
		"bad integrity": func(m map[string]any) { m["tarball"].(map[string]any)["integrity"] = "sha1-abc" },
		"bad validated": func(m map[string]any) { m["validation"].(map[string]any)["validated_versions"] = []string{"1.0"} },
	}
	for name, mutate := range mutations {
		raw, _ := json.Marshal(base)
		var copyOf map[string]any
		_ = json.Unmarshal(raw, &copyOf)
		mutate(copyOf)
		data, _ := json.Marshal(copyOf)
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: manifest accepted", name)
		}
	}
}

// fixture serves a fake agent-desktop tarball and returns a manifest that pins it.
type fixture struct {
	srv      *httptest.Server
	hits     atomic.Int32
	m        *Manifest
	key      string
	stateDir string
	bin      []byte
	tgz      []byte
}

const testKey = "linux-x64"

func newFixture(t *testing.T, mutate func(files map[string][]byte)) *fixture {
	t.Helper()
	bin := []byte("fake agent-desktop 1.0.2")
	helper := []byte("fake clipboard owner")
	files := map[string][]byte{
		"package/bin/agent-desktop-linux-x64":                       bin,
		"package/bin/agent-desktop-linux-clipboard-owner-linux-x64": helper,
		"package/bin/agent-desktop-win32-x64.exe":                   []byte("windows"),
		"package/README.md":                                         []byte("docs"),
	}
	if mutate != nil {
		mutate(files)
	}
	f := &fixture{key: testKey, stateDir: t.TempDir(), bin: bin}
	f.tgz = buildTarball(t, files)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		_, _ = w.Write(f.tgz)
	}))
	t.Cleanup(f.srv.Close)
	f.m = &Manifest{
		SchemaVersion: 1,
		Package:       "agent-desktop",
		Version:       "1.0.2",
		Tarball:       Tarball{URL: f.srv.URL + "/agent-desktop.tgz", Integrity: sriSHA512(f.tgz)},
		Platforms: map[string]Platform{testKey: {
			Binary:  File{Member: "bin/agent-desktop-linux-x64", SHA256: sha256hex(bin)},
			Helpers: []File{{Member: "bin/agent-desktop-linux-clipboard-owner-linux-x64", SHA256: sha256hex(helper)}},
		}},
		Validation: Validation{ValidatedVersions: []string{"1.0.2"}, ValidatedPlatforms: []string{testKey}},
	}
	return f
}

func buildTarball(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sriSHA512(b []byte) string {
	h := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(h[:])
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeExec answers every agent-desktop command with a success envelope unless
// the command is listed in fail.
type fakeExec struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool
}

func (f *fakeExec) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args[0])
	failed := f.fail[args[0]]
	f.mu.Unlock()
	if failed {
		return []byte(`{"ok":false,"error":{"code":"PERM_DENIED"}}`), nil
	}
	return []byte(`{"ok":true,"data":{}}`), nil
}

func TestStageInstallsVerifiedFilesAndReusesThem(t *testing.T) {
	f := newFixture(t, nil)
	s := NewStore(f.stateDir)
	dir, err := Stager{Store: s}.Stage(context.Background(), f.m, f.key)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if dir != s.VersionDir("1.0.2") {
		t.Fatalf("dir = %s", dir)
	}
	id := IdentifyManaged(s, "1.0.2", f.key)
	if id.Problem != "" || id.Version != "1.0.2" || !id.Found {
		t.Fatalf("identity after stage = %+v", id)
	}
	if _, err := (Stager{Store: s}).Stage(context.Background(), f.m, f.key); err != nil {
		t.Fatalf("second stage: %v", err)
	}
	if got := f.hits.Load(); got != 1 {
		t.Fatalf("second stage downloaded again: %d requests", got)
	}
}

func TestStageRejectsTarballIntegrityMismatch(t *testing.T) {
	f := newFixture(t, nil)
	f.m.Tarball.Integrity = sriSHA512([]byte("other archive"))
	s := NewStore(f.stateDir)
	if _, err := (Stager{Store: s}).Stage(context.Background(), f.m, f.key); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("expected integrity failure, got %v", err)
	}
	if _, err := os.Stat(s.VersionDir("1.0.2")); !os.IsNotExist(err) {
		t.Fatalf("version directory created despite integrity failure: %v", err)
	}
}

func TestStageRejectsMemberHashMismatch(t *testing.T) {
	f := newFixture(t, nil)
	p := f.m.Platforms[testKey]
	p.Binary.SHA256 = sha256hex([]byte("tampered"))
	f.m.Platforms[testKey] = p
	s := NewStore(f.stateDir)
	if _, err := (Stager{Store: s}).Stage(context.Background(), f.m, f.key); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("expected sha256 failure, got %v", err)
	}
	if _, err := os.Stat(s.VersionDir("1.0.2")); !os.IsNotExist(err) {
		t.Fatalf("version directory created despite hash failure")
	}
}

func TestStageRejectsArchiveMissingHelper(t *testing.T) {
	f := newFixture(t, func(files map[string][]byte) {
		delete(files, "package/bin/agent-desktop-linux-clipboard-owner-linux-x64")
	})
	// The manifest still names the helper, so the archive is incomplete.
	f.tgz = buildTarball(t, map[string][]byte{"package/bin/agent-desktop-linux-x64": f.bin})
	f.m.Tarball.Integrity = sriSHA512(f.tgz)
	s := NewStore(f.stateDir)
	if _, err := (Stager{Store: s}).Stage(context.Background(), f.m, f.key); err == nil || !strings.Contains(err.Error(), "归档缺少") {
		t.Fatalf("expected missing member failure, got %v", err)
	}
}

func TestIdentifyBundledChecksBinaryAndHelpers(t *testing.T) {
	f := newFixture(t, nil)
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent-desktop")
	helper := filepath.Join(dir, "agent-desktop-linux-clipboard-owner")
	if err := os.WriteFile(exe, f.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("fake clipboard owner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if id := IdentifyBundled(f.m, testKey, exe); id.Problem != "" || id.Version != "1.0.2" {
		t.Fatalf("matching bundle = %+v", id)
	}
	if id := IdentifyBundled(f.m, testKey, "relative/agent-desktop"); id.Found || id.Problem != ProblemNotInstalled {
		t.Fatalf("relative path = %+v", id)
	}
	if err := os.WriteFile(exe, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if id := IdentifyBundled(f.m, testKey, exe); id.Problem != ProblemBinaryHash || id.Version != "" {
		t.Fatalf("tampered binary = %+v", id)
	}
	if err := os.WriteFile(exe, f.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(helper); err != nil {
		t.Fatal(err)
	}
	if id := IdentifyBundled(f.m, testKey, exe); id.Problem != ProblemHelperMissing {
		t.Fatalf("missing helper = %+v", id)
	}
}

func TestInstallSwitchesOnlyAfterHealth(t *testing.T) {
	f := newFixture(t, nil)
	u := NewUpdater(f.m, f.key, f.stateDir, (&fakeExec{fail: map[string]bool{"list-displays": true}}).run)
	if _, err := u.Install(context.Background()); err == nil {
		t.Fatal("install succeeded although health failed")
	}
	if active, _ := u.Store.Active(); active != "" {
		t.Fatalf("active switched despite failed health: %q", active)
	}

	ok := &fakeExec{}
	u.Exec = ok.run
	ch, err := u.Install(context.Background())
	if err != nil || !ch.Changed {
		t.Fatalf("install = %+v, %v", ch, err)
	}
	if active, _ := u.Store.Active(); active != "1.0.2" {
		t.Fatalf("active = %q", active)
	}
	if got := strings.Join(ok.calls, ","); got != "version,permissions,list-displays" {
		t.Fatalf("health commands = %s", got)
	}
}

func TestInstallRefusesUnvalidatedRelease(t *testing.T) {
	f := newFixture(t, nil)
	f.m.Validation.ValidatedPlatforms = []string{"darwin-arm64"}
	u := NewUpdater(f.m, f.key, f.stateDir, (&fakeExec{}).run)
	if _, err := u.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "验收") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if got := f.hits.Load(); got != 0 {
		t.Fatalf("refused install still downloaded: %d", got)
	}
}

func TestApplyRollsBackWhenRestartedHostFailsVerify(t *testing.T) {
	f := newFixture(t, nil)
	u := NewUpdater(f.m, f.key, f.stateDir, (&fakeExec{}).run)
	var restarts int
	h := Hooks{
		Restart: func(context.Context) error { restarts++; return nil },
		Verify:  func(context.Context) error { return errors.New("engine still native") },
	}
	ch, err := u.Apply(context.Background(), h)
	if err == nil || !ch.RolledBack {
		t.Fatalf("apply = %+v, %v; want rollback", ch, err)
	}
	if active, _ := u.Store.Active(); active != "" {
		t.Fatalf("active after rollback = %q, want bundled", active)
	}
	if restarts != 2 {
		t.Fatalf("restarts = %d, want 2 (switch and rollback)", restarts)
	}
}

func TestApplyWithoutServiceLeavesSwitchForRestart(t *testing.T) {
	f := newFixture(t, nil)
	u := NewUpdater(f.m, f.key, f.stateDir, (&fakeExec{}).run)
	ch, err := u.Apply(context.Background(), Hooks{Restart: func(context.Context) error { return ErrRestartUnavailable }})
	if err != nil || !ch.RestartRequired || ch.RolledBack {
		t.Fatalf("apply = %+v, %v", ch, err)
	}
	if active, _ := u.Store.Active(); active != "1.0.2" {
		t.Fatalf("active = %q", active)
	}
}

func TestApplyClearsFailureStreakAfterVerifiedUpdate(t *testing.T) {
	f := newFixture(t, nil)
	u := NewUpdater(f.m, f.key, f.stateDir, (&fakeExec{}).run)
	hooks := Hooks{
		Restart: func(context.Context) error { return nil },
		Verify:  func(context.Context) error { return nil },
	}
	if err := u.Store.SavePolicy(Policy{ConsecutiveFailures: 2, LastError: "boom"}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Apply(context.Background(), hooks); err != nil {
		t.Fatalf("apply: %v", err)
	}
	policy, err := u.Store.LoadPolicy()
	if err != nil || policy.Suspended || policy.ConsecutiveFailures != 0 || policy.LastError != "" {
		t.Fatalf("policy after verified update = %+v, %v", policy, err)
	}

	// Re-running a deliberate update that finds the pinned release already active
	// also resumes automatic updates.
	if err := u.Store.SavePolicy(Policy{ConsecutiveFailures: 2, LastError: "boom"}); err != nil {
		t.Fatal(err)
	}
	if ch, err := u.Apply(context.Background(), hooks); err != nil || ch.Changed {
		t.Fatalf("apply on current release = %+v, %v", ch, err)
	}
	policy, err = u.Store.LoadPolicy()
	if err != nil || policy.Suspended || policy.ConsecutiveFailures != 0 {
		t.Fatalf("policy after current check = %+v, %v", policy, err)
	}
}

func TestAutoApplyStagesPendingAndRollsBackFailedStart(t *testing.T) {
	f := newFixture(t, nil)
	st := Status{State: StateUpdateAvailable, Installed: "1.0.1"}
	u := NewUpdater(f.m, f.key, f.stateDir, (&fakeExec{}).run)

	if _, reason, _ := u.AutoApply(context.Background(), ModeCheck, st); reason != "mode_check" {
		t.Fatalf("check mode reason = %q", reason)
	}
	if _, reason, _ := u.AutoApply(context.Background(), ModeApply, Status{State: StateCurrent}); reason != "state_current" {
		t.Fatalf("current state reason = %q", reason)
	}
	if _, reason, _ := u.AutoApply(context.Background(), ModeApply, Status{State: StateUpdateAvailable, Installed: "1.1.0"}); reason != "not_patch_release" {
		t.Fatalf("minor line reason = %q", reason)
	}

	ch, reason, err := u.AutoApply(context.Background(), ModeApply, st)
	if err != nil || reason != "" || !ch.Pending {
		t.Fatalf("auto apply = %+v, %q, %v", ch, reason, err)
	}
	if pending, _ := u.Store.Pending(); pending != "1.0.2" {
		t.Fatalf("pending = %q", pending)
	}

	// The next Host start finds the switch failing and rolls it back.
	bad := &fakeExec{fail: map[string]bool{"permissions": true}}
	u.Exec = bad.run
	res, err := u.CheckPending(context.Background())
	if err != nil || !res.RolledBack {
		t.Fatalf("check pending = %+v, %v", res, err)
	}
	if active, _ := u.Store.Active(); active != "" {
		t.Fatalf("active after rollback = %q", active)
	}
	if pending, _ := u.Store.Pending(); pending != "" {
		t.Fatalf("pending not cleared: %q", pending)
	}
	policy, _ := u.Store.LoadPolicy()
	if policy.ConsecutiveFailures != 1 || len(policy.FailedVersions) != 1 {
		t.Fatalf("policy = %+v", policy)
	}
	if _, reason, _ := u.AutoApply(context.Background(), ModeApply, st); reason != "previously_failed" {
		t.Fatalf("retry reason = %q", reason)
	}
}

func TestAutoApplyCommitsHealthyStart(t *testing.T) {
	f := newFixture(t, nil)
	u := NewUpdater(f.m, f.key, f.stateDir, (&fakeExec{}).run)
	if _, _, err := u.AutoApply(context.Background(), ModeApply, Status{State: StateUpdateAvailable, Installed: "1.0.1"}); err != nil {
		t.Fatal(err)
	}
	res, err := u.CheckPending(context.Background())
	if err != nil || res.RolledBack || res.Reason != "committed" {
		t.Fatalf("check pending = %+v, %v", res, err)
	}
	if active, _ := u.Store.Active(); active != "1.0.2" {
		t.Fatalf("active after commit = %q", active)
	}
}

func TestAutoApplySuspendsAfterTwoFailures(t *testing.T) {
	f := newFixture(t, nil)
	u := NewUpdater(f.m, f.key, f.stateDir, (&fakeExec{fail: map[string]bool{"version": true}}).run)
	if err := u.Store.SavePolicy(Policy{ConsecutiveFailures: 1}); err != nil {
		t.Fatal(err)
	}
	st := Status{State: StateUpdateAvailable, Installed: "1.0.1"}
	if _, _, err := u.AutoApply(context.Background(), ModeApply, st); err == nil {
		t.Fatal("expected the health failure to surface")
	}
	policy, _ := u.Store.LoadPolicy()
	if !policy.Suspended || policy.ConsecutiveFailures != 2 {
		t.Fatalf("policy after second failure = %+v", policy)
	}
	if _, reason, _ := u.AutoApply(context.Background(), ModeApply, st); reason != "suspended" {
		t.Fatalf("suspended reason = %q", reason)
	}
}

func TestAutoApplySkipsMacOS(t *testing.T) {
	f := newFixture(t, nil)
	f.m.Validation.ValidatedPlatforms = []string{"darwin-arm64"}
	u := NewUpdater(f.m, "darwin-arm64", f.stateDir, (&fakeExec{}).run)
	if _, reason, _ := u.AutoApply(context.Background(), ModeApply, Status{State: StateUpdateAvailable, Installed: "1.0.1"}); reason != "macos_requires_confirmation" {
		t.Fatalf("macOS reason = %q", reason)
	}
}

func TestEvaluateStates(t *testing.T) {
	m := &Manifest{
		Version:    "1.0.2",
		Platforms:  map[string]Platform{testKey: {}, "darwin-arm64": {}},
		Validation: Validation{ValidatedVersions: []string{"1.0.1", "1.0.2"}, ValidatedPlatforms: []string{testKey}},
		Upstream:   &Upstream{Latest: "1.1.0"},
	}
	bundled := func(v string) Identity { return Identity{Found: true, Source: SourceBundled, Version: v} }
	cases := []struct {
		name       string
		key        string
		configured string
		id         Identity
		state      string
		effective  string
		reason     string
	}{
		{"native configured", testKey, EngineNative, bundled("1.0.2"), StateNative, EngineNative, ""},
		{"nothing installed", testKey, EngineAgentDesktop, Identity{Source: SourceManaged, Problem: ProblemNotInstalled}, StateNotInstalled, EngineNative, ProblemNotInstalled},
		{"hash mismatch", testKey, EngineAgentDesktop, Identity{Found: true, Source: SourceBundled, Problem: ProblemBinaryHash}, StateIntegrityFailed, EngineNative, ProblemBinaryHash},
		{"version not accepted", testKey, EngineAgentDesktop, bundled("1.0.0"), StateVersionUnsupported, EngineNative, "version_not_validated"},
		{"platform not accepted", "darwin-arm64", EngineAgentDesktop, bundled("1.0.2"), StateVersionUnsupported, EngineNative, "platform_not_validated"},
		{"older validated release", testKey, EngineAgentDesktop, bundled("1.0.1"), StateUpdateAvailable, EngineAgentDesktop, "pinned_newer_validated"},
		{"pinned, upstream newer", testKey, EngineAgentDesktop, bundled("1.0.2"), StateUpstreamUnvalidated, EngineAgentDesktop, "upstream_not_validated"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := Evaluate(m, c.key, c.configured, c.id)
			if st.State != c.state || st.Effective != c.effective || st.Reason != c.reason {
				t.Fatalf("evaluate = state %s effective %s reason %q; want %s %s %q", st.State, st.Effective, st.Reason, c.state, c.effective, c.reason)
			}
		})
	}
}

func TestEvaluateReportsUnvalidatedPin(t *testing.T) {
	m := &Manifest{
		Version:    "1.1.0",
		Platforms:  map[string]Platform{testKey: {}},
		Validation: Validation{ValidatedVersions: []string{"1.0.2"}},
	}
	st := Evaluate(m, testKey, EngineAgentDesktop, Identity{Found: true, Source: SourceBundled, Version: "1.0.2"})
	if st.State != StateUpstreamUnvalidated || st.Reason != "pinned_not_validated" || st.Effective != EngineAgentDesktop {
		t.Fatalf("unvalidated pin = %+v", st)
	}
}

func TestHealthRequiresOKEnvelopes(t *testing.T) {
	if err := checkEnvelope([]byte(`{"ok":true,"data":{}}`)); err != nil {
		t.Fatalf("ok envelope rejected: %v", err)
	}
	if err := checkEnvelope([]byte(`{"ok":false,"error":{"code":"TIMEOUT"}}`)); err == nil {
		t.Fatal("failed envelope accepted")
	}
	if err := checkEnvelope([]byte(`not json`)); err == nil {
		t.Fatal("non-JSON accepted")
	}
}
