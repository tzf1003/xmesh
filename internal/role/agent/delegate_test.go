package agent

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/delegate/delegatetest"
)

// TestMain lets the test binary act as the fake Agent CLI that the delegate runners start (delegatetest), before the
// test framework starts. The other tests of this package are unaffected.
func TestMain(m *testing.M) {
	runRedelegateProbe(os.Args)
	delegatetest.Serve(os.Args)
	os.Exit(m.Run())
}

// delegateConfig writes the base test configuration with agent.delegate set to delegate, or without the section when
// delegate is nil, and opens its store. It returns the file path and the store.
func delegateConfig(t *testing.T, delegate map[string]any) (string, *config.Store) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"node":  map[string]any{"name": "test-node", "roles": []string{"agent"}},
		"mesh":  map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}},
		"agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "authz": map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}, "pty": map[string]any{"default_shell": "auto"}},
	}
	if delegate != nil {
		doc["agent"].(map[string]any)["delegate"] = delegate
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err = os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, store
}

// fakeRunnerConfig is the agent.delegate runner entry that runs one scenario of the fake, with its parameters.
// Its mode arguments are cleared, so the scenario sees only its own parameters.
func fakeRunnerConfig(t *testing.T, scenario string, params ...string) map[string]any {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"cmd":         exe,
		"args":        append([]string{delegatetest.Marker, scenario}, params...),
		"read_args":   []string{},
		"write_args":  []string{},
		"resume_args": []string{delegatetest.Marker, scenario, "{session_id}"},
	}
}

// startDelegate starts an Agent on the configuration that delegate describes and connects one local client. The
// Claude Code configuration directory is empty for the test, so no real session is found.
func startDelegate(t *testing.T, delegate map[string]any) (*Agent, string, *mcp.ClientSession) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	path, store := delegateConfig(t, delegate)
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	return a, path, connect(t, server.URL+"/mcp")
}

// delegateNames returns the delegate tools among names, sorted.
func delegateNames(names []string) []string {
	var out []string
	for _, name := range names {
		if strings.HasPrefix(name, "delegate.") {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// auditRecords reads every record that the audit logger wrote into dir.
func auditRecords(t *testing.T, dir string) []map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var out []map[string]any
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("audit line is not JSON: %q", line)
			}
			out = append(out, rec)
		}
	}
	return out
}

// taskCompleteRecord returns the task_complete record of taskID, or nil.
func taskCompleteRecord(records []map[string]any, taskID string) map[string]any {
	for _, rec := range records {
		if rec["event"] == "task_complete" && rec["run_id"] == taskID {
			return rec
		}
	}
	return nil
}

// conformance: CTR-DLG-01
func Test_CTRDLG01_SwitchHidesAndRefusesTheTools(t *testing.T) {
	_, path, client := startDelegate(t, nil)
	if names := delegateNames(listedToolNames(t, client)); len(names) != 0 {
		t.Fatalf("delegate tools listed while the switch is off: %q", names)
	}
	out := tool(t, client, "delegate.list", map[string]any{})
	if !out.IsError || object(t, out)["rule"] != "delegate.disabled" {
		t.Fatal("a call with the switch off was not refused with delegate.disabled", object(t, out))
	}

	rewriteConfigFile(t, path, func(agent map[string]any) {
		agent["delegate"] = map[string]any{"enabled": true, "runners": map[string]any{"claude": fakeRunnerConfig(t, "claude")}}
	})
	want := []string{"delegate.cancel", "delegate.list", "delegate.output", "delegate.run", "delegate.send"}
	waitUntil(t, func() bool { return slices.Equal(delegateNames(listedToolNames(t, client)), want) })
	if out = tool(t, client, "delegate.list", map[string]any{}); out.IsError {
		t.Fatal("delegate.list refused after the switch went on", object(t, out))
	}

	rewriteConfigFile(t, path, func(agent map[string]any) { agent["delegate"] = map[string]any{"enabled": false} })
	waitUntil(t, func() bool { return len(delegateNames(listedToolNames(t, client))) == 0 })
	if out = tool(t, client, "delegate.list", map[string]any{}); !out.IsError || object(t, out)["rule"] != "delegate.disabled" {
		t.Fatal("delegate.list not refused after the switch went off", object(t, out))
	}
}

// conformance: CTR-DLG-02 CTR-DLG-06
func Test_CTRDLG06_RunThroughMCPWritesAnAuditRecordWithoutThePrompt(t *testing.T) {
	a, _, client := startDelegate(t, map[string]any{"enabled": true, "runners": map[string]any{"claude": fakeRunnerConfig(t, "claude")}})
	dir := t.TempDir()
	logger, err := audit.New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)
	a.SetAudit(logger)

	const secret = "PROMPT-SECRET-7731"
	out := object(t, tool(t, client, "delegate.run", map[string]any{"runner": "claude", "prompt": secret, "wait_ms": 20000}))
	if out["state"] != "done" || out["result"] != "answer:"+secret || out["session_id"] != "sess-1" {
		t.Fatal("delegate.run reply", out)
	}
	taskID, _ := out["task_id"].(string)
	eventually(t, "the task_complete record", func() bool { return taskCompleteRecord(auditRecords(t, dir), taskID) != nil })

	rec := taskCompleteRecord(auditRecords(t, dir), taskID)
	digest, _ := rec["args_digest"].(map[string]any)
	if rec["task_status"] != "passed" || rec["status"] != "ok" || digest["runner"] != "claude" || digest["mode"] != "read" || digest["resolved_path"] != out["resolved_path"] {
		t.Fatalf("task_complete record = %v", rec)
	}
	var text strings.Builder
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, file := range files {
		b, _ := os.ReadFile(file)
		text.Write(b)
	}
	if strings.Contains(text.String(), secret) || strings.Contains(text.String(), "answer:") {
		t.Fatal("the audit log carries the prompt or the reply")
	}
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_DrainRefusesRunAndSendButKeepsWrapUpCalls(t *testing.T) {
	a, _, client := startDelegate(t, map[string]any{"enabled": true, "runners": map[string]any{"sleepy": fakeRunnerConfig(t, "sleep")}})
	started := object(t, tool(t, client, "delegate.run", map[string]any{"runner": "sleepy", "prompt": "x", "wait_ms": 0}))
	taskID, _ := started["task_id"].(string)

	a.Drain(false)
	t.Cleanup(a.Resume)
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"delegate.run", map[string]any{"runner": "sleepy", "prompt": "y"}},
		{"delegate.send", map[string]any{"session_id": "sess-none", "message": "hi"}},
	} {
		out := tool(t, client, call.name, call.args)
		if !out.IsError || object(t, out)["rule"] != "mesh.draining" {
			t.Fatalf("%s while draining: %v", call.name, object(t, out))
		}
	}
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"delegate.list", map[string]any{}},
		{"delegate.output", map[string]any{"task_id": taskID}},
	} {
		if out := tool(t, client, call.name, call.args); out.IsError {
			t.Fatalf("%s refused while draining: %v", call.name, object(t, out))
		}
	}
	if state := object(t, tool(t, client, "delegate.cancel", map[string]any{"task_id": taskID}))["state"]; state != "cancelled" {
		t.Fatalf("cancel while draining reports %v", state)
	}
}

// conformance: CTR-DLG-02 CTR-DLG-04 CTR-DLG-06
func Test_CTRDLG06_ConfigSwitchesReachTheTools(t *testing.T) {
	argv := fakeRunnerConfig(t, "argv")
	argv["write_args"] = []string{"--write-mode"}
	_, path, client := startDelegate(t, map[string]any{"enabled": true, "allow_sessions": false, "runners": map[string]any{"argv": argv}})
	writeArgs := map[string]any{"runner": "argv", "prompt": "edit", "mode": "write", "wait_ms": 20000}
	if out := tool(t, client, "delegate.run", writeArgs); !out.IsError || object(t, out)["rule"] != "delegate.write_disabled" {
		t.Fatal("write mode was not refused without allow_write", object(t, out))
	}
	if out := object(t, tool(t, client, "delegate.list", map[string]any{})); len(out["sessions"].([]any)) != 0 {
		t.Fatal("sessions listed with allow_sessions false", out["sessions"])
	}
	if out := tool(t, client, "delegate.send", map[string]any{"session_id": "sess-none", "message": "hi"}); !out.IsError || object(t, out)["rule"] != "delegate.sessions_disabled" {
		t.Fatal("send not refused with allow_sessions false", object(t, out))
	}

	rewriteConfigFile(t, path, func(agent map[string]any) {
		delegate := agent["delegate"].(map[string]any)
		delegate["allow_write"] = true
	})
	eventually(t, "allow_write to take effect", func() bool {
		out := tool(t, client, "delegate.run", writeArgs)
		return !out.IsError && strings.Contains(object(t, out)["result"].(string), "--write-mode")
	})
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_DelegateToolsFollowAuthz(t *testing.T) {
	_, store := delegateConfig(t, map[string]any{"enabled": true, "runners": map[string]any{"claude": fakeRunnerConfig(t, "claude")}})
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(taggedAuth))
	t.Cleanup(server.Close)

	stranger := connectTagged(t, server.URL+"/mcp", "tag:other")
	if names := delegateNames(listedToolNames(t, stranger)); len(names) != 0 {
		t.Fatalf("a caller outside authz sees %q", names)
	}
	out := tool(t, stranger, "delegate.list", map[string]any{})
	if !out.IsError || object(t, out)["error"] != "unauthorized" {
		t.Fatal("a caller outside authz was not refused", object(t, out))
	}
	peer := connectTagged(t, server.URL+"/mcp", "tag:peer")
	if out = tool(t, peer, "delegate.list", map[string]any{}); out.IsError {
		t.Fatal("an authorized caller was refused", object(t, out))
	}
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_SwitchOffEndsRunningTasks(t *testing.T) {
	_, path, client := startDelegate(t, map[string]any{"enabled": true, "runners": map[string]any{"sleepy": fakeRunnerConfig(t, "sleep")}})
	started := object(t, tool(t, client, "delegate.run", map[string]any{"runner": "sleepy", "prompt": "x", "wait_ms": 0}))
	taskID := started["task_id"]

	rewriteConfigFile(t, path, func(agent map[string]any) { agent["delegate"] = map[string]any{"enabled": false} })
	waitUntil(t, func() bool { return len(delegateNames(listedToolNames(t, client))) == 0 })
	rewriteConfigFile(t, path, func(agent map[string]any) {
		agent["delegate"] = map[string]any{"enabled": true, "runners": map[string]any{"sleepy": fakeRunnerConfig(t, "sleep")}}
	})
	waitUntil(t, func() bool { return len(delegateNames(listedToolNames(t, client))) == 5 })
	eventually(t, "the running task to be cancelled by the switch", func() bool {
		return object(t, tool(t, client, "delegate.output", map[string]any{"task_id": taskID}))["state"] == "cancelled"
	})
}
