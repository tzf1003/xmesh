package onboarding

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const testClientEndpoint = "http://127.0.0.1:48291/mcp"

func isolatedClientPaths(t *testing.T) map[string]string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", filepath.Join(root, "配置 'quoted'", "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "配置 'quoted'", "claude-code"))
	paths, err := currentClientPaths()
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func putClientConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func readClientConfig(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func oneClientPlan(t *testing.T, id string) ClientChange {
	t.Helper()
	plans, err := PlanClients([]string{id}, testClientEndpoint)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plan %s: %v", id, err)
	}
	return plans[0]
}

func TestClientPaths(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "alice")
	paths := ClientPaths(home, func(string) string { return "" })
	if paths["codex"] != filepath.Join(home, ".codex", "config.toml") || paths["claude-code"] != filepath.Join(home, ".claude.json") {
		t.Fatalf("default paths: %v", paths)
	}
	env := map[string]string{"CODEX_HOME": filepath.Join(home, "codex custom"), "CLAUDE_CONFIG_DIR": filepath.Join(home, "claude custom")}
	paths = ClientPaths(home, func(key string) string { return env[key] })
	if paths["codex"] != filepath.Join(env["CODEX_HOME"], "config.toml") || paths["claude-code"] != filepath.Join(env["CLAUDE_CONFIG_DIR"], ".claude.json") {
		t.Fatalf("override paths: %v", paths)
	}
}

func TestClientNativeTemporaryDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude-code"))
	change := oneClientPlan(t, "codex")
	receipt, err := ApplyClient(change, testClientEndpoint, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := RollbackClient(*receipt); err != nil {
		t.Fatal(err)
	}
}

func TestClientMergePreservesConfigurationsAndRollback(t *testing.T) {
	paths := isolatedClientPaths(t)
	originals := map[string]string{
		"codex":       "# Personal settings\nmodel = 'model-name' # chosen model\n\n[mcp_servers.other]\ncommand = '/Applications/路径 with spaces/tool' # keep this comment\nargs = ['--option', '\"quoted\"']\n\n[profiles.work]\nmodel = 'work-model'\n",
		"claude-code": `{"numStartups":900719925474099312345,"projects":{"/项目 'quoted'":{"allowedTools":["Read"]}},"mcpServers":{"other":{"command":"/path with spaces/tool","args":["--flag"],"env":{"KEY":"other-secret"}}}}`,
	}
	for _, id := range []string{"codex", "claude-code"} {
		t.Run(id, func(t *testing.T) {
			path := paths[id]
			putClientConfig(t, path, originals[id])
			change := oneClientPlan(t, id)
			if !change.Exists || change.Conflict {
				t.Fatalf("preview flags: %+v", change)
			}
			secret := "test-secret-凭据-'\"\\"
			receipt, err := ApplyClient(change, testClientEndpoint, secret)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.BackupPath == "" || !bytes.Equal(readClientConfig(t, receipt.BackupPath), []byte(originals[id])) {
				t.Fatal("backup differs from original configuration")
			}
			if err := verifyPrivateClientFile(receipt.BackupPath); err != nil {
				t.Fatal(err)
			}
			if err := VerifyClient(*receipt, testClientEndpoint); err != nil {
				t.Fatal(err)
			}
			stored := readClientConfig(t, path)
			repeated, err := mergeClientConfig(id, stored, true, testClientEndpoint, secret)
			if err != nil || !bytes.Equal(stored, repeated) {
				t.Fatal("repeated merge changed an already matching configuration")
			}
			if id == "codex" {
				if !bytes.HasPrefix(stored, []byte(originals[id])) {
					t.Fatal("unrelated TOML bytes changed")
				}
			} else {
				before, _ := parseClientJSON([]byte(originals[id]))
				after, _ := parseClientJSON(stored)
				if string(after["numStartups"]) != string(before["numStartups"]) || !reflect.DeepEqual(compactClientJSON(t, after["projects"]), compactClientJSON(t, before["projects"])) {
					t.Fatal("unrelated JSON configuration changed")
				}
				beforeServers, _ := jsonServerMap(before)
				afterServers, _ := jsonServerMap(after)
				if !bytes.Equal(compactClientJSON(t, beforeServers["other"]), compactClientJSON(t, afterServers["other"])) {
					t.Fatal("other MCP server changed")
				}
			}
			preview, err := json.Marshal(oneClientPlan(t, id))
			if err != nil || strings.Contains(string(preview), secret) || strings.Contains(string(preview), "other-secret") || strings.Contains(string(preview), "Bearer ") {
				t.Fatal("preview contains configuration credentials")
			}
			// Receipts survive private transaction-journal serialization.
			encoded, _ := json.Marshal(receipt)
			var durable ClientReceipt
			if err := json.Unmarshal(encoded, &durable); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				if err := RollbackClient(durable); err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(readClientConfig(t, path), []byte(originals[id])) {
				t.Fatal("rollback did not restore exact original bytes")
			}
		})
	}
}

func compactClientJSON(t *testing.T, data []byte) []byte {
	t.Helper()
	var result bytes.Buffer
	if err := json.Compact(&result, data); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func TestCodexMergeSyntaxForms(t *testing.T) {
	cases := map[string]string{
		"table":                 "# keep\nmodel='m'\n[mcp_servers.xmesh]\ncommand='old'\nargs=['start']\n[mcp_servers.other]\ncommand='other' # preserve\n[mcp_servers.xmesh.env]\nKEY='private'\n[profile]\nvalue=1\n",
		"quoted":                "[\"mcp_servers\".'xmesh'] # old entry\nurl='http://localhost:1'\nhttp_headers={Authorization='old'}\n[mcp_servers.other]\ncommand='other'\n",
		"dotted":                "model='m'\nmcp_servers.xmesh.command='old'\nmcp_servers.xmesh.args=['a','b']\nmcp_servers.other.command='other' # preserve\n",
		"nested-inline":         "# personal\n[mcp_servers]\nxmesh={command='old',env={KEY='value'}}\nother={command='other',args=['a']} # preserve\n",
		"root-inline":           "# personal settings\nmodel='m' # preserve\nmcp_servers={xmesh={command='old'},other={command='other',args=['a'],when=1979-05-27T07:32:00Z}}\n[profile]\nvalue=1\n",
		"multiline":             "model='m'\n[mcp_servers.xmesh]\ncommand='''\nold multiline\n[mcp_servers.spoof]\n'''\nargs=[\n 'a', # inside array\n 'b'\n]\n[mcp_servers.other]\ncommand='other' # preserve\n",
		"root-inline-empty":     "mcp_servers={}\n# keep final comment\n",
		"empty":                 "",
		"parent-table":          "[mcp_servers]\n# servers\n[mcp_servers.other]\ncommand='other'\n",
		"quoted-unrelated-name": "[mcp_servers.'xmesh.other']\ncommand='separate-name'\n",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := mergeClientConfig("codex", []byte(source), true, testClientEndpoint, "credential-'\"\\-\u2028")
			if err != nil {
				t.Fatal(err)
			}
			var before, after map[string]any
			if err := toml.Unmarshal([]byte(source), &before); err != nil {
				t.Fatal(err)
			}
			if err := toml.Unmarshal(result, &after); err != nil {
				t.Fatal(err)
			}
			servers := after["mcp_servers"].(map[string]any)
			if !clientEntryMatches("codex", servers["xmesh"].(map[string]any), testClientEndpoint) {
				t.Fatal("entry verification failed")
			}
			delete(servers, "xmesh")
			if oldServers, ok := before["mcp_servers"].(map[string]any); ok {
				delete(oldServers, "xmesh")
			} else {
				before["mcp_servers"] = map[string]any{}
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("other parsed values changed: before=%#v after=%#v", before, after)
			}
			if strings.Contains(source, "# preserve") && !bytes.Contains(result, []byte("# preserve")) {
				t.Fatal("unrelated comment was lost")
			}
		})
	}
}

func TestClientPreviewConflict(t *testing.T) {
	paths := isolatedClientPaths(t)
	for _, test := range []struct {
		id, source string
	}{
		{"codex", "[mcp_servers.xmesh]\ncommand='old'\nenv={SECRET='stored-secret'}\n"},
		{"claude-code", `{"mcpServers":{"xmesh":{"command":"old","env":{"SECRET":"stored-secret"}}}}`},
		{"claude-code", `{"mcpServers":{"xmesh":null}}`},
		{"claude-code", `{"mcpServers":{"xmesh":5}}`},
	} {
		putClientConfig(t, paths[test.id], test.source)
		change := oneClientPlan(t, test.id)
		if !change.Conflict {
			t.Fatal("existing different entry must produce a conflict preview")
		}
		preview, _ := json.Marshal(change)
		if bytes.Contains(preview, []byte("stored-secret")) {
			t.Fatal("preview contains an existing credential")
		}
		receipt, err := ApplyClient(change, testClientEndpoint, "new-secret")
		if err != nil {
			t.Fatal(err)
		}
		if oneClientPlan(t, test.id).Conflict {
			t.Fatal("installed matching entry should be recognized")
		}
		if err := RollbackClient(*receipt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientMalformedConfigStopsBeforeMutation(t *testing.T) {
	paths := isolatedClientPaths(t)
	tests := []struct{ id, source string }{
		{"codex", "[mcp_servers\nurl='private-literal'"},
		{"codex", "model='first'\nmodel='private-literal'\n"},
		{"codex", "mcp_servers='private-literal'\n"},
		{"claude-code", `{"mcpServers":{"xmesh":"private-literal"}`},
		{"claude-code", `{"mcpServers":{},"mcpServers":{"xmesh":"private-literal"}}`},
		{"claude-code", `{"other":{"key":1,"key":"private-literal"}}`},
		{"claude-code", `{"mcpServers":null}`},
		{"claude-code", `{"mcpServers":[]}`},
		{"claude-code", `[]`},
		{"claude-code", `null`},
		{"claude-code", `{}` + "\n{}"},
		{"claude-code", ""},
	}
	for _, test := range tests {
		putClientConfig(t, paths[test.id], test.source)
		_, err := PlanClients([]string{test.id}, testClientEndpoint)
		if err == nil {
			t.Fatalf("malformed %s configuration accepted", test.id)
		}
		if strings.Contains(err.Error(), "private-literal") {
			t.Fatal("parser error exposed input content")
		}
		if string(readClientConfig(t, paths[test.id])) != test.source {
			t.Fatal("planning changed malformed configuration")
		}
		change := ClientChange{ID: test.id, Path: paths[test.id], Exists: true, BeforeHash: hashClientFile([]byte(test.source), true)}
		if receipt, err := ApplyClient(change, testClientEndpoint, "new-secret"); err == nil || receipt != nil {
			t.Fatal("malformed configuration was applied")
		}
		backups, err := filepath.Glob(paths[test.id] + ".xmesh-backup-*")
		if err != nil || len(backups) != 0 {
			t.Fatal("malformed configuration created a backup")
		}
	}
}

func TestClientCASAndRollbackCAS(t *testing.T) {
	paths := isolatedClientPaths(t)
	for _, id := range []string{"codex", "claude-code"} {
		original := "model='original'\n"
		changed := "model='editor-change'\n"
		if id == "claude-code" {
			original, changed = `{"personal":"original"}`, `{"personal":"editor-change"}`
		}
		putClientConfig(t, paths[id], original)
		change := oneClientPlan(t, id)
		putClientConfig(t, paths[id], changed)
		if receipt, err := ApplyClient(change, testClientEndpoint, "secret"); err == nil || receipt != nil {
			t.Fatal("stale preview was applied")
		}
		if string(readClientConfig(t, paths[id])) != changed {
			t.Fatal("editor change was overwritten")
		}
		change = oneClientPlan(t, id)
		receipt, err := ApplyClient(change, testClientEndpoint, "secret")
		if err != nil {
			t.Fatal(err)
		}
		putClientConfig(t, paths[id], original)
		if err := RollbackClient(*receipt); err == nil {
			t.Fatal("rollback overwrote an edited client file")
		}
		if string(readClientConfig(t, paths[id])) != original {
			t.Fatal("rollback changed editor content")
		}
	}
}

func TestConcurrentClientApply(t *testing.T) {
	paths := isolatedClientPaths(t)
	change := oneClientPlan(t, "codex")
	var wait sync.WaitGroup
	type outcome struct {
		receipt *ClientReceipt
		err     error
	}
	results := make(chan outcome, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			r, err := ApplyClient(change, testClientEndpoint, "secret")
			results <- outcome{r, err}
		}()
	}
	wait.Wait()
	close(results)
	var successes, failures int
	for result := range results {
		if result.err == nil {
			successes++
			if err := VerifyClient(*result.receipt, testClientEndpoint); err != nil {
				t.Fatal(err)
			}
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("CAS outcomes: %d successful, %d failed", successes, failures)
	}
	if _, exists, err := readClientFile(paths["codex"]); err != nil || !exists {
		t.Fatal("installed configuration unavailable")
	}
}

func TestCreatedClientRollbackRetriesAndBackupAuthentication(t *testing.T) {
	paths := isolatedClientPaths(t)
	for _, id := range []string{"codex", "claude-code"} {
		change := oneClientPlan(t, id)
		receipt, err := ApplyClient(change, testClientEndpoint, "secret")
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Existed || receipt.BackupPath != "" {
			t.Fatal("new file receipt has an original file")
		}
		for range 3 {
			if err := RollbackClient(*receipt); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := os.Stat(paths[id]); !os.IsNotExist(err) {
			t.Fatal("new configuration remains after rollback")
		}
	}
	putClientConfig(t, paths["claude-code"], `{"original":true}`)
	receipt, err := ApplyClient(oneClientPlan(t, "claude-code"), testClientEndpoint, "secret")
	if err != nil {
		t.Fatal(err)
	}
	installed := readClientConfig(t, paths["claude-code"])
	putClientConfig(t, receipt.BackupPath, `{"tampered":true}`)
	if err := RollbackClient(*receipt); err == nil {
		t.Fatal("tampered backup was restored")
	}
	if !bytes.Equal(readClientConfig(t, paths["claude-code"]), installed) {
		t.Fatal("failed backup authentication changed configuration")
	}
}

func TestClientPreparedReceiptPrecedesReplacement(t *testing.T) {
	paths := isolatedClientPaths(t)
	for _, id := range []string{"codex", "claude-code"} {
		original := "model='before'\n"
		if id == "claude-code" {
			original = `{"personal":"before"}`
		}
		putClientConfig(t, paths[id], original)
		change := oneClientPlan(t, id)
		var journalReceipt ClientReceipt
		called := 0
		receipt, err := ApplyClientPrepared(change, testClientEndpoint, "private-token", func(prepared ClientReceipt) error {
			called++
			if string(readClientConfig(t, paths[id])) != original {
				t.Fatal("replacement preceded durable recovery callback")
			}
			if string(readClientConfig(t, prepared.BackupPath)) != original {
				t.Fatal("backup unavailable during recovery callback")
			}
			encoded, _ := json.Marshal(prepared)
			if bytes.Contains(encoded, []byte("private-token")) {
				t.Fatal("receipt contains a credential")
			}
			if err := json.Unmarshal(encoded, &journalReceipt); err != nil {
				t.Fatal(err)
			}
			return errors.New("simulated-journal-failure-private-token")
		})
		if called != 1 || receipt != nil || err == nil || strings.Contains(err.Error(), "private-token") {
			t.Fatal("preparation failure did not stop replacement safely")
		}
		if string(readClientConfig(t, paths[id])) != original {
			t.Fatal("journal failure changed client configuration")
		}
		// Restart recovery with a durable intent and an untouched original file.
		if err := RollbackClient(journalReceipt); err != nil {
			t.Fatal(err)
		}
		receipt, err = ApplyClientPrepared(change, testClientEndpoint, "private-token", func(prepared ClientReceipt) error {
			journalReceipt = prepared
			return nil
		})
		if err != nil || receipt == nil || *receipt != journalReceipt {
			t.Fatal("successful replacement differs from its durable intent")
		}
		// Restart recovery after the exact intended bytes reached the client file.
		for range 3 {
			if err := RollbackClient(journalReceipt); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestInspectClientVersionOutputIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test executables use the Unix script format")
	}
	root := t.TempDir()
	t.Setenv("PATH", root)
	for name, content := range map[string]string{
		"codex":  "#!/bin/sh\necho 'codex-cli 1.2.3 private-version-output'\n",
		"claude": "#!/bin/sh\necho '2.1.300 (Claude Code) private-version-output'\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	clients := InspectClients()
	if len(clients) != 2 || clients[0].ID != "codex" || clients[0].Version != "1.2.3" || clients[1].ID != "claude-code" || clients[1].Version != "2.1.300" {
		t.Fatal("version discovery did not recognize installed clients")
	}
	encoded, _ := json.Marshal(clients)
	if bytes.Contains(encoded, []byte("private-version-output")) {
		t.Fatal("version discovery exposed command output")
	}
	if err := os.Remove(filepath.Join(root, "claude")); err != nil {
		t.Fatal(err)
	}
	clients = InspectClients()
	if clients[1].Installed || clients[1].Problem == "" {
		t.Fatal("missing executable was not reported")
	}
}

func TestClientPathSafetyAndVerification(t *testing.T) {
	paths := isolatedClientPaths(t)
	change := oneClientPlan(t, "codex")
	wrong := change
	wrong.Path = paths["claude-code"]
	if _, err := ApplyClient(wrong, testClientEndpoint, "secret"); err == nil {
		t.Fatal("client path substitution accepted")
	}
	for _, endpoint := range []string{"http://example.com/mcp", "http://secret@localhost/mcp", "file:///tmp/mcp", "http://localhost/mcp?secret=value"} {
		if _, err := PlanClients([]string{"codex"}, endpoint); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
	receipt, err := ApplyClient(change, testClientEndpoint, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyClient(*receipt, "http://localhost:2/mcp"); err == nil {
		t.Fatal("wrong endpoint verified")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(paths["codex"], 0644); err != nil {
			t.Fatal(err)
		}
		if err := VerifyClient(*receipt, testClientEndpoint); err == nil {
			t.Fatal("public permissions verified")
		}
	}
	if runtime.GOOS == "windows" {
		return // Creating symbolic links requires an additional Windows capability.
	}
	if err := os.Remove(paths["codex"]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(paths["claude-code"], paths["codex"]); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanClients([]string{"codex"}, testClientEndpoint); err == nil {
		t.Fatal("symlink configuration accepted")
	}
}
