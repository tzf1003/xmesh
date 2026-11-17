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

// runnerDirs lists the directories a task needs on PATH to run binary: its own, and, for a script that starts with
// "#!/usr/bin/env node", the directory of that interpreter, which npm and Homebrew often keep elsewhere.
func runnerDirs(binary, dir string) []string {
	dirs := []string{filepath.Dir(binary)}
	f, err := os.Open(binary)
	if err != nil {
		return dirs
	}
	defer f.Close()
	head := make([]byte, 256)
	n, _ := f.Read(head)
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if !strings.HasPrefix(line, "#!") || len(fields) < 2 || filepath.Base(fields[0]) != "env" {
		return dirs
	}
	name := fields[1]
	if strings.HasPrefix(name, "-") && len(fields) > 2 {
		name = fields[2]
	}
	if interpreter, err := locateRunner(name, dir); err == nil && filepath.Dir(interpreter) != dirs[0] {
		dirs = append(dirs, filepath.Dir(interpreter))
	}
	return dirs
}

// withDirs returns the PATH setting for env with dirs put in front of PATH, leaving out the ones PATH already has. ok is
// false when there is nothing to add.
func withDirs(env []string, dirs []string) (extra map[string]string, ok bool) {
	sep := string(os.PathListSeparator)
	current := ""
	for _, kv := range env {
		if k, v, found := strings.Cut(kv, "="); found && (k == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(k, "PATH"))) {
			current = v
		}
	}
	have := map[string]bool{}
	for _, entry := range strings.Split(current, sep) {
		have[entry] = true
	}
	var add []string
	for _, d := range dirs {
		if !have[d] {
			have[d] = true
			add = append(add, d)
		}
	}
	if len(add) == 0 {
		return nil, false
	}
	if current != "" {
		add = append(add, current)
	}
	return map[string]string{"PATH": strings.Join(add, sep)}, true
}
