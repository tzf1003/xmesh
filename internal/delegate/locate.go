package delegate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	runexec "github.com/tzf1003/xmesh/internal/exec"
)

// A node started by launchd or systemd gets a minimal PATH (macOS: /usr/bin:/bin:/usr/sbin:/sbin), while Claude Code and
// Codex are installed in the user's own directories. locateRunner therefore looks in these directories when a bare
// command name is not on PATH.
func installDirs() []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".claude", "local"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".volta", "bin"),
			filepath.Join(home, ".cargo", "bin"),
		)
		if runtime.GOOS == "windows" {
			dirs = append(dirs, filepath.Join(home, "AppData", "Roaming", "npm"))
		}
	}
	if runtime.GOOS != "windows" {
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin")
	}
	return dirs
}

// locateRunner resolves cmd like exec.LookupCommand does for exec.run. A bare name that is not on PATH is then searched
// in installDirs. It returns the absolute path of the executable. An explicit path is never searched elsewhere.
func locateRunner(cmd, dir string) (string, error) {
	path, err := runexec.LookupCommand(cmd, dir)
	if err == nil || strings.ContainsAny(cmd, `/\`) || filepath.Base(cmd) != cmd {
		return path, err
	}
	for _, d := range installDirs() {
		if found, e := runexec.LookupCommand(filepath.Join(d, cmd), dir); e == nil {
			return found, nil
		}
	}
	return "", err
}

// withBinaryDir returns env with the directory of binary at the front of PATH when PATH does not have it yet. Scripts
// that start with "#!/usr/bin/env node" need the directory of their interpreter, which is usually next to them.
func withBinaryDir(env []string, binary string) (extra map[string]string, ok bool) {
	dir := filepath.Dir(binary)
	sep := string(os.PathListSeparator)
	current := ""
	for _, kv := range env {
		if k, v, found := strings.Cut(kv, "="); found && (k == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(k, "PATH"))) {
			current = v
		}
	}
	for _, entry := range strings.Split(current, sep) {
		if entry == dir {
			return nil, false
		}
	}
	if current == "" {
		return map[string]string{"PATH": dir}, true
	}
	return map[string]string{"PATH": dir + sep + current}, true
}
