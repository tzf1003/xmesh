package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/ci"
	"github.com/tzf1003/xmesh/internal/config"
)

const ciRegistrationToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// ciFixture serves the CI routes over a real supervisor. No runner package is available, so the
// instances stay broken; the routes under test do not depend on them running.
func ciFixture(t *testing.T, public map[string]bool) *fixture {
	t.Helper()
	var f *fixture
	f = newFixture(t, func(o *Options) {
		state := filepath.Join(filepath.Dir(o.Store.Path()), "ci-state")
		_ = os.MkdirAll(state, 0o700)
		m := ci.New(ci.Options{
			StateDir:  state,
			Node:      "ctl-node",
			Config:    func() config.CI { return o.Store.Get().CI },
			RunningIn: func(string) bool { return false },
		})
		t.Cleanup(func() { m.Shutdown(0) })
		o.Store.Subscribe(func(next *config.Config) { m.Apply(next.CI) })
		o.CI = m
		o.CIVisibility = func(_ context.Context, _, repo string) (bool, error) { return public[repo], nil }
	})
	return f
}

func (f *fixture) configText() string {
	f.t.Helper()
	raw, err := os.ReadFile(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

func runnerDir(t *testing.T, url string, ephemeral bool) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"agentName":"old-runner","gitHubUrl":"` + url + `","workFolder":"_work"`
	if ephemeral {
		body += `,"ephemeral":true`
	}
	if err := os.WriteFile(filepath.Join(dir, ".runner"), []byte(body+"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// conformance: CTR-CTL-15 a kernel without the supervisor does not declare the capability
func Test_CTRCTL15_NoSupervisorIsNotImplemented(t *testing.T) {
	f := newFixture(t)
	apiError(t, f.do("GET", "/v1/ci", nil, nil), v1.ErrNotImplemented)
	apiError(t, f.do("POST", "/v1/ci/bindings", v1.CiBindingCreate{Target: "o/r", RegistrationToken: ciRegistrationToken}, nil), v1.ErrNotImplemented)
}

// conformance: CTR-CTL-15 binding with a one-time registration token, then listing, patching, restarting and unbinding
func Test_CTRCTL15_BindingLifecycle(t *testing.T) {
	f := ciFixture(t, nil)
	create := v1.CiBindingCreate{Target: "tzf1003/xmesh", RegistrationToken: ciRegistrationToken, Concurrency: 2, Labels: []string{"gpu"}}
	r := f.do("POST", "/v1/ci/bindings", create, nil)
	if r.Status != 201 {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	valid(t, "CiBinding", r.Body)
	var created v1.CiBinding
	decodeAs(t, r, &created)
	if created.ID != "tzf1003-xmesh" || created.Source != v1.CiSourceToken || created.Concurrency != 2 {
		t.Fatalf("created = %+v", created)
	}
	// The token is held in memory only: not in the file, not in any answer.
	if strings.Contains(f.configText(), ciRegistrationToken) || strings.Contains(string(r.Body), ciRegistrationToken) {
		t.Fatal("the registration token was written or echoed")
	}
	if text := f.configText(); !strings.Contains(text, "tzf1003/xmesh") || !strings.Contains(text, "source: token") {
		t.Fatalf("binding missing from the file:\n%s", text)
	}

	overview := f.ok("GET", "/v1/ci", nil, "CiOverview")
	if strings.Contains(string(overview.Body), ciRegistrationToken) {
		t.Fatal("overview echoes the token")
	}
	var ov v1.CiOverview
	decodeAs(t, overview, &ov)
	if len(ov.Bindings) != 1 || len(ov.Accounts) != 0 || !ov.PreventSleepWhileBusy {
		t.Fatalf("overview = %+v", ov)
	}
	status := f.ok("GET", "/v1/status", nil, "StatusResponse")
	var st v1.StatusResponse
	decodeAs(t, status, &st)
	if st.CI == nil || len(st.CI.Bindings) != 1 {
		t.Fatalf("status.ci = %+v", st.CI)
	}
	found := false
	for _, c := range st.Control.Capabilities {
		found = found || c == v1.CapCI
	}
	if !found {
		t.Fatal("ci capability not declared")
	}

	apiError(t, f.do("POST", "/v1/ci/bindings", v1.CiBindingCreate{Target: "TZF1003/xmesh", RegistrationToken: ciRegistrationToken}, nil), v1.ErrConflict)

	one, paused := 1, true
	labels := []string{}
	r = f.do("PATCH", "/v1/ci/bindings/tzf1003-xmesh", v1.CiBindingPatch{Concurrency: &one, Paused: &paused, Labels: &labels}, nil)
	if r.Status != 200 {
		t.Fatalf("patch: %d %s", r.Status, r.Body)
	}
	valid(t, "CiBinding", r.Body)
	got := f.store.Get().CI.Bindings
	if len(got) != 1 || got[0].Concurrency != 1 || !got[0].Paused || len(got[0].Labels) != 0 {
		t.Fatalf("config after patch = %+v", got)
	}
	bad := 99
	apiError(t, f.do("PATCH", "/v1/ci/bindings/tzf1003-xmesh", v1.CiBindingPatch{Concurrency: &bad}, nil), v1.ErrInvalidArgs)
	apiError(t, f.do("PATCH", "/v1/ci/bindings/nope", v1.CiBindingPatch{Concurrency: &one}, nil), v1.ErrNotFound)

	if r = f.do("POST", "/v1/ci/bindings/tzf1003-xmesh/restart", nil, nil); r.Status != 204 {
		t.Fatalf("restart: %d %s", r.Status, r.Body)
	}
	apiError(t, f.do("POST", "/v1/ci/bindings/nope/restart", nil, nil), v1.ErrNotFound)

	// Without an account, repairing needs a fresh registration token.
	e := apiError(t, f.do("POST", "/v1/ci/bindings/tzf1003-xmesh/repair", v1.CiBindingRepair{}, nil), v1.ErrInvalidArgs)
	if e.Field != "registration_token" {
		t.Fatalf("field = %q", e.Field)
	}
	r = f.do("POST", "/v1/ci/bindings/tzf1003-xmesh/repair", v1.CiBindingRepair{RegistrationToken: ciRegistrationToken}, nil)
	if r.Status != 200 {
		t.Fatalf("repair: %d %s", r.Status, r.Body)
	}
	valid(t, "CiBinding", r.Body)

	r = f.do("DELETE", "/v1/ci/bindings/tzf1003-xmesh?force=true", nil, nil)
	if r.Status != 200 {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	valid(t, "CiBinding", r.Body)
	var gone v1.CiBinding
	decodeAs(t, r, &gone)
	if !gone.Removing {
		t.Fatalf("removing = false: %s", r.Body)
	}
	if len(f.store.Get().CI.Bindings) != 0 {
		t.Fatalf("binding still configured: %+v", f.store.Get().CI.Bindings)
	}
	apiError(t, f.do("DELETE", "/v1/ci/bindings/tzf1003-xmesh", nil, nil), v1.ErrNotFound)
}

// conformance: CTR-CTL-15 a public repository needs a second confirmation
func Test_CTRCTL15_PublicRepoNeedsConfirm(t *testing.T) {
	f := ciFixture(t, map[string]bool{"o/open": true})
	create := v1.CiBindingCreate{Target: "o/open", RegistrationToken: ciRegistrationToken}
	e := apiError(t, f.do("POST", "/v1/ci/bindings", create, nil), v1.ErrInvalidArgs)
	if e.Reason != "public_repo_requires_confirm" || e.Field != "confirm_public" {
		t.Fatalf("error = %+v", e)
	}
	if len(f.store.Get().CI.Bindings) != 0 {
		t.Fatal("binding written without confirmation")
	}
	create.ConfirmPublic = true
	r := f.do("POST", "/v1/ci/bindings", create, nil)
	if r.Status != 201 {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	var b v1.CiBinding
	decodeAs(t, r, &b)
	if b.Public == nil || !*b.Public {
		t.Fatalf("public = %v", b.Public)
	}
}

// conformance: CTR-CTL-15 exactly one of the three binding ways, and field checks
func Test_CTRCTL15_BindingCreateArguments(t *testing.T) {
	f := ciFixture(t, nil)
	cases := map[string]v1.CiBindingCreate{
		"nothing":      {},
		"target only":  {Target: "o/r"},
		"two ways":     {Target: "o/r", Account: "a", RegistrationToken: ciRegistrationToken},
		"import+token": {ImportDir: "/x", RegistrationToken: ciRegistrationToken},
		"bad target":   {Target: "not a repo", RegistrationToken: ciRegistrationToken},
		"bad id":       {ID: "Bad_ID", Target: "o/r", RegistrationToken: ciRegistrationToken},
		"bad conc":     {Target: "o/r", RegistrationToken: ciRegistrationToken, Concurrency: 40},
		"bad label":    {Target: "o/r", RegistrationToken: ciRegistrationToken, Labels: []string{"has space"}},
	}
	for name, body := range cases {
		if r := f.do("POST", "/v1/ci/bindings", body, nil); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	apiError(t, f.do("POST", "/v1/ci/bindings", v1.CiBindingCreate{Target: "o/r", Account: "nobody"}, nil), v1.ErrNotFound)
	if n := len(f.store.Get().CI.Bindings); n != 0 {
		t.Fatalf("%d bindings written by refused requests", n)
	}
}

// conformance: CTR-CTL-15 taking over an existing runner directory
func Test_CTRCTL15_ImportRunner(t *testing.T) {
	f := ciFixture(t, nil)
	dir := runnerDir(t, "https://github.com/tzf1003/legacy", false)

	r := f.ok("POST", "/v1/ci/import/inspect", v1.CiImportInspectRequest{Dir: dir}, "CiImportInspectResponse")
	var in v1.CiImportInspectResponse
	decodeAs(t, r, &in)
	if in.Target != "tzf1003/legacy" || in.RunnerName != "old-runner" || in.ServiceInstalled || in.Running {
		t.Fatalf("inspect = %+v", in)
	}
	apiError(t, f.do("POST", "/v1/ci/import/inspect", v1.CiImportInspectRequest{Dir: t.TempDir()}, nil), v1.ErrNotFound)
	apiError(t, f.do("POST", "/v1/ci/import/inspect", v1.CiImportInspectRequest{Dir: "relative"}, nil), v1.ErrInvalidArgs)

	org := runnerDir(t, "https://github.com/some-org", false)
	e := apiError(t, f.do("POST", "/v1/ci/bindings", v1.CiBindingCreate{ImportDir: org}, nil), v1.ErrInvalidArgs)
	if e.Reason != "not_a_runner" {
		t.Fatalf("org runner: %+v", e)
	}
	e = apiError(t, f.do("POST", "/v1/ci/bindings", v1.CiBindingCreate{ImportDir: runnerDir(t, "https://github.com/o/r", true)}, nil), v1.ErrInvalidArgs)
	if e.Reason != "ephemeral_runner" {
		t.Fatalf("ephemeral: %+v", e)
	}
	service := runnerDir(t, "https://github.com/o/r", false)
	if err := os.WriteFile(filepath.Join(service, ".service"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e = apiError(t, f.do("POST", "/v1/ci/bindings", v1.CiBindingCreate{ImportDir: service}, nil), v1.ErrInvalidArgs)
	if e.Reason != "service_installed" {
		t.Fatalf("service: %+v", e)
	}

	r = f.do("POST", "/v1/ci/bindings", v1.CiBindingCreate{ImportDir: dir}, nil)
	if r.Status != 201 {
		t.Fatalf("import: %d %s", r.Status, r.Body)
	}
	valid(t, "CiBinding", r.Body)
	got := f.store.Get().CI.Bindings
	if len(got) != 1 || got[0].Source != "import" || got[0].Target != "tzf1003/legacy" || got[0].Dir != filepath.Clean(dir) {
		t.Fatalf("config = %+v", got)
	}
	// An imported runner is a single instance.
	two := 2
	e = apiError(t, f.do("PATCH", "/v1/ci/bindings/"+got[0].ID, v1.CiBindingPatch{Concurrency: &two}, nil), v1.ErrInvalidArgs)
	if e.Field != "concurrency" {
		t.Fatalf("patch: %+v", e)
	}
}

// conformance: CTR-CTL-15 the account routes answer unavailable until accounts exist
func Test_CTRCTL15_AccountRoutesUnavailable(t *testing.T) {
	f := ciFixture(t, nil)
	for _, c := range []struct{ method, path string }{
		{"POST", "/v1/ci/accounts"}, {"DELETE", "/v1/ci/accounts/a"}, {"GET", "/v1/ci/accounts/a/repos"},
	} {
		e := apiError(t, f.do(c.method, c.path, map[string]string{"token": "x"}, nil), v1.ErrUnavailable)
		if e.Reason != "accounts_unavailable" {
			t.Errorf("%s %s: %+v", c.method, c.path, e)
		}
	}
}
