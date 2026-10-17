package cwd

import (
	"github.com/tzf1003/xmesh/internal/toolerr"
	"os"
	"path/filepath"
	"strconv"
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
	// Creating a symlink needs a privilege on Windows that a standard account may lack; the
	// rest of the test does not depend on it.
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Logf("symlinks are not available, escape check skipped: %v", err)
	} else if _, e := r.Resolve("escape/new", "", Create); e == nil || e.(*toolerr.Error).Fields["reason"] != "symlink_escape" {
		t.Fatalf("escape: %v", e)
	}
	// An empty allow list admits everything the deny list does not cover.
	r.Policy.Allow = nil
	if _, e := r.Resolve(root, "", Existing); e != nil {
		t.Fatalf("empty allow refused an ordinary directory: %v", e)
	}
	if _, e := r.Resolve("secret", "", Create); e == nil {
		t.Fatal("empty allow ignored deny")
	}
	if _, e := r.Resolve("bad\x00path", "", Create); e == nil {
		t.Fatal("NUL accepted")
	}
}

func TestDenyFileRefusesListedDirectoriesWithEmptyAllow(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret")
	if err := os.Mkdir(secret, 0o700); err != nil {
		t.Fatal(err)
	}
	denyFile := filepath.Join(t.TempDir(), "cwd-deny.yaml")
	if err := os.WriteFile(denyFile, []byte("# 禁止目录\n- "+strconv.Quote(secret)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New(root, nil, Policy{DenyFile: denyFile})
	if err != nil {
		t.Fatal(err)
	}
	if _, e := r.Resolve(root, "", Existing); e != nil {
		t.Fatalf("empty allow refused an ordinary directory: %v", e)
	}
	if _, e := r.Resolve(secret, "", Existing); e == nil {
		t.Fatal("deny file entry accepted")
	}
	if _, err := New(root, nil, Policy{DenyFile: filepath.Join(root, "missing.yaml")}); err == nil {
		t.Fatal("a missing deny file must not be treated as an empty list")
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("key: value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root, nil, Policy{DenyFile: bad}); err == nil {
		t.Fatal("a deny file that is not a list was accepted")
	}
}
