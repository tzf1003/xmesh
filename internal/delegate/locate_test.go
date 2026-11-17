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

func TestWithDirsAddsOnlyMissingDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path layout")
	}
	sep := string(os.PathListSeparator)
	env := []string{"HOME=/h", "PATH=/usr/bin" + sep + "/bin"}
	extra, ok := withDirs(env, []string{"/opt/x", "/usr/bin", "/opt/x"})
	if !ok || extra["PATH"] != "/opt/x"+sep+"/usr/bin"+sep+"/bin" {
		t.Fatalf("withDirs = %v, %v", extra, ok)
	}
	if _, ok := withDirs(env, []string{"/usr/bin"}); ok {
		t.Fatal("a directory already on PATH must not be added again")
	}
}

// An npm CLI is a script that starts with "#!/usr/bin/env node", and Node may live in another directory than the CLI.
func TestRunnerDirsIncludesTheInterpreterDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shebang scripts")
	}
	home := t.TempDir()
	cliDir := filepath.Join(home, ".npm-global", "bin")
	nodeDir := filepath.Join(home, ".local", "bin")
	for _, d := range []string{cliDir, nodeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(cliDir, "codex")
	if err := os.WriteFile(cli, []byte("#!/usr/bin/env fakenode\nconsole.log(1)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeDir, "fakenode"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	got := runnerDirs(cli, t.TempDir())
	if len(got) != 2 || got[0] != cliDir || got[1] != nodeDir {
		t.Fatalf("runnerDirs = %v, want [%s %s]", got, cliDir, nodeDir)
	}
	if plain := runnerDirs(filepath.Join(nodeDir, "fakenode"), t.TempDir()); len(plain) != 1 {
		t.Fatalf("a shell script needs only its own directory, got %v", plain)
	}
}
