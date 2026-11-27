package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const redelegateProbeMarker = "--xmesh-redelegate-probe"

func runRedelegateProbe(args []string) {
	if len(args) < 3 || args[1] != redelegateProbeMarker {
		return
	}
	os.Exit(execRedelegateProbe(args[2:]))
}

func execRedelegateProbe(args []string) int {
	_ = os.Unsetenv("XMESH_DELEGATE")
	mode := args[0]
	endpoint := args[1]

	switch mode {
	case "mcp":
		if err := verifyMCPBlocked(endpoint); err != nil {
			fmt.Fprintln(os.Stderr, "mcp probe:", err)
			return 2
		}
		return 0
	case "full":
		// 1. Direct MCP probe from the delegated task process after unsetting XMESH_DELEGATE.
		if err := verifyMCPBlocked(endpoint); err != nil {
			fmt.Fprintln(os.Stderr, "direct mcp:", err)
			return 2
		}
		// 2. Child process spawned by the delegated task also cannot re-delegate.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		child := exec.Command(exe, redelegateProbeMarker, "mcp", endpoint)
		if out, err := child.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "child mcp: %v (%s)\n", err, out)
			return 4
		}
		// 3. Confused-deputy bounce: calling exec.run on the local agent runs a child probe; that child must also be blocked.
		if err := verifyExecBounceBlocked(endpoint, exe); err != nil {
			fmt.Fprintln(os.Stderr, "exec bounce:", err)
			return 5
		}
		// 4. Reusing a pre-created normal caller's Mcp-Session-Id must be refused with HTTP 403.
		if len(args) >= 3 && args[2] != "" {
			if err := verifySessionHijackBlocked(endpoint, args[2]); err != nil {
				fmt.Fprintln(os.Stderr, "session hijack:", err)
				return 6
			}
		}
		out, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "probe-ok"})
		_, _ = os.Stdout.Write(append(out, '\n'))
		return 0
	default:
		return 9
	}
}

func verifyMCPBlocked(endpoint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "delegated-probe", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer session.Close()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	for _, tl := range list.Tools {
		if strings.HasPrefix(tl.Name, "delegate.") {
			return fmt.Errorf("delegated caller still saw %s in tools/list", tl.Name)
		}
	}

	attempts := []struct {
		tool string
		args map[string]any
	}{
		{"delegate.list", map[string]any{}},
		{"delegate.run", map[string]any{"runner": "probe", "prompt": "loop"}},
		{"delegate.output", map[string]any{"task_id": "t-1"}},
		{"delegate.send", map[string]any{"session_id": "s-1", "message": "hi"}},
		{"delegate.cancel", map[string]any{"task_id": "t-1"}},
	}
	for _, attempt := range attempts {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: attempt.tool, Arguments: attempt.args})
		if err != nil {
			return fmt.Errorf("%s transport error: %w", attempt.tool, err)
		}
		if !res.IsError {
			return fmt.Errorf("%s succeeded inside delegated process", attempt.tool)
		}
		body := decodeToolResult(res)
		if body["error"] != "policy_denied" || body["rule"] != "delegate.redelegate_denied" {
			return fmt.Errorf("%s got error=%v rule=%v, want policy_denied/delegate.redelegate_denied", attempt.tool, body["error"], body["rule"])
		}
	}
	return nil
}

func verifyExecBounceBlocked(endpoint, exe string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "delegated-exec-probe", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return err
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "exec.run",
		Arguments: map[string]any{
			"cmd":  exe,
			"args": []string{redelegateProbeMarker, "mcp", endpoint},
		},
	})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("exec.run failed: %v", decodeToolResult(res))
	}
	body := decodeToolResult(res)
	if exitCode, _ := body["exit_code"].(float64); exitCode != 0 {
		return fmt.Errorf("exec.run child exited %v (%v)", body["exit_code"], body["stderr"])
	}
	return nil
}

func verifySessionHijackBlocked(endpoint, normalSessionID string) error {
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delegate.list","arguments":{}}}`
	req, err := http.NewRequest("POST", endpoint, bytes.NewBufferString(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", normalSessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		return fmt.Errorf("reusing non-delegated Mcp-Session-Id returned HTTP %d, want 403", resp.StatusCode)
	}
	return nil
}

func decodeToolResult(res *mcp.CallToolResult) map[string]any {
	out := map[string]any{}
	if len(res.Content) == 0 {
		return out
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		_ = json.Unmarshal([]byte(tc.Text), &out)
	}
	return out
}

// conformance: CTR-DLG-06
func Test_DelegatedSubprocessCannotRedelegateThroughLocalAgent(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, store := delegateConfig(t, map[string]any{"enabled": true})
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	srv := httptest.NewUnstartedServer(a.HTTP(Local))
	srv.Config.ConnContext = a.ConnContext()
	srv.Start()
	t.Cleanup(srv.Close)

	endpoint := srv.URL + "/mcp"
	normal := connect(t, endpoint)

	// Configure a runner that invokes our probe inside a real delegated task, with the normal session ID passed
	// in argv so the probe can also test session-hijack rejection.
	probeRunner := map[string]any{
		"cmd":        exe,
		"args":       []string{redelegateProbeMarker, "full", endpoint, normal.ID()},
		"read_args":  []string{},
		"write_args": []string{},
	}
	path := store.Path()
	rewriteConfigFile(t, path, func(agent map[string]any) {
		agent["delegate"] = map[string]any{
			"enabled": true,
			"runners": map[string]any{"probe": probeRunner},
		}
	})
	waitUntil(t, func() bool {
		out := tool(t, normal, "delegate.list", map[string]any{})
		if out.IsError {
			return false
		}
		for _, r := range object(t, out)["runners"].([]any) {
			if r.(map[string]any)["name"] == "probe" {
				return true
			}
		}
		return false
	})

	// Run the delegated task; the probe fails (non-zero) unless ALL bypass attempts were refused.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := normal.CallTool(ctx, &mcp.CallToolParams{Name: "delegate.run", Arguments: map[string]any{"runner": "probe", "prompt": "check", "wait_ms": 40000}})
	if err != nil {
		t.Fatalf("delegate.run call failed: %v", err)
	}
	if out.IsError {
		t.Fatalf("normal delegate.run failed: %v", object(t, out))
	}
	body := object(t, out)
	if body["state"] != "done" || body["result"] != "probe-ok" {
		t.Fatalf("delegated bypass probe failed: %+v", body)
	}

	// Normal caller remains unaffected and can still delegate after the task finishes.
	if names := delegateNames(listedToolNames(t, normal)); len(names) != 5 {
		t.Fatalf("normal caller lost delegate tools: %v", names)
	}
	if check := tool(t, normal, "delegate.list", map[string]any{}); check.IsError {
		t.Fatalf("normal delegate.list failed after delegated task: %v", object(t, check))
	}
}
