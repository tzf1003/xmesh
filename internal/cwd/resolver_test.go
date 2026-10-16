package cwd

import (
	"github.com/tzf1003/xmesh/internal/toolerr"
	"os"
	"path/filepath"
	"testing"
)

// conformance: CTR-CWD-01 CON-TOOL-02
func Test_CTRCWD01(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	r, _ := New(root, map[string]Workspace{"work": {Path: root}}, Policy{Allow: []string{root}, Deny: []string{filepath.Join(root, "secret")}})
	if p, e := r.Resolve("@work", "", Existing); e != nil || p.Path != root {
		t.Fatalf("alias: %+v %v", p, e)
	}
	if _, e := r.Resolve("new", "", Create); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Resolve("missing/new", "", Create); e == nil {
		t.Fatal("missing parent accepted")
	}
	if _, e := r.Resolve("secret", "", Create); e == nil {
		t.Fatal("deny accepted")
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, e := r.Resolve("escape/new", "", Create); e == nil || e.(*toolerr.Error).Fields["reason"] != "symlink_escape" {
		t.Fatalf("escape: %v", e)
	}
	r.Policy.Allow = nil
	if _, e := r.Resolve(root, "", Existing); e == nil {
		t.Fatal("empty allow accepted")
	}
	if _, e := r.Resolve("bad\x00path", "", Create); e == nil {
		t.Fatal("NUL accepted")
	}
}
