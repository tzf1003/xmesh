package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/api/control/v1/schemacheck"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/sandbox"
)

// stubRunner is an l2Runner that starts nothing: it reports a fixed exit code so
// a call that reached it is distinguishable from one that ran the command.
type stubRunner struct {
	mu     sync.Mutex
	starts int
	closes int
	order  *[]string
}

type stubProcess struct{}

func (stubProcess) PID() int                    { return 1 }
func (stubProcess) Wait() (int, error)          { return 5, nil }
func (stubProcess) Close(context.Context) error { return nil }

func (r *stubRunner) Start(context.Context, string, sandbox.Spec, sandbox.Streams) (sandbox.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	return stubProcess{}, nil
}

func (r *stubRunner) StartTerminal(context.Context, string, sandbox.Spec, int, int) (sandbox.Terminal, error) {
	return nil, errors.New("not used")
}

func (r *stubRunner) Close(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	return nil
}

func stubOpener(t *testing.T, open func(context.Context, string) (l2Runner, error)) {
	t.Helper()
	previous := openL2Runner
	openL2Runner = open
	t.Cleanup(func() { openL2Runner = previous })
}

func l2Config(t *testing.T, optIn bool) (*config.Store, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	policy := map[string]any{"isolation": "L2"}
	if optIn {
		policy["windows_appcontainer"] = true
	}
	doc := map[string]any{"node": map[string]any{"name": "l2-node", "roles": []string{"agent"}}, "mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}}, "agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "policy": policy, "authz": map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}}}
	b, _ := json.Marshal(doc)
	path := filepath.Join(root, "config.json")
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	store, e := config.NewStore(path)
	if e != nil {
		t.Fatal(e)
	}
	return store, root
}

// serve runs the runtime until the test ends and returns a connected client plus
// a stop function that reports the error Run returned.
func serve(t *testing.T, store *config.Store, root string) (*mcp.ClientSession, func() error) {
	t.Helper()
	addr := address(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, store, Options{Local: true, Listen: addr, StateDir: filepath.Join(root, "state")})
	}()
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case e := <-done:
			return e
		case <-time.After(15 * time.Second):
			return errors.New("runtime shutdown hung")
		}
	}
	t.Cleanup(func() { _ = stop() })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		attempt, release := context.WithTimeout(context.Background(), time.Second)
		s, e := client.Connect(attempt, &mcp.StreamableClientTransport{Endpoint: "http://" + addr + "/mcp"}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
		release()
		if e == nil {
			t.Cleanup(func() { s.Close() })
			return s, stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("agent listener not ready")
	return nil, nil
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	o, e := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatal(name, e)
	}
	b, _ := json.Marshal(o.StructuredContent)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	return v
}

func warningsOf(info map[string]any) string {
	list, _ := info["warnings"].([]any)
	var all []string
	for _, w := range list {
		all = append(all, w.(string))
	}
	return strings.Join(all, "\n")
}

func Test_Runtime_WindowsL2RequiresOptIn(t *testing.T) {
	stubOpener(t, func(context.Context, string) (l2Runner, error) {
		t.Fatal("the runner was built without the opt-in switch")
		return nil, nil
	})
	store, root := l2Config(t, false)
	runner, notes, unavailable := startSandbox(context.Background(), store.Get(), filepath.Join(root, "state"))
	if runner != nil || len(notes) != 0 || unavailable != "" {
		t.Fatal("an unconfigured node installed a runner or a note", runner, notes, unavailable)
	}
	client, _ := serve(t, store, root)
	if note := warningsOf(callTool(t, client, "sys.info", map[string]any{})); strings.Contains(note, "L2") {
		t.Fatal("sys.info reports a runner note without the switch", note)
	}
}

func Test_Runtime_WindowsL2RunnerUnavailableFailsClosed(t *testing.T) {
	var opened string
	stubOpener(t, func(_ context.Context, dir string) (l2Runner, error) {
		opened = dir
		return nil, errors.New("recovery journal is busy")
	})
	store, root := l2Config(t, true)
	client, stop := serve(t, store, root)
	if want := filepath.Join(root, "state", "sandbox"); opened != want {
		t.Fatalf("runner state directory %q, want %q", opened, want)
	}
	note := warningsOf(callTool(t, client, "sys.info", map[string]any{}))
	if !strings.Contains(note, "sandbox.unavailable") || strings.Contains(note, "busy") {
		t.Fatalf("startup note must name the fail-closed rule without leaking the cause: %q", note)
	}
	if runtime.GOOS == "windows" {
		out := callTool(t, client, "exec.run", map[string]any{"cmd": os.Args[0], "args": []string{"-test.run=^$"}})
		if out["error"] != "policy_denied" || out["rule"] != "sandbox.unavailable" {
			t.Fatal("L2 exec was not refused without a runner", out)
		}
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

const accountReason = "当前账户无法读取 NTFS 变更日志，Windows L2 需要以管理员（提权）或 LocalSystem 服务运行节点"

// A runner refused for an account-level reason is not started, and the reason
// reaches the caller, sys.info and the control status; the log-only cause does not.
func Test_Runtime_WindowsL2AccountRefusalIsExplained(t *testing.T) {
	stubOpener(t, func(context.Context, string) (l2Runner, error) {
		return nil, &sandbox.UnavailableError{Reason: accountReason, Cause: errors.New("volume device open: Access is denied.")}
	})
	store, root := l2Config(t, true)
	client, stop := serve(t, store, root)
	out := callTool(t, client, "exec.run", map[string]any{"cmd": os.Args[0], "args": []string{"-test.run=^$"}})
	if out["error"] != "policy_denied" || out["rule"] != "sandbox.unavailable" || out["reason"] != accountReason {
		t.Fatal("L2 exec was not refused with the reason", out)
	}
	note := warningsOf(callTool(t, client, "sys.info", map[string]any{}))
	if !strings.Contains(note, "sandbox.unavailable") || !strings.Contains(note, accountReason) || strings.Contains(note, "Access is denied") {
		t.Fatalf("sys.info note: %q", note)
	}
	endpoint, err := control.ReadEndpoint(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := control.NewClient(endpoint).Do(context.Background(), "GET", "/v1/status", nil, nil)
	if err != nil || reply.Status != 200 {
		t.Fatal(reply, err)
	}
	if err := schemacheck.Validate("StatusResponse", reply.Body); err != nil {
		t.Fatal(err)
	}
	var st v1.StatusResponse
	if err := json.Unmarshal(reply.Body, &st); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range st.Warnings {
		if w.Code == v1.WarnSandboxUnavailable {
			found = w.Severity == v1.SeverityWarning && strings.Contains(w.Message, accountReason) && !strings.Contains(w.Message, "Access is denied")
		}
	}
	if !found {
		t.Fatalf("control status lacks sandbox_unavailable: %+v", st.Warnings)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func Test_Runtime_WindowsL2RunnerServesAndClosesAfterAgent(t *testing.T) {
	runner := &stubRunner{}
	stubOpener(t, func(context.Context, string) (l2Runner, error) { return runner, nil })
	store, root := l2Config(t, true)
	client, stop := serve(t, store, root)
	out := callTool(t, client, "exec.run", map[string]any{"cmd": os.Args[0], "args": []string{"-test.run=^$"}})
	if out["exit_code"] != float64(5) {
		t.Fatal("L2 exec did not go through the installed runner", out)
	}
	if note := warningsOf(callTool(t, client, "sys.info", map[string]any{})); strings.Contains(note, "sandbox.unavailable") {
		t.Fatal("a healthy runner reported a failure note", note)
	}
	runner.mu.Lock()
	starts, closes := runner.starts, runner.closes
	runner.mu.Unlock()
	if starts != 1 || closes != 0 {
		t.Fatalf("runner starts=%d closes=%d while serving", starts, closes)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	closes = runner.closes
	runner.mu.Unlock()
	if closes != 1 {
		t.Fatalf("runner closed %d times at shutdown, want 1", closes)
	}
}

func Test_Runtime_WindowsL2IgnoredOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the switch is effective on Windows")
	}
	store, root := l2Config(t, true)
	runner, notes, unavailable := startSandbox(context.Background(), store.Get(), filepath.Join(root, "state"))
	if runner != nil || len(notes) != 1 || !strings.Contains(notes[0], "仅在 Windows") || unavailable != "" {
		t.Fatal("the switch must be ignored with a note off Windows", runner, notes, unavailable)
	}
}
