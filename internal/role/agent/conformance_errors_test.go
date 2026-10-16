package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tzf1003/xmesh/docs/contracts/schemas"
)

func errorSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	body, err := schemas.Files.ReadFile("error.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err = json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("urn:xmesh:error", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:xmesh:error")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// requireEnvelope checks RUL-08 for one failed call: isError, structuredContent
// that validates against error.schema.json, and one text content item holding
// the same object as compact JSON. It returns the error object.
func requireEnvelope(t *testing.T, schema *jsonschema.Schema, label, wantCode string, out *mcp.CallToolResult) map[string]any {
	t.Helper()
	if !out.IsError {
		t.Errorf("%s: isError is false: %v", label, out.StructuredContent)
		return nil
	}
	v := object(t, out)
	if v["error"] != wantCode {
		t.Errorf("%s: error = %v, want %s: %v", label, v["error"], wantCode, v)
		return v
	}
	var normalized any
	raw, _ := json.Marshal(out.StructuredContent)
	_ = json.Unmarshal(raw, &normalized)
	if err := schema.Validate(normalized); err != nil {
		t.Errorf("%s: structuredContent violates error.schema.json: %v\n%s", label, err, raw)
	}
	message, _ := v["message"].(string)
	if message == "" || strings.ContainsAny(message, "\r\n") {
		t.Errorf("%s: message must be one non-empty line: %q", label, message)
	}
	if len(out.Content) != 1 {
		t.Errorf("%s: want exactly one content item, got %d", label, len(out.Content))
		return v
	}
	text, ok := out.Content[0].(*mcp.TextContent)
	if !ok {
		t.Errorf("%s: content item is %T, want text", label, out.Content[0])
		return v
	}
	var fromText map[string]any
	if err := json.Unmarshal([]byte(text.Text), &fromText); err != nil || strings.Contains(text.Text, "\n") {
		t.Errorf("%s: text content is not compact JSON: %q (%v)", label, text.Text, err)
	}
	if string(mustJSON(fromText)) != string(mustJSON(v)) {
		t.Errorf("%s: text content differs from structuredContent:\n%s\n%s", label, mustJSON(fromText), mustJSON(v))
	}
	return v
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// Failures use the CTR-ERR-01 object and the RUL-08 envelope: isError, a
// structuredContent object that passes error.schema.json (so each code carries
// its required extra fields), and a compact JSON text twin. The tool-level
// error is an MCP result, not a transport error. A non-zero exit code is not a
// failure. Successful results carry structuredContent and its text twin too.
// conformance: CTR-ERR-01 RUL-08
func Test_CTRERR01_EnvelopeAndSchema(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("failures are provoked with POSIX shell commands")
	}
	schema := errorSchema(t)
	bin, _ := os.Executable()
	locked := []string{bin, "-test.run=TestConformanceBackendHelper", "--", "--xmesh-conformance-backend-helper"}
	crashing := []string{bin, "-test.run=TestAgentBackendProcess", "--", "--xmesh-agent-backend-helper"}
	_, url, root := conformanceAgent(t, func(agent map[string]any, root string) {
		agent["pty"] = map[string]any{"default_shell": "/bin/sh", "max_sessions": 1}
		agent["exec"] = map[string]any{"max_concurrent": 1}
		agent["backends"] = map[string]any{
			"locked": map[string]any{"transport": "stdio", "command": locked, "policy": "exclusive"},
			"ida":    map[string]any{"transport": "stdio", "command": crashing, "policy": "exclusive"},
		}
	})
	owner, other := connect(t, url), connect(t, url)

	// A success has both representations as well.
	ok := tool(t, owner, "fs.write", map[string]any{"path": "a.txt", "content": "one\ntwo\none\n"})
	if ok.IsError || ok.StructuredContent == nil || len(ok.Content) != 1 {
		t.Fatalf("success envelope: %+v", ok)
	}
	var twin map[string]any
	if text, isText := ok.Content[0].(*mcp.TextContent); !isText || json.Unmarshal([]byte(text.Text), &twin) != nil || string(mustJSON(twin)) != string(mustJSON(object(t, ok))) {
		t.Errorf("success text content is not the JSON twin of structuredContent: %+v", ok.Content[0])
	}
	hash := object(t, ok)["sha256"]
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	huge, err := os.Create(filepath.Join(root, "huge.dat"))
	if err != nil {
		t.Fatal(err)
	}
	_ = huge.Truncate(10<<20 + 1)
	_ = huge.Close()
	if err = os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}

	nonzero := tool(t, owner, "exec.run", map[string]any{"cmd": "sh", "args": []string{"-c", "exit 3"}})
	if nonzero.IsError || object(t, nonzero)["exit_code"] != float64(3) {
		t.Errorf("a non-zero exit code is a successful result: %v", object(t, nonzero))
	}

	opened := tool(t, owner, "pty.open", map[string]any{"cols": 80, "rows": 24})
	if opened.IsError {
		t.Fatal(object(t, opened))
	}
	id := object(t, opened)["session_id"]

	// The exec slot is taken by a command that waits for a file.
	release := filepath.Join(root, "release-exec")
	running := startBlocked(owner, "exec.run", map[string]any{"cmd": "sh", "args": []string{"-c", "while [ ! -f release-exec ]; do sleep 0.02; done"}, "timeout": "50s"})
	waitFor(t, "the blocking command to hold the slot", func() bool {
		return object(t, tool(t, owner, "sys.info", map[string]any{}))["limits"].(map[string]any)["exec_shared_in_use"] == float64(1)
	})
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("x"), 0o600) })

	grant := tool(t, owner, "backend.acquire", map[string]any{"name": "locked", "ttl": "1m"})
	if grant.IsError {
		t.Fatal(object(t, grant))
	}
	idaGrant := tool(t, owner, "backend.acquire", map[string]any{"name": "ida", "ttl": "1m"})
	if idaGrant.IsError {
		t.Fatal(object(t, idaGrant))
	}

	cases := []struct {
		label, code string
		call        func() *mcp.CallToolResult
		check       func(map[string]any)
	}{
		{"invalid_args", "invalid_args", func() *mcp.CallToolResult { return tool(t, owner, "fs.write", map[string]any{"path": "x"}) }, nil},
		{"invalid_cwd", "invalid_cwd", func() *mcp.CallToolResult { return tool(t, owner, "fs.read", map[string]any{"path": "absent.txt"}) }, func(v map[string]any) {
			if v["reason"] != "not_found" {
				t.Errorf("reason = %v", v["reason"])
			}
		}},
		{"policy_denied (command)", "policy_denied", func() *mcp.CallToolResult {
			return tool(t, owner, "exec.run", map[string]any{"cmd": "rm", "args": []string{"-rf", "/"}})
		}, func(v map[string]any) {
			if v["rule"] != "exec.deny.rm_rf_root" {
				t.Errorf("rule = %v", v["rule"])
			}
		}},
		{"policy_denied (delete)", "policy_denied", func() *mcp.CallToolResult { return tool(t, owner, "fs.delete", map[string]any{"path": "dir"}) }, func(v map[string]any) {
			if v["rule"] != "fs.delete.recursive_default_deny" {
				t.Errorf("rule = %v", v["rule"])
			}
		}},
		{"conflict", "conflict", func() *mcp.CallToolResult {
			return tool(t, owner, "fs.write", map[string]any{"path": "a.txt", "content": "x", "expected_sha256": strings.Repeat("0", 64)})
		}, func(v map[string]any) {
			if v["current_sha256"] != hash {
				t.Errorf("current_sha256 = %v", v["current_sha256"])
			}
		}},
		{"edit_not_found", "edit_not_found", func() *mcp.CallToolResult {
			return tool(t, owner, "fs.edit", map[string]any{"path": "a.txt", "old_string": "absent", "new_string": "x"})
		}, nil},
		{"edit_ambiguous", "edit_ambiguous", func() *mcp.CallToolResult {
			return tool(t, owner, "fs.edit", map[string]any{"path": "a.txt", "old_string": "one", "new_string": "x"})
		}, func(v map[string]any) {
			if lines, _ := v["lines"].([]any); len(lines) != 2 || lines[0] != float64(1) || lines[1] != float64(3) {
				t.Errorf("lines = %v", v["lines"])
			}
		}},
		{"binary_rejected", "binary_rejected", func() *mcp.CallToolResult { return tool(t, owner, "fs.read", map[string]any{"path": "bin.dat"}) }, nil},
		{"too_large", "too_large", func() *mcp.CallToolResult { return tool(t, owner, "fs.read", map[string]any{"path": "huge.dat"}) }, nil},
		{"pty_limit", "pty_limit", func() *mcp.CallToolResult {
			return tool(t, other, "pty.open", map[string]any{"cols": 80, "rows": 24})
		}, func(v map[string]any) {
			if v["scope"] != "device" || v["limit"] != float64(1) || v["in_use"] != float64(1) {
				t.Errorf("pty_limit fields: %v", v)
			}
		}},
		{"not_owner", "not_owner", func() *mcp.CallToolResult {
			return tool(t, other, "pty.write", map[string]any{"session_id": id, "data": "x"})
		}, nil},
		{"session_not_found", "session_not_found", func() *mcp.CallToolResult {
			return tool(t, owner, "pty.read", map[string]any{"session_id": "p-00000000", "wait_ms": 0})
		}, nil},
		{"exec_saturated", "exec_saturated", func() *mcp.CallToolResult {
			return tool(t, other, "exec.run", map[string]any{"cmd": "true", "timeout": 300})
		}, nil},
		{"not_found", "not_found", func() *mcp.CallToolResult {
			return tool(t, owner, "exec.output", map[string]any{"output_ref": "o-missing"})
		}, nil},
		{"lease_required", "lease_required", func() *mcp.CallToolResult { return tool(t, other, "locked.echo", map[string]any{"x": 1}) }, nil},
		{"resource_busy (held)", "resource_busy", func() *mcp.CallToolResult {
			return tool(t, other, "backend.acquire", map[string]any{"name": "locked", "ttl": "1m"})
		}, func(v map[string]any) {
			if v["reason"] != "held" {
				t.Errorf("reason = %v", v["reason"])
			}
		}},
		{"resource_busy (token_mismatch)", "resource_busy", func() *mcp.CallToolResult {
			return tool(t, other, "locked.echo", map[string]any{"_lease_token": object(t, grant)["token"]})
		}, nil},
		{"backend_unavailable", "backend_unavailable", func() *mcp.CallToolResult {
			return tool(t, owner, "ida.crash", map[string]any{"_lease_token": object(t, idaGrant)["token"]})
		}, nil},
	}
	for _, c := range cases {
		out := c.call()
		v := requireEnvelope(t, schema, c.label, c.code, out)
		if v != nil && c.check != nil {
			c.check(v)
		}
	}
	// The timeout error needs the slot back, so it runs after the release.
	if err = os.WriteFile(release, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireFinished(t, "the blocking exec", running)
	timeout := tool(t, owner, "exec.run", map[string]any{"cmd": "sleep", "args": []string{"5"}, "timeout": 200})
	requireEnvelope(t, schema, "timeout", "timeout", timeout)

	// unauthorized: a policy reload that removes the tool from the caller's grant.
	body, _ := os.ReadFile(filepath.Join(root, "config.json"))
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	doc["agent"].(map[string]any)["authz"] = map[string]any{"tag:peer": map[string]any{"tools": []string{"sys.*"}, "backends": []string{}}}
	body, _ = json.Marshal(doc)
	if err = os.WriteFile(filepath.Join(root, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	var denied *mcp.CallToolResult
	waitFor(t, "the narrowed grant to apply", func() bool {
		denied = tool(t, owner, "fs.read", map[string]any{"path": "a.txt"})
		return denied.IsError
	})
	requireEnvelope(t, schema, "unauthorized", "unauthorized", denied)
}
