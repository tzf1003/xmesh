package delegate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

// A node started by launchd has PATH=/usr/bin:/bin:/usr/sbin:/sbin, and Claude Code lives in the user's ~/.local/bin.
// The runner list and the run must both find it.
func TestLocateRunnerFindsUserInstallDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install directories are looked up by plain file name here")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	got, err := locateRunner("claude", t.TempDir())
	if err != nil || got != claude {
		t.Fatalf("locateRunner = %q, %v; want %q", got, err, claude)
	}
	if _, err := locateRunner("no-such-agent-cli", t.TempDir()); err == nil {
		t.Fatal("a command that is nowhere was found")
	}
	// An explicit path is never searched in other places.
	if _, err := locateRunner("./claude", t.TempDir()); err == nil {
		t.Fatal("a relative path that does not exist was found through the install directories")
	}

	rows := runnerRows(config.Delegate{}, t.TempDir())
	found := map[string]RunnerRow{}
	for _, row := range rows {
		found[row.Name] = row
	}
	if r := found["claude"]; !r.Available || r.Path != claude {
		t.Fatalf("claude row = %+v, want available at %s", r, claude)
	}
}

func TestWithBinaryDirAddsOnlyAMissingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path layout")
	}
	sep := string(os.PathListSeparator)
	env := []string{"HOME=/h", "PATH=/usr/bin" + sep + "/bin"}
	extra, ok := withBinaryDir(env, filepath.Join("/opt", "x", "codex"))
	if !ok || extra["PATH"] != filepath.Join("/opt", "x")+sep+"/usr/bin"+sep+"/bin" {
		t.Fatalf("withBinaryDir = %v, %v", extra, ok)
	}
	if _, ok := withBinaryDir(env, "/usr/bin/codex"); ok {
		t.Fatal("a directory already on PATH must not be added again")
	}
}
