package main

import (
	"flag"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Recognize the single ExecStart generated for a node. Ambiguous custom units
// require manual repair before setup may replace their registration.
func setupUnitConfigMatches(raw []byte, expected string) bool {
	section := ""
	commands := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasSuffix(line, `\`) {
			return false
		}
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "[Service]" || strings.TrimSpace(key) != "ExecStart" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			commands = nil
		} else {
			commands = append(commands, value)
		}
	}
	if len(commands) != 1 {
		return false
	}
	args, ok := splitSetupUnitCommand(commands[0])
	if !ok || len(args) < 3 || !path.IsAbs(args[0]) || args[1] != "serve" {
		return false
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "")
	for _, name := range []string{"listen", "state-dir", "oauth-password-file", "tunnel-listen", "control-allow-sid"} {
		flags.String(name, "", "")
	}
	for _, name := range []string{"local", "local-only", "ingress", "service"} {
		flags.Bool(name, false, "")
	}
	if flags.Parse(args[2:]) != nil || flags.NArg() != 0 {
		return false
	}
	count := 0
	for _, arg := range args[2:] {
		if arg == "--config" || arg == "-config" || strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "-config=") {
			count++
		}
	}
	return count == 1 && path.IsAbs(*configPath) && *configPath == expected
}

func splitSetupUnitCommand(value string) ([]string, bool) {
	args := []string{}
	var word strings.Builder
	var quote byte
	active := false
	for len(value) > 0 {
		c := value[0]
		if c == '\x00' || c == '\n' || c == '\r' {
			return nil, false
		}
		if c == '\t' || c == ' ' {
			if quote == 0 {
				if active {
					args = append(args, word.String())
					word.Reset()
					active = false
				}
				value = value[1:]
				continue
			}
		}
		if c == '\'' || c == '"' {
			if quote == 0 {
				quote = c
				active = true
				value = value[1:]
				continue
			}
			if quote == c {
				quote = 0
				value = value[1:]
				continue
			}
		}
		if c == '\\' {
			if strings.HasPrefix(value, `\s`) {
				word.WriteByte(' ')
				value = value[2:]
				active = true
				continue
			}
			if strings.HasPrefix(value, `\'`) {
				word.WriteByte('\'')
				value = value[2:]
				active = true
				continue
			}
			r, _, tail, err := strconv.UnquoteChar(value, '"')
			if err != nil || r == 0 {
				return nil, false
			}
			word.WriteRune(r)
			value = tail
			active = true
			continue
		}
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			return nil, false
		}
		word.WriteRune(r)
		value = value[size:]
		active = true
	}
	if quote != 0 {
		return nil, false
	}
	if active {
		args = append(args, word.String())
	}
	for _, arg := range args {
		// Environment and systemd specifier expansion must not change the path
		// after this comparison. Such custom registrations are left untouched.
		if strings.ContainsAny(arg, "$%") {
			return nil, false
		}
	}
	return args, true
}

func setupUnitManagerMatches(raw []byte, expected string) bool {
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return false
		}
		if _, exists := values[key]; exists {
			return false
		}
		values[key] = value
	}
	fragment, ok := values["FragmentPath"]
	dropins, haveDropins := values["DropInPaths"]
	return ok && haveDropins && fragment == expected && dropins == "" && values["NeedDaemonReload"] == "no"
}
