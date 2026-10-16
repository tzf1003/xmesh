package cwd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

func denied(t *testing.T, r *Resolver, raw string, mode Mode, reasons ...string) {
	t.Helper()
	_, err := r.Resolve(raw, "", mode)
	v, ok := err.(*toolerr.Error)
	if !ok || v.Code != "invalid_cwd" {
		t.Fatalf("Resolve(%q) = %v, want invalid_cwd", raw, err)
	}
	for _, reason := range reasons {
		if v.Fields["reason"] == reason {
			return
		}
	}
	t.Fatalf("Resolve(%q) reason = %v, want one of %v", raw, v.Fields["reason"], reasons)
}

// allow/deny are compared after EvalSymlinks: a string that starts with an allow
// prefix is still refused when its real path lies in deny or outside allow,
// symlinks that leave allow report symlink_escape, deny wins over allow, and an
// allow or deny rule that is itself a symlink means its real target.
// conformance: CON-TOOL-02
func Test_CONTOOL02_PolicyComparesRealPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"open", "secret", "nested/deep"} {
		if err = os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{filepath.Join(root, "open", "file.txt"), filepath.Join(root, "secret", "key.txt"), filepath.Join(outside, "file.txt")} {
		if err = os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"to-outside":     outside,                       // leaves the allow root
		"to-secret":      filepath.Join(root, "secret"), // stays in allow, lands on deny
		"to-open":        filepath.Join(root, "open"),   // harmless in-root link
		"to-secret-file": filepath.Join(root, "secret", "key.txt"),
	}
	for name, target := range links {
		if err = os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Skip("symlinks are not available:", err)
		}
	}
	r, err := New(root, nil, Policy{Allow: []string{root}, Deny: []string{filepath.Join(root, "secret")}})
	if err != nil {
		t.Fatal(err)
	}

	// The string is under allow; the real path is not.
	denied(t, r, filepath.Join(root, "to-outside"), Existing, "symlink_escape")
	denied(t, r, filepath.Join(root, "to-outside", "file.txt"), Existing, "symlink_escape")
	denied(t, r, filepath.Join(root, "to-outside", "new.txt"), Create, "symlink_escape")
	// The real path is allowed by the allow prefix but matched by deny.
	denied(t, r, filepath.Join(root, "to-secret"), Existing, "denied", "symlink_escape")
	denied(t, r, filepath.Join(root, "to-secret", "key.txt"), Existing, "denied", "symlink_escape")
	denied(t, r, filepath.Join(root, "to-secret-file"), Existing, "denied", "symlink_escape")
	// `..` is cleaned before the comparison.
	denied(t, r, root+string(filepath.Separator)+filepath.Join("open", "..", ".."), Existing, "denied")
	// Harmless links resolve to their real target.
	got, err := r.Resolve(filepath.Join(root, "to-open", "file.txt"), "", Existing)
	if err != nil || got.Path != filepath.Join(root, "open", "file.txt") {
		t.Fatalf("in-root link: %+v %v", got, err)
	}

	// deny wins when a path matches both lists, regardless of the order of the rules.
	both, err := New(root, nil, Policy{Allow: []string{filepath.Join(root, "secret"), root}, Deny: []string{filepath.Join(root, "secret")}})
	if err != nil {
		t.Fatal(err)
	}
	denied(t, both, filepath.Join(root, "secret", "key.txt"), Existing, "denied")
	denied(t, both, filepath.Join(root, "secret"), Existing, "denied")
	if _, err = both.Resolve(filepath.Join(root, "open", "file.txt"), "", Existing); err != nil {
		t.Fatal(err)
	}

	// Rules that are symlinks are compared by their real targets: allowing a
	// link allows the directory it points to, however that directory is spelled.
	byLink, err := New(root, nil, Policy{Allow: []string{filepath.Join(root, "to-open")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, spelled := range []string{filepath.Join(root, "open", "file.txt"), filepath.Join(root, "to-open", "file.txt")} {
		got, err = byLink.Resolve(spelled, "", Existing)
		if err != nil || got.Path != filepath.Join(root, "open", "file.txt") {
			t.Fatalf("Resolve(%q) = %+v, %v", spelled, got, err)
		}
	}
	denied(t, byLink, filepath.Join(root, "nested", "deep"), Existing, "denied")
	// A deny rule that is a symlink blocks its target as well.
	denyLink, err := New(root, nil, Policy{Allow: []string{root}, Deny: []string{filepath.Join(root, "to-secret")}})
	if err != nil {
		t.Fatal(err)
	}
	denied(t, denyLink, filepath.Join(root, "secret", "key.txt"), Existing, "denied")
}
