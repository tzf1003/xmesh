// Package oauth implements single-subject OAuth and tool scope filtering.
package oauth

import (
	"encoding/json"
	"regexp"
	"strings"
)

var nodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)

func Expand(scopes []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, scope := range scopes {
		p := strings.Split(scope, ":")
		validDevice := func(v string) bool { return v == "*" || nodePattern.MatchString(v) }
		valid := scope == "devices:read" || (len(p) == 2 && (p[0] == "exec" || p[0] == "pty" || p[0] == "sys" || p[0] == "fs") && validDevice(p[1])) || (len(p) == 3 && p[0] == "fs" && (p[1] == "read" || p[1] == "write") && validDevice(p[2])) || (len(p) == 3 && p[0] == "backend" && validDevice(p[1]) && validDevice(p[2]))
		valid = valid || (len(p) == 3 && p[0] == "computer" && (p[1] == "observe" || p[1] == "control" || p[1] == "clipboard-read" || p[1] == "clipboard-write") && validDevice(p[2]))
		if !valid {
			return nil, errScope
		}
		expanded := []string{scope}
		if len(p) == 2 && p[0] == "fs" {
			expanded = []string{"fs:read:" + p[1], "fs:write:" + p[1]}
		}
		for _, v := range expanded {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out, nil
}
func Allows(scopes []string, device, tool, backend string) bool {
	if computerBuiltin(tool) && backend == "" {
		if computerScope(scopes, device, "control") {
			return true
		}
		return tool != "computer.act" && computerScope(scopes, device, "observe")
	}
	if strings.HasPrefix(tool, "backend.") && backend == "" {
		expanded, e := Expand(scopes)
		if e != nil {
			return false
		}
		for _, scope := range expanded {
			p := strings.Split(scope, ":")
			if len(p) == 3 && p[0] == "backend" && (p[1] == "*" || p[1] == device) {
				return true
			}
		}
		return false
	}
	required := ""
	switch {
	case tool == "devices.list" || tool == "tools.search" || tool == "session.set_defaults":
		required = "devices:read"
	case strings.HasPrefix(tool, "sys."):
		required = "sys:" + device
	case strings.HasPrefix(tool, "exec."):
		required = "exec:" + device
	case strings.HasPrefix(tool, "pty."):
		required = "pty:" + device
	case strings.HasPrefix(tool, "fs."):
		read := tool == "fs.read" || tool == "fs.download" || tool == "fs.glob" || tool == "fs.grep" || tool == "fs.list" || tool == "fs.stat" || tool == "fs.sha256"
		if read {
			required = "fs:read:" + device
		} else {
			required = "fs:write:" + device
		}
	default:
		if backend == "" {
			backend, _, _ = strings.Cut(tool, ".")
		}
		required = "backend:" + device + ":" + backend
	}
	need := strings.Split(required, ":")
	expanded, e := Expand(scopes)
	if e != nil {
		return false
	}
	for _, scope := range expanded {
		p := strings.Split(scope, ":")
		if len(p) != len(need) {
			continue
		}
		ok := true
		for i := range p {
			if p[i] != "*" && p[i] != need[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func computerBuiltin(tool string) bool {
	switch tool {
	case "computer.info", "computer.open", "computer.observe", "computer.act", "computer.action_status", "computer.renew", "computer.close", "computer.sessions":
		return true
	default:
		return false
	}
}

func computerScope(scopes []string, device, capability string) bool {
	if _, err := Expand(scopes); err != nil {
		return false
	}
	for _, s := range scopes {
		p := strings.Split(s, ":")
		if len(p) == 3 && p[0] == "computer" && p[1] == capability && (p[2] == device || p[2] == "*") {
			return true
		}
	}
	return false
}

// AllowsArguments applies the additional privilege needed by mixed-mode tools.
// Tool visibility alone must not let an observer reserve the desktop's input authority.
func AllowsArguments(scopes []string, device, tool string, args map[string]json.RawMessage) bool {
	if tool != "computer.open" {
		return true
	}
	var mode string
	if json.Unmarshal(args["mode"], &mode) != nil {
		return false
	}
	if mode == "control" {
		return computerScope(scopes, device, "control")
	}
	return mode == "observe" && (computerScope(scopes, device, "observe") || computerScope(scopes, device, "control"))
}
