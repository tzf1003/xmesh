package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/pty"
)

// contractTools is the tool surface of docs/design/05-tools.md and the CTR-*
// contracts: required parameters, and every parameter the contract names.
// The fs tools additionally accept `cwd`, the base directory that CTR-CWD-01
// defines for relative paths.
var contractTools = map[string]struct{ required, params []string }{
	"sys.info":        {nil, nil},
	"exec.run":        {[]string{"cmd"}, []string{"cmd", "args", "cwd", "env", "stdin", "timeout", "mode", "idempotency_key"}},
	"exec.output":     {[]string{"output_ref"}, []string{"output_ref", "offset", "limit", "stream"}},
	"pty.open":        {[]string{"cols", "rows"}, []string{"shell", "cwd", "env", "cols", "rows", "name"}},
	"pty.write":       {[]string{"session_id", "data"}, []string{"session_id", "data"}},
	"pty.read":        {[]string{"session_id"}, []string{"session_id", "cursor", "max_bytes", "wait_ms"}},
	"pty.resize":      {[]string{"session_id", "cols", "rows"}, []string{"session_id", "cols", "rows"}},
	"pty.signal":      {[]string{"session_id", "signal"}, []string{"session_id", "signal"}},
	"pty.transfer":    {[]string{"session_id", "to_caller"}, []string{"session_id", "to_caller"}},
	"pty.close":       {[]string{"session_id"}, []string{"session_id"}},
	"pty.list":        {nil, nil},
	"fs.read":         {[]string{"path"}, []string{"path", "offset", "limit"}},
	"fs.write":        {[]string{"path", "content"}, []string{"path", "content", "expected_sha256"}},
	"fs.edit":         {[]string{"path", "old_string", "new_string"}, []string{"path", "old_string", "new_string", "replace_all", "expected_sha256"}},
	"fs.glob":         {[]string{"pattern"}, []string{"pattern", "path", "limit", "include_ignored"}},
	"fs.grep":         {[]string{"pattern", "output_mode"}, []string{"pattern", "path", "glob", "type", "output_mode", "context", "ignore_case", "multiline", "head_limit"}},
	"fs.list":         {[]string{"path"}, []string{"path", "depth", "show_hidden"}},
	"fs.stat":         {[]string{"path"}, []string{"path"}},
	"fs.sha256":       {[]string{"path"}, []string{"path"}},
	"fs.mkdir":        {[]string{"path"}, []string{"path", "parents"}},
	"fs.move":         {[]string{"from", "to"}, []string{"from", "to", "overwrite"}},
	"fs.delete":       {[]string{"path"}, []string{"path", "recursive"}},
	"backend.acquire": {[]string{"name", "ttl"}, []string{"name", "ttl"}},
	"backend.renew":   {[]string{"name", "ttl", "_lease_token"}, []string{"name", "ttl", "_lease_token"}},
	"backend.release": {[]string{"name", "_lease_token"}, []string{"name", "_lease_token"}},
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// The Agent offers exactly the contract's 25 tools (sys, exec, pty, fs and the
// three built-in lease tools), no tool for creating or enumerating worktrees
// or workspaces, and each tool's parameters follow the contract. sys.info's
// workspaces are the configured aliases, not a scan of the disk.
// conformance: CON-ARCH-06
func Test_CONARCH06_NoWorktreeTools_SurfaceMatchesContract(t *testing.T) {
	_, url, root := conformanceAgent(t, func(agent map[string]any, root string) {
		agent["workspaces"] = map[string]any{"alpha": map[string]any{"path": root, "desc": "configured"}}
	})
	// A checkout that a disk scan would find, but that is not configured.
	if err := os.MkdirAll(filepath.Join(root, "found-by-scan", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	client := connect(t, url)
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range list.Tools {
		seen[tool.Name] = true
		lowered := strings.ToLower(tool.Name + " " + tool.Description)
		if strings.Contains(lowered, "worktree") || strings.Contains(lowered, "workspace") {
			t.Errorf("tool %s manages workspaces or worktrees: %q", tool.Name, tool.Description)
		}
		want, ok := contractTools[tool.Name]
		if !ok {
			t.Errorf("tool %s is not in the contract", tool.Name)
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Required   []string       `json:"required"`
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(tool.Name, err)
		}
		if !reflect.DeepEqual(sorted(schema.Required), sorted(want.required)) {
			t.Errorf("%s required = %v, contract %v", tool.Name, schema.Required, want.required)
		}
		props := []string{}
		for name := range schema.Properties {
			props = append(props, name)
		}
		allowed := append([]string{}, want.params...)
		if strings.HasPrefix(tool.Name, "fs.") {
			allowed = append(allowed, "cwd")
		}
		if !reflect.DeepEqual(sorted(props), sorted(allowed)) {
			t.Errorf("%s parameters = %v, contract %v", tool.Name, sorted(props), sorted(allowed))
		}
	}
	for name := range contractTools {
		if !seen[name] {
			t.Errorf("contract tool %s is not offered", name)
		}
	}
	if len(list.Tools) != len(contractTools) || len(list.Tools) != 25 {
		t.Errorf("%d tools offered, contract has %d", len(list.Tools), len(contractTools))
	}
	// Calling an invented management tool is a protocol error, never a success.
	for _, name := range []string{"worktree.create", "worktree.list", "workspace.list", "workspace.create", "git.worktree"} {
		if out, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}}); err == nil && !out.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	info := object(t, tool(t, client, "sys.info", map[string]any{}))
	rows := info["workspaces"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["alias"] != "alpha" || rows[0].(map[string]any)["path"] != root {
		t.Errorf("workspaces must be exactly the configured aliases: %v", rows)
	}
}

// sys.info reports each field as CTR-SYS-01 defines it, compared one by one
// with the loaded configuration (node.name, workspaces, backends, limits), the
// platform (os, arch, path_sep, home, shells) and the live PTY/exec counters.
// conformance: CTR-SYS-01
func Test_CTRSYS01_FieldsMatchContractAndConfiguration(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := []string{bin, "-test.run=TestConformanceBackendHelper", "--", "--xmesh-conformance-backend-helper"}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	a, url, root := conformanceAgent(t, func(agent map[string]any, root string) {
		agent["workspaces"] = map[string]any{
			"zeta":    map[string]any{"path": root, "desc": "last alias"},
			"alpha":   map[string]any{"path": filepath.Join(root, "alpha"), "desc": "first alias"},
			"missing": map[string]any{"path": filepath.Join(root, "does-not-exist")},
		}
		if err := os.Mkdir(filepath.Join(root, "alpha"), 0o700); err != nil {
			t.Fatal(err)
		}
		agent["exec"] = map[string]any{"max_concurrent": 3, "exclusive_slots": 2}
		agent["pty"] = map[string]any{"default_shell": "auto", "max_sessions": 6, "max_per_caller": 4, "closed_retain_max": 7}
		agent["authz"] = map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"visible"}}}
		agent["backends"] = map[string]any{
			"visible": map[string]any{"transport": "stdio", "command": command, "policy": "exclusive", "favorite": true},
			"hidden":  map[string]any{"transport": "stdio", "command": command, "policy": "shared"},
		}
	})
	client := connect(t, url)
	info := object(t, tool(t, client, "sys.info", map[string]any{}))

	if info["os"] != runtime.GOOS || info["arch"] != runtime.GOARCH {
		t.Errorf("os/arch = %v/%v", info["os"], info["arch"])
	}
	if info["hostname"] != "test-node" {
		t.Errorf("hostname must be the configured node.name, got %v", info["hostname"])
	}
	if osHost, _ := os.Hostname(); osHost != "test-node" && info["hostname"] == osHost {
		t.Error("hostname is the operating system name")
	}
	wantHome, _ := os.UserHomeDir()
	if info["home"] != wantHome {
		t.Errorf("home = %v, want %v", info["home"], wantHome)
	}
	if info["path_sep"] != string(os.PathSeparator) || len(info["path_sep"].(string)) != 1 {
		t.Errorf("path_sep = %q", info["path_sep"])
	}
	wantShell, shellErr := pty.DefaultShell("auto")
	shells, _ := info["shells"].([]any)
	if shellErr != nil {
		if len(shells) != 0 {
			t.Errorf("no shell resolves but shells = %v", shells)
		}
	} else if len(shells) == 0 || shells[0] != wantShell {
		t.Errorf("shells = %v, first item must be the auto resolution %q", shells, wantShell)
	}

	// workspaces: one row per configured alias, paths resolved, failures kept with error.
	rows := info["workspaces"].([]any)
	byAlias := map[string]map[string]any{}
	for _, row := range rows {
		m := row.(map[string]any)
		byAlias[m["alias"].(string)] = m
	}
	if len(rows) != 3 || len(byAlias) != 3 {
		t.Fatalf("workspaces = %v", rows)
	}
	if byAlias["zeta"]["path"] != root || byAlias["zeta"]["desc"] != "last alias" {
		t.Errorf("zeta = %v", byAlias["zeta"])
	}
	if byAlias["alpha"]["path"] != filepath.Join(root, "alpha") || byAlias["alpha"]["desc"] != "first alias" {
		t.Errorf("alpha = %v", byAlias["alpha"])
	}
	if _, hasPath := byAlias["missing"]["path"]; hasPath || byAlias["missing"]["error"] == nil {
		t.Errorf("an alias that fails to resolve is listed with error and no path: %v", byAlias["missing"])
	}

	// backends: filtered by authz.backends; carry policy, favorite and a legal state.
	backends := info["backends"].([]any)
	if len(backends) != 1 {
		t.Fatalf("backends = %v (a backend the caller may not use must not be listed)", backends)
	}
	visible := backends[0].(map[string]any)
	if visible["name"] != "visible" || visible["policy"] != "exclusive" || visible["favorite"] != true {
		t.Errorf("backend = %v", visible)
	}
	if state := visible["state"]; state != "stopped" && state != "running" && state != "crashed" {
		t.Errorf("backend state %v is not one of stopped/running/crashed", state)
	}

	// limits: configuration values and live counters.
	limits := func() map[string]any {
		return object(t, tool(t, client, "sys.info", map[string]any{}))["limits"].(map[string]any)
	}
	want := map[string]float64{"exec_shared": 3, "exec_exclusive": 2, "pty_max_sessions": 6, "pty_max_per_caller": 4, "pty_closed_retain_max": 7, "pty_in_use": 0, "pty_active": 0, "pty_closed": 0}
	for key, value := range want {
		if limits()[key] != value {
			t.Errorf("limits.%s = %v, want %v", key, limits()[key], value)
		}
	}
	if runtime.GOOS != "windows" {
		for i := 0; i < 3; i++ {
			if out := tool(t, client, "pty.open", map[string]any{"cols": 80, "rows": 24, "shell": "/bin/sh"}); out.IsError {
				t.Fatal(object(t, out))
			}
		}
		// A second MCP session whose terminal is orphaned when it disconnects.
		other := connect(t, url)
		orphan := object(t, tool(t, other, "pty.open", map[string]any{"cols": 80, "rows": 24, "shell": "/bin/sh"}))
		if err := other.Close(); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the disconnected session's terminal to be orphaned", func() bool {
			return limits()["pty_in_use"] == float64(4) && limits()["pty_active"] == float64(3)
		})
		// A terminal whose shell exits becomes closed: kept, but in neither count.
		if out := tool(t, client, "pty.write", map[string]any{"session_id": firstActive(t, client), "data": "exit\n"}); out.IsError {
			t.Fatal(object(t, out))
		}
		eventually(t, "the exited terminal to be closed", func() bool {
			l := limits()
			return l["pty_closed"] == float64(1) && l["pty_in_use"] == float64(3) && l["pty_active"] == float64(2)
		})
		_ = orphan
	}

	// The numbers follow the current configuration, not a historical maximum.
	body, _ := os.ReadFile(filepath.Join(root, "config.json"))
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	doc["agent"].(map[string]any)["pty"].(map[string]any)["max_sessions"] = 5
	body, _ = json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(root, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "limits to follow the reloaded configuration", func() bool { return limits()["pty_max_sessions"] == float64(5) })
	_ = a
}
