package delegate

import (
	"sort"
	"strings"

	"github.com/tzf1003/xmesh/internal/config"
	runexec "github.com/tzf1003/xmesh/internal/exec"
)

// runner is one way to start an Agent CLI without a screen (CTR-DLG-05). kind selects the output mapping: claude
// and codex have their own, and every other name uses the generic rule.
type runner struct {
	kind       string
	cmd        string
	args       []string
	readArgs   []string
	writeArgs  []string
	resumeArgs []string
}

// builtinRunners are the defaults of CTR-DLG-05. The claude defaults follow the Claude Code 2.1.325 --help output.
// The codex defaults are not verified against a Codex CLI.
var builtinRunners = map[string]runner{
	"claude": {
		kind:       "claude",
		cmd:        "claude",
		args:       []string{"-p", "--output-format", "stream-json", "--verbose"},
		readArgs:   []string{"--permission-mode", "dontAsk", "--permission-prompts", "none", "--allowedTools", "Read,Glob,Grep"},
		writeArgs:  []string{"--permission-mode", "acceptEdits", "--permission-prompts", "none", "--allowedTools", "Read,Glob,Grep,Edit,Write,Bash"},
		resumeArgs: []string{"-p", "--output-format", "stream-json", "--verbose", "--resume", "{session_id}"},
	},
	"codex": {
		kind:       "codex",
		cmd:        "codex",
		args:       []string{"exec", "--json", "--skip-git-repo-check", "-"},
		readArgs:   []string{"--sandbox", "read-only"},
		writeArgs:  []string{"--sandbox", "workspace-write"},
		resumeArgs: []string{"exec", "resume", "{session_id}", "--json", "--skip-git-repo-check", "-"},
	},
}

// runnerFor returns the runner called name: the built-in one with the configured fields over it, or a configured
// runner that no built-in defines. It reports false when neither names one with a command.
func runnerFor(cfg config.Delegate, name string) (runner, bool) {
	base, builtin := builtinRunners[name]
	over, configured := cfg.Runners[name]
	if !builtin && !configured {
		return runner{}, false
	}
	r := base
	if !builtin {
		r.kind = "generic"
	}
	if configured {
		// A nil list keeps the built-in value, and an empty list clears it (config.DelegateRunner).
		if over.Cmd != "" {
			r.cmd = over.Cmd
		}
		if over.Args != nil {
			r.args = over.Args
		}
		if over.ReadArgs != nil {
			r.readArgs = over.ReadArgs
		}
		if over.WriteArgs != nil {
			r.writeArgs = over.WriteArgs
		}
		if over.ResumeArgs != nil {
			r.resumeArgs = over.ResumeArgs
		}
	}
	if r.cmd == "" {
		return runner{}, false
	}
	return r, true
}

// runnerNames lists the built-in runner names and the configured ones, sorted and without repeats.
func runnerNames(cfg config.Delegate) []string {
	seen := map[string]bool{}
	var names []string
	for name := range builtinRunners {
		seen[name] = true
		names = append(names, name)
	}
	for name := range cfg.Runners {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// argv assembles the command line of one task (CTR-DLG-05). The resume arguments replace the base arguments when a
// session is named, and the arguments of the mode follow. A base argument list that ends with a lone "-" (standard
// input) keeps the mode arguments before it. The prompt never appears here.
func (r runner) argv(mode, sessionID string) []string {
	base := r.args
	if sessionID != "" {
		base = make([]string, len(r.resumeArgs))
		for i, arg := range r.resumeArgs {
			base[i] = strings.ReplaceAll(arg, "{session_id}", sessionID)
		}
	}
	modeArgs := r.readArgs
	if mode == "write" {
		modeArgs = r.writeArgs
	}
	argv := []string{r.cmd}
	if n := len(base); n > 0 && base[n-1] == "-" {
		argv = append(argv, base[:n-1]...)
		argv = append(argv, modeArgs...)
		return append(argv, "-")
	}
	argv = append(argv, base...)
	return append(argv, modeArgs...)
}

// runnerViews lists the runners for delegate.list. available reports whether the executable can be found from dir.
func runnerViews(cfg config.Delegate, dir string) []Object {
	out := []Object{}
	for _, name := range runnerNames(cfg) {
		r, ok := runnerFor(cfg, name)
		if !ok {
			continue
		}
		row := Object{"name": name, "available": false}
		if path, err := runexec.LookupCommand(r.cmd, dir); err == nil {
			row["available"] = true
			row["path"] = path
		}
		out = append(out, row)
	}
	return out
}
