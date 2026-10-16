package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cwd_policy applies to the cwd handed to pty.open, not to later `cd` commands
// typed into the shell. A PTY opened in an allowed directory can `cd` into a
// directory the policy denies without being blocked, while exec and fs calls
// that name the denied directory are still refused. pty.list keeps reporting the
// start directory because it does not track `cd`.
// conformance: CON-SEC-05
func Test_CONSEC05_CdInsidePTYNotIntercepted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test drives a POSIX shell with `cd` and `$(pwd -P)`")
	}
	_, url, root := conformanceAgent(t, func(agent map[string]any, root string) {
		agent["cwd_policy"] = map[string]any{"allow": []string{root}, "deny": []string{filepath.Join(root, "secret")}}
		agent["pty"] = map[string]any{"default_shell": "/bin/sh"}
	})
	secret := filepath.Join(root, "secret")
	if err := os.Mkdir(secret, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secret, "inside.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := connect(t, url)

	// The policy itself denies the directory for every path-taking tool.
	for name, args := range map[string]map[string]any{
		"exec.run": {"cmd": "pwd", "cwd": secret},
		"pty.open": {"cols": 80, "rows": 24, "cwd": secret},
		"fs.read":  {"path": filepath.Join(secret, "inside.txt")},
		"fs.write": {"path": filepath.Join(secret, "new.txt"), "content": "x"},
	} {
		out := tool(t, client, name, args)
		if !out.IsError || object(t, out)["error"] != "invalid_cwd" || object(t, out)["reason"] != "denied" {
			t.Fatalf("%s into the denied directory: %v", name, object(t, out))
		}
	}
	if _, err := os.Stat(filepath.Join(secret, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("denied fs.write created a file")
	}

	opened := tool(t, client, "pty.open", map[string]any{"cols": 120, "rows": 24, "cwd": root})
	if opened.IsError {
		t.Fatal(object(t, opened))
	}
	id := object(t, opened)["session_id"].(string)
	// The echoed command line contains `AT:%s:END`, the shell's output contains
	// the expanded directory, so only real output matches the pattern.
	command := "cd secret && printf 'AT:%s:END\\n' \"$(pwd -P)\" && ls\n"
	if out := tool(t, client, "pty.write", map[string]any{"session_id": id, "data": command}); out.IsError {
		t.Fatal(object(t, out))
	}
	pattern := regexp.MustCompile(`AT:(/[^\r\n]*):END`)
	var text strings.Builder
	cursor := float64(0)
	deadline := time.Now().Add(15 * time.Second)
	var matched []string
	for time.Now().Before(deadline) && matched == nil {
		out := tool(t, client, "pty.read", map[string]any{"session_id": id, "cursor": uint64(cursor), "wait_ms": 200})
		if out.IsError {
			t.Fatal(object(t, out))
		}
		v := object(t, out)
		text.WriteString(v["data"].(string))
		cursor = v["next_cursor"].(float64)
		matched = pattern.FindStringSubmatch(text.String())
	}
	if matched == nil {
		t.Fatalf("the shell never reported its directory; output so far: %q", text.String())
	}
	reported, _ := filepath.EvalSymlinks(matched[1])
	if reported != secret {
		t.Fatalf("shell is in %q, want the policy-denied %q", reported, secret)
	}
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(text.String(), "inside.txt") && time.Now().Before(deadline); {
		out := object(t, tool(t, client, "pty.read", map[string]any{"session_id": id, "cursor": uint64(cursor), "wait_ms": 200}))
		text.WriteString(out["data"].(string))
		cursor = out["next_cursor"].(float64)
	}
	if !strings.Contains(text.String(), "inside.txt") {
		t.Fatalf("the shell could not list the directory it cd'd into: %q", text.String())
	}

	// Nothing re-checked the new directory: the session is still active and its
	// recorded cwd is the start directory.
	listed := object(t, tool(t, client, "pty.list", map[string]any{}))["sessions"].([]any)
	if len(listed) != 1 {
		t.Fatal(listed)
	}
	row := listed[0].(map[string]any)
	if row["state"] != "active" || row["cwd"] != root {
		t.Fatalf("cd changed the session record: %v", row)
	}
	// And the denial for tool-level paths is unchanged afterwards.
	if out := tool(t, client, "exec.run", map[string]any{"cmd": "pwd", "cwd": secret}); !out.IsError || object(t, out)["error"] != "invalid_cwd" {
		t.Fatal("exec.run into the denied directory was accepted after the cd", object(t, out))
	}
}
