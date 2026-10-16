package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pathWorld is a directory layout with every spelling of the same place:
//
//	root/real/file.txt        the real directory and file
//	root/link  -> real        a symlink inside the allow root
//	@ws                       an alias for root/real
//	home/file.txt             the user's home, reached as ~ and $HOME
//	root/escape -> outside    a symlink leaving the allow root
type pathWorld struct {
	url              string
	root, real, home string
	outside          string
	client           *mcp.ClientSession
	spellings        []spelling
}

// spelling is one way of naming a directory D: the cwd to send and the path of
// D itself, from which the path of a child is derived.
type spelling struct {
	name     string
	cwd      string // value for the cwd parameter ("" omits it)
	dir      string // D as a path argument
	child    func(string) string
	resolved string // what D resolves to
}

func newPathWorld(t *testing.T) *pathWorld {
	t.Helper()
	w := &pathWorld{}
	var err error
	if w.home, err = filepath.EvalSymlinks(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if w.outside, err = filepath.EvalSymlinks(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", w.home)
	t.Setenv("USERPROFILE", w.home)
	_, w.url, w.root = conformanceAgent(t, func(agent map[string]any, root string) {
		agent["cwd_policy"] = map[string]any{"allow": []string{root, w.home}}
		agent["workspaces"] = map[string]any{"ws": map[string]any{"path": filepath.Join(root, "real")}}
	})
	w.real = filepath.Join(w.root, "real")
	for _, dir := range []string{w.real, filepath.Join(w.real, "sub")} {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{filepath.Join(w.real, "file.txt"), filepath.Join(w.home, "file.txt"), filepath.Join(w.outside, "secret.txt")} {
		if err = os.WriteFile(file, []byte("alpha\nbeta\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Symlink(w.real, filepath.Join(w.root, "link")); err != nil {
		t.Skip("symlinks are not available:", err)
	}
	if err = os.Symlink(w.outside, filepath.Join(w.root, "escape")); err != nil {
		t.Skip("symlinks are not available:", err)
	}
	join := func(prefix string) func(string) string {
		return func(name string) string { return prefix + name }
	}
	w.spellings = []spelling{
		{"symlink", w.root, "link", join("link/"), w.real},
		{"alias", "@ws", ".", join(""), w.real},
		{"tilde", w.root, "~", join("~/"), w.home},
	}
	w.client = connect(t, w.url)
	return w
}

// CON-TOOL-03: every path-taking tool that succeeds returns the symlink-
// resolved absolute path, never the `~`, `@alias` or symlink spelling it was
// given. The symlink-escape spelling is refused with invalid_cwd/symlink_escape
// and no resolved_path.
// conformance: CON-TOOL-03
func Test_CONTOOL03_ResolvedPathEverywhere(t *testing.T) {
	w := newPathWorld(t)
	for _, sp := range w.spellings {
		t.Run(sp.name, func(t *testing.T) {
			must := func(name string, args map[string]any, wantResolved string) map[string]any {
				t.Helper()
				if sp.cwd != "" {
					args["cwd"] = sp.cwd
				}
				out := tool(t, w.client, name, args)
				if out.IsError {
					t.Fatalf("%s %v: %v", name, args, object(t, out))
				}
				v := object(t, out)
				got, _ := v["resolved_path"].(string)
				if got != wantResolved {
					t.Fatalf("%s %v: resolved_path = %q, want %q", name, args, got, wantResolved)
				}
				for _, spelled := range []string{sp.cwd, sp.dir, "~", "@ws"} {
					if spelled != "" && spelled != "." && got == spelled {
						t.Fatalf("%s echoed its input %q as resolved_path", name, spelled)
					}
				}
				return v
			}
			file := func(name string) string { return filepath.Join(sp.resolved, name) }
			must("fs.write", map[string]any{"path": sp.child("new.txt"), "content": "one two\n"}, file("new.txt"))
			must("fs.read", map[string]any{"path": sp.child("new.txt")}, file("new.txt"))
			must("fs.edit", map[string]any{"path": sp.child("new.txt"), "old_string": "one", "new_string": "1"}, file("new.txt"))
			must("fs.sha256", map[string]any{"path": sp.child("new.txt")}, file("new.txt"))
			must("fs.stat", map[string]any{"path": sp.child("new.txt")}, file("new.txt"))
			must("fs.stat", map[string]any{"path": sp.child("absent.txt")}, file("absent.txt"))
			must("fs.list", map[string]any{"path": sp.dir}, sp.resolved)
			must("fs.mkdir", map[string]any{"path": sp.child("made")}, file("made"))
			must("fs.move", map[string]any{"from": sp.child("new.txt"), "to": sp.child("moved.txt")}, file("moved.txt"))
			must("fs.glob", map[string]any{"pattern": "*.txt", "path": sp.dir}, sp.resolved)
			must("fs.grep", map[string]any{"pattern": "alpha", "path": sp.dir, "output_mode": "files"}, sp.resolved)
			must("fs.delete", map[string]any{"path": sp.child("moved.txt")}, file("moved.txt"))
			must("fs.delete", map[string]any{"path": sp.child("made"), "recursive": true}, file("made"))

			// exec.run and pty.open resolve their cwd the same way.
			execArgs := map[string]any{"cmd": "printf", "args": []string{"x"}}
			if runtime.GOOS == "windows" {
				execArgs = map[string]any{"cmd": "powershell", "args": []string{"-NoProfile", "-Command", "Write-Output x"}}
			}
			execArgs["cwd"] = sp.dir
			if sp.cwd != "" && sp.dir == "." {
				execArgs["cwd"] = sp.cwd
			}
			out := tool(t, w.client, "exec.run", execArgs)
			if out.IsError || object(t, out)["resolved_path"] != sp.resolved {
				t.Fatalf("exec.run cwd %v: %v", execArgs["cwd"], object(t, out))
			}
			ptyArgs := map[string]any{"cols": 80, "rows": 24, "cwd": execArgs["cwd"]}
			if opened := tool(t, w.client, "pty.open", ptyArgs); opened.IsError || object(t, opened)["resolved_path"] != sp.resolved {
				t.Fatalf("pty.open cwd %v: %v", ptyArgs["cwd"], object(t, opened))
			}
		})
	}

	t.Run("symlink_escape", func(t *testing.T) {
		refused := func(name string, args map[string]any) {
			t.Helper()
			out := tool(t, w.client, name, args)
			v := object(t, out)
			if !out.IsError || v["error"] != "invalid_cwd" || v["reason"] != "symlink_escape" {
				t.Fatalf("%s %v: want invalid_cwd/symlink_escape, got %v", name, args, v)
			}
			if _, leaked := v["resolved_path"]; leaked {
				t.Fatalf("%s: a refusal carries no resolved_path: %v", name, v)
			}
		}
		refused("fs.read", map[string]any{"path": "escape/secret.txt"})
		refused("fs.write", map[string]any{"path": "escape/new.txt", "content": "x"})
		refused("fs.list", map[string]any{"path": "escape"})
		refused("fs.glob", map[string]any{"pattern": "*", "path": "escape"})
		refused("exec.run", map[string]any{"cmd": "pwd", "cwd": "escape"})
		refused("pty.open", map[string]any{"cols": 80, "rows": 24, "cwd": "escape"})
		if _, err := os.Stat(filepath.Join(w.outside, "new.txt")); !os.IsNotExist(err) {
			t.Fatal("the refused write reached outside the allow root")
		}
	})
}

// CON-TOOL-01: exec cwd, pty cwd and every fs path go through the one
// Resolver. The same spelling means the same place in all three; the same
// invalid spelling is refused with the same error in all three.
// conformance: CON-TOOL-01
func Test_CONTOOL01_OneResolverForExecPTYAndFS(t *testing.T) {
	w := newPathWorld(t)
	ptyDirs := func(cwd string) (string, string) {
		args := map[string]any{"cols": 80, "rows": 24, "cwd": cwd}
		out := tool(t, w.client, "pty.open", args)
		v := object(t, out)
		if out.IsError {
			return v["error"].(string), asString(v["reason"])
		}
		return "ok:" + v["resolved_path"].(string), ""
	}
	execDirs := func(cwd string) (string, string) {
		args := map[string]any{"cmd": "printf", "args": []string{"x"}, "cwd": cwd}
		if runtime.GOOS == "windows" {
			args = map[string]any{"cmd": "powershell", "args": []string{"-NoProfile", "-Command", "Write-Output x"}, "cwd": cwd}
		}
		out := tool(t, w.client, "exec.run", args)
		v := object(t, out)
		if out.IsError {
			return v["error"].(string), asString(v["reason"])
		}
		return "ok:" + v["resolved_path"].(string), ""
	}
	fsDirs := func(cwd string) (string, string) {
		out := tool(t, w.client, "fs.list", map[string]any{"path": ".", "cwd": cwd})
		v := object(t, out)
		if out.IsError {
			return v["error"].(string), asString(v["reason"])
		}
		return "ok:" + v["resolved_path"].(string), ""
	}
	cases := []struct {
		spelling string
		want     string
		reason   string
	}{
		{"@ws", "ok:" + w.real, ""},
		{"~", "ok:" + w.home, ""},
		{"~/", "ok:" + w.home, ""},
		{"$HOME", "ok:" + w.home, ""},
		{filepath.Join(w.root, "link"), "ok:" + w.real, ""},
		{"link", "", ""}, // relative to the default cwd: root/link
		{"real/../real", "", ""},
		{"@nope", "invalid_cwd", "not_found"},
		{"escape", "invalid_cwd", "symlink_escape"},
		{"missing-dir", "invalid_cwd", "not_found"},
		{w.outside, "invalid_cwd", "denied"},
		{"bad\x00path", "invalid_args", ""},
	}
	for _, c := range cases {
		want, wantReason := c.want, c.reason
		if c.spelling == "link" || c.spelling == "real/../real" {
			want = "ok:" + w.real
		}
		kinds := map[string]func(string) (string, string){"pty cwd": ptyDirs, "fs path": fsDirs}
		if runtime.GOOS != "windows" {
			// PowerShell start-up is slow on Windows; exec cwd is covered by Test_CONTOOL03_ResolvedPathEverywhere there.
			kinds["exec cwd"] = execDirs
		}
		for kind, resolve := range kinds {
			got, reason := resolve(c.spelling)
			if got != want || reason != wantReason {
				t.Errorf("%s = %q: got %s/%s, want %s/%s", kind, c.spelling, got, reason, want, wantReason)
			}
		}
	}
	// The workspace itself is configuration: relative spellings use the default cwd.
	if got, _ := fsDirs("."); got != "ok:"+w.root {
		t.Errorf("the default cwd resolves to %s, want %s", got, w.root)
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
