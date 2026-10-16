package agent

import (
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Credentials in the Agent's own environment (auth keys, OAuth client secrets,
// cloud keys, SSH agent socket, credential directories on PATH) are not passed
// to exec or PTY children; the child starts from the minimal environment. A
// caller that passes a denied variable explicitly gets policy_denied/env.denied
// and nothing is started.
// conformance: CON-SEC-04
func Test_CONSEC04_CredentialsDoNotReachChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX env(1) and a POSIX shell")
	}
	secrets := map[string]string{
		"TS_AUTHKEY":                     "tskey-auth-SECRET1",
		"TAILSCALE_AUTHKEY":              "tskey-auth-SECRET2",
		"XMESH_OAUTH_CLIENT_SECRET":      "SECRET3",
		"AWS_SECRET_ACCESS_KEY":          "SECRET4",
		"GOOGLE_APPLICATION_CREDENTIALS": "/opt/creds/SECRET5.json",
		"SSH_AUTH_SOCK":                  "/tmp/ssh-SECRET6/agent",
		"ANTHROPIC_API_KEY":              "SECRET7",
		"OPENAI_API_KEY":                 "SECRET8",
		"UNLISTED_APP_TOKEN":             "SECRET9", // not on any list: the minimal environment drops it
	}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	t.Setenv("PATH", os.Getenv("PATH")+":/home/someone/.aws/bin:/home/someone/.ssh/tools")
	_, url, root := conformanceAgent(t, func(agent map[string]any, _ string) {
		agent["pty"] = map[string]any{"default_shell": "/bin/sh"}
	})
	client := connect(t, url)
	assertClean := func(label, output string) {
		t.Helper()
		for name, value := range secrets {
			if strings.Contains(output, name) || strings.Contains(output, value) {
				t.Errorf("%s: the child environment contains %s", label, name)
			}
		}
		if strings.Contains(output, ".aws/bin") || strings.Contains(output, ".ssh/tools") {
			t.Errorf("%s: credential directories stayed on PATH", label)
		}
		if !strings.Contains(output, "PATH=") {
			t.Errorf("%s: the minimal environment must keep PATH: %q", label, output)
		}
	}

	out := tool(t, client, "exec.run", map[string]any{"cmd": "env"})
	if out.IsError {
		t.Fatal(object(t, out))
	}
	assertClean("exec.run", object(t, out)["stdout"].(string))

	opened := tool(t, client, "pty.open", map[string]any{"cols": 200, "rows": 24})
	if opened.IsError {
		t.Fatal(object(t, opened))
	}
	id := object(t, opened)["session_id"].(string)
	tool(t, client, "pty.write", map[string]any{"session_id": id, "data": "env; printf 'ENV_%s\\n' DONE\n"})
	var text strings.Builder
	cursor := float64(0)
	done := regexp.MustCompile(`(?m)^ENV_DONE\r?$`)
	for i := 0; i < 200 && !done.MatchString(text.String()); i++ {
		v := object(t, tool(t, client, "pty.read", map[string]any{"session_id": id, "cursor": uint64(cursor), "wait_ms": 100}))
		text.WriteString(v["data"].(string))
		cursor = v["next_cursor"].(float64)
	}
	if !done.MatchString(text.String()) {
		t.Fatalf("the shell never printed its environment: %q", text.String())
	}
	assertClean("pty.open", text.String())

	// Explicit denied input: refused, and nothing is started.
	before := len(object(t, tool(t, client, "pty.list", map[string]any{}))["sessions"].([]any))
	for label, env := range map[string]map[string]string{
		"denied name":              {"AWS_ACCESS_KEY_ID": "AKIA"},
		"denied name, other case":  {"ssh_auth_sock": "/tmp/agent"},
		"credential path in value": {"TOOL_CONFIG": "/home/someone/.ssh/config"},
		"credential dir on PATH":   {"PATH": "/usr/bin:/home/someone/.aws/bin"},
	} {
		denied := tool(t, client, "exec.run", map[string]any{"cmd": "touch", "args": []string{"started-marker"}, "env": env})
		v := object(t, denied)
		if !denied.IsError || v["error"] != "policy_denied" || v["rule"] != "env.denied" {
			t.Errorf("exec.run with %s: %v", label, v)
		}
		denied = tool(t, client, "pty.open", map[string]any{"cols": 80, "rows": 24, "env": env})
		v = object(t, denied)
		if !denied.IsError || v["error"] != "policy_denied" || v["rule"] != "env.denied" {
			t.Errorf("pty.open with %s: %v", label, v)
		}
	}
	if _, err := os.Stat(root + "/started-marker"); !os.IsNotExist(err) {
		t.Error("a command with a denied environment was started")
	}
	if after := len(object(t, tool(t, client, "pty.list", map[string]any{}))["sessions"].([]any)); after != before {
		t.Errorf("a refused pty.open created a session: %d -> %d", before, after)
	}
	// A harmless caller variable is delivered.
	out = tool(t, client, "exec.run", map[string]any{"cmd": "env", "env": map[string]string{"MY_SETTING": "visible"}})
	if out.IsError || !strings.Contains(object(t, out)["stdout"].(string), "MY_SETTING=visible") {
		t.Errorf("caller env was not applied: %v", object(t, out))
	}
}
