package control

import (
	"slices"
	"strings"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
)

// patch sends edits guarded by etag (empty means no If-Match header).
func (f *fixture) patch(etag string, dry bool, edits ...v2.ConfigEdit) *Response {
	f.t.Helper()
	headers := map[string]string{}
	if etag != "" {
		headers["If-Match"] = `"` + etag + `"`
	}
	return f.do("PATCH", "/v2/config", v2.ConfigEditRequest{Edits: edits, DryRun: dry}, headers)
}

func Test_ControlConfigPatchAppliesEditsAtomically(t *testing.T) {
	f := newFixture(t)
	etag := f.config().ETag
	r := f.patch(etag, false,
		v2.ConfigEdit{Path: "/agent/backends/docs", Value: map[string]any{"transport": "stdio", "command": []string{"node", "server.js"}, "policy": "shared"}},
		v2.ConfigEdit{Path: "/agent/disabled_tools", Value: []string{"exec.run"}},
	)
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	valid(t, "ConfigPutResponse", r.Body)
	var out v2.ConfigPutResponse
	decodeAs(t, r, &out)
	if !out.Applied || out.ETag == etag || len(out.Diff) == 0 {
		t.Fatalf("%+v", out)
	}
	cfg, err := config.Parse([]byte(f.file()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Backends["docs"].Transport != "stdio" || !slices.Equal(cfg.Agent.DisabledTools, []string{"exec.run"}) {
		t.Fatalf("edits not written: %+v %v", cfg.Agent.Backends, cfg.Agent.DisabledTools)
	}

	// Deleting the backend leaves the file as it was before the add.
	etag = f.config().ETag
	if r = f.patch(etag, false, v2.ConfigEdit{Path: "/agent/backends/docs", Delete: true}); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if cfg, _ = config.Parse([]byte(f.file())); len(cfg.Agent.Backends) != 0 {
		t.Fatal("backend not deleted")
	}
}

func Test_ControlConfigPatchRejectsWithoutWriting(t *testing.T) {
	f := newFixture(t)
	etag := f.config().ETag
	apiError(t, f.patch("", false, v2.ConfigEdit{Path: "/agent/disabled_tools", Value: []string{}}), v2.ErrPreconditionNeeded)
	e := apiError(t, f.patch("sha256-"+strings.Repeat("0", 64), false, v2.ConfigEdit{Path: "/agent/disabled_tools", Value: []string{}}), v2.ErrPreconditionFailed)
	if e.CurrentETag != etag {
		t.Fatalf("current_etag %q, want %q", e.CurrentETag, etag)
	}
	apiError(t, f.patch(etag, false), v2.ErrInvalidArgs)
	apiError(t, f.patch(etag, false, v2.ConfigEdit{Path: "agent/backends/docs", Value: "x"}), v2.ErrInvalidArgs)
	// a scalar on the way down is a shape conflict, not a validation error
	apiError(t, f.patch(etag, false, v2.ConfigEdit{Path: "/node/name/extra", Value: "x"}), v2.ErrConflict)
	// the edit applies but the result does not validate: nothing is written
	e = apiError(t, f.patch(etag, false, v2.ConfigEdit{Path: "/agent/backends/docs", Value: map[string]any{"transport": "stdio", "policy": "shared"}}), v2.ErrConfigInvalid)
	if len(e.Diagnostics) == 0 {
		t.Fatal("no diagnostics for an invalid backend")
	}
	if f.file() != testConfig {
		t.Fatal("rejected patch changed the file")
	}
}

func Test_ControlConfigPatchDryRunAndNoop(t *testing.T) {
	f := newFixture(t)
	etag := f.config().ETag
	r := f.patch(etag, true, v2.ConfigEdit{Path: "/agent/disabled_tools", Value: []string{"pty.open"}})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var out v2.ConfigPutResponse
	decodeAs(t, r, &out)
	if out.Applied || len(out.Diff) == 0 || f.file() != testConfig {
		t.Fatalf("dry run wrote: %+v", out)
	}
	r = f.patch(etag, false, v2.ConfigEdit{Path: "/agent/disabled_tools", Delete: true})
	decodeAs(t, r, &out)
	if out.Applied {
		t.Fatal("deleting an absent key should change nothing")
	}
}
