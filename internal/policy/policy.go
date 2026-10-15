// Package policy enforces command and environment rules before side effects.
package policy

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type Rule struct {
	name string
	re   *regexp.Regexp
}
type Engine struct {
	config config.Policy
	rules  []Rule
}

var safe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func Base(cmd string) string {
	b := filepath.Base(strings.ReplaceAll(cmd, `\`, "/"))
	return strings.TrimSuffix(strings.ToLower(b), ".exe")
}
func Normalize(cmd string, args []string) string {
	base := path.Base(strings.ReplaceAll(cmd, `\`, "/"))
	if runtime.GOOS == "windows" && strings.HasSuffix(strings.ToLower(base), ".exe") {
		base = base[:len(base)-4]
	}
	line := base
	for _, a := range args {
		if !safe.MatchString(a) {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		line += " " + a
	}
	return line
}
func New(p config.Policy) (*Engine, error) {
	e := &Engine{config: p}
	patterns := map[string]string{}
	if !p.AllowDangerous {
		patterns["exec.deny.rm_rf_root"] = `(?i)\brm\s+(-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*|--recursive)\s+(/|[A-Za-z]:\\)(\s|$)`
		patterns["exec.deny.curl_pipe_sh"] = `(?i)\bcurl\b[^|\n]*\|\s*(sh|bash|zsh|pwsh|powershell)\b`
		patterns["exec.deny.format"] = `(?i)\bformat\s+[A-Za-z]:`
	}
	names := []string{}
	for n := range patterns {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e.rules = append(e.rules, Rule{n, regexp.MustCompile(patterns[n])})
	}
	for i, p := range p.Exec.DenyPatterns {
		r, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		e.rules = append(e.rules, Rule{fmt.Sprintf("exec.deny.user.%d", i), r})
	}
	return e, nil
}
func denied(rule string) error {
	return toolerr.New("policy_denied", "操作命中拒绝规则", "rule", rule, "reason", "当前策略不允许此操作")
}
func (e *Engine) Command(cmd string, args []string) error {
	if cmd == "" || strings.ContainsRune(cmd, 0) {
		return toolerr.Args("cmd")
	}
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return toolerr.Args("args")
		}
	}
	base := Base(cmd)
	if !e.config.AllowDangerous {
		if base == "format" {
			return denied("exec.deny.format")
		}
		for _, a := range args {
			if strings.EqualFold(a, "-EncodedCommand") {
				return denied("exec.deny.encoded_command")
			}
		}
	}
	if len(e.config.Exec.AllowCmd) > 0 {
		ok := false
		for _, c := range e.config.Exec.AllowCmd {
			if c == cmd || c == base {
				ok = true
				break
			}
		}
		if !ok {
			return denied("exec.allow_cmd")
		}
	}
	texts := []string{Normalize(cmd, args)}
	for i, a := range args {
		if i+1 >= len(args) {
			break
		}
		if ((base == "sh" || base == "bash" || base == "zsh" || base == "dash") && a == "-c") || ((base == "pwsh" || base == "powershell") && strings.EqualFold(a, "-Command")) || (base == "cmd" && strings.EqualFold(a, "/c")) {
			texts = append(texts, args[i+1])
		}
	}
	for _, r := range e.rules {
		for _, text := range texts {
			if r.re.MatchString(text) {
				return denied(r.name)
			}
		}
	}
	return nil
}
func (e *Engine) denyEnv(k, v string) bool {
	for _, p := range append([]string{"AWS_*", "AZURE_*", "GOOGLE_*", "SSH_AUTH_SOCK", "TS_AUTHKEY", "TAILSCALE_*", "XMESH_*", "OPENAI_*", "ANTHROPIC_*", "LD_*", "DYLD_*", "BASH_ENV", "ENV", "ZDOTDIR", "PYTHONPATH", "NODE_OPTIONS"}, e.config.Env.DenyNames...) {
		if ok, _ := path.Match(strings.ToUpper(p), strings.ToUpper(k)); ok {
			return true
		}
	}
	for _, p := range append([]string{".ssh", "gcloud", ".azure", ".aws"}, e.config.Env.DenyPathSubstrings...) {
		if strings.Contains(strings.ToLower(v), strings.ToLower(p)) {
			return true
		}
	}
	return false
}
func (e *Engine) Environment(extra map[string]string) ([]string, error) {
	return e.environment(os.Environ(), extra, runtime.GOOS == "windows")
}

func (e *Engine) environment(inherited []string, extra map[string]string, foldCase bool) ([]string, error) {
	normalize := func(k string) string {
		if foldCase {
			return strings.ToUpper(k)
		}
		return k
	}
	env := map[string]string{}
	for _, kv := range inherited {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || strings.ContainsRune(kv, 0) {
			continue
		}
		k = normalize(k)
		modulePath := foldCase && k == "PSMODULEPATH"
		if k == "PATH" || modulePath {
			separator := ":"
			if foldCase {
				separator = ";"
			}
			entries := strings.Split(v, separator)
			allowed := entries[:0]
			for _, entry := range entries {
				if !e.denyEnv(k, entry) {
					allowed = append(allowed, entry)
				}
			}
			if len(allowed) == 0 {
				continue
			}
			v = strings.Join(allowed, separator)
		}
		windowsRuntime := foldCase && (k == "SYSTEMROOT" || k == "WINDIR" || k == "SYSTEMDRIVE" || k == "COMSPEC" || k == "PATHEXT" || k == "TEMP" || k == "TMP" || k == "APPDATA" || k == "LOCALAPPDATA" || k == "PROGRAMFILES")
		if k == "PATH" || modulePath || k == "HOME" || k == "USERPROFILE" || k == normalize("SystemRoot") || k == "LANG" || k == "LANGUAGE" || strings.HasPrefix(k, "LC_") || windowsRuntime {
			if !e.denyEnv(k, v) {
				env[k] = v
			}
		}
	}
	seen := make(map[string]bool, len(extra))
	for k, v := range extra {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return nil, toolerr.Args("env")
		}
		if e.denyEnv(k, v) {
			return nil, denied("env.denied")
		}
		k = normalize(k)
		if seen[k] {
			return nil, toolerr.Args("env")
		}
		seen[k] = true
		env[k] = v
	}
	if foldCase && !seen["PSMODULEPATH"] && env["PSMODULEPATH"] == "" {
		// A present empty value lets each PowerShell runtime initialize its own
		// module defaults, including when the Agent was launched outside a shell.
		if env["SYSTEMROOT"] != "" && !e.denyEnv("PSMODULEPATH", "") {
			env["PSMODULEPATH"] = ""
		}
	}
	keys := []string{}
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out, nil
}
