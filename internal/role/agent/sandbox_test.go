package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/sandbox"
)

// recordingRunner stands in for a managed runner. It never starts a process: it
// records the workspace and reports a fixed exit code, so a call that reached it
// is distinguishable from one that ran the command directly.
type recordingRunner struct {
	mu         sync.Mutex
	workspaces []string
}

type recordedProcess struct{}

func (recordedProcess) PID() int                    { return 1 }
func (recordedProcess) Wait() (int, error)          { return recordedExit, nil }
func (recordedProcess) Close(context.Context) error { return nil }

const recordedExit = 3

func (r *recordingRunner) Start(_ context.Context, workspace string, _ sandbox.Spec, _ sandbox.Streams) (sandbox.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workspaces = append(r.workspaces, workspace)
	return recordedProcess{}, nil
}

func (r *recordingRunner) StartTerminal(context.Context, string, sandbox.Spec, int, int) (sandbox.Terminal, error) {
	return nil, errors.New("not used")
}

func (r *recordingRunner) starts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.workspaces)
}

func setIsolation(t *testing.T, path, level string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	agent := doc["agent"].(map[string]any)
	agent["policy"] = map[string]any{"isolation": level}
	if b, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

// harmless re-runs this test binary without selecting any test.
func harmless(t *testing.T) map[string]any {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"cmd": exe, "args": []string{"-test.run=^$"}}
}

func Test_SandboxRunnerAttachedToEveryCall(t *testing.T) {
	path, store := configuration(t)
	setIsolation(t, path, "L2")
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	runner := &recordingRunner{}
	a.SetSandbox(runner)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	for call := 1; call <= 3; call++ {
		out := object(t, tool(t, client, "exec.run", harmless(t)))
		if out["exit_code"] != float64(recordedExit) || runner.starts() != call {
			t.Fatalf("call %d did not run through the managed runner: %v starts=%d", call, out, runner.starts())
		}
	}
}

func Test_SandboxRunnerHotSwitchBetweenL1AndL2(t *testing.T) {
	path, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	runner := &recordingRunner{}
	a.SetSandbox(runner)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	run := func() map[string]any { return object(t, tool(t, client, "exec.run", harmless(t))) }
	if out := run(); out["exit_code"] != float64(0) || runner.starts() != 0 {
		t.Fatalf("L1 used the runner: %v", out)
	}
	setIsolation(t, path, "L2")
	if out := run(); out["exit_code"] != float64(recordedExit) || runner.starts() != 1 {
		t.Fatalf("L2 after reload skipped the runner: %v", out)
	}
	setIsolation(t, path, "L1")
	if out := run(); out["exit_code"] != float64(0) || runner.starts() != 1 {
		t.Fatalf("L1 after reload used the runner: %v", out)
	}
}

func Test_SandboxRunnerAbsentIsNeverTypedNil(t *testing.T) {
	_, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	engine, e := policy.New(config.Policy{Isolation: "L2"})
	if e != nil {
		t.Fatal(e)
	}
	a.attachSandbox(engine)
	if engine.SandboxRunner() != nil {
		t.Fatal("an engine without a runner reports one")
	}
	a.SetSandbox(nil, "L2 运行器不可用")
	a.attachSandbox(engine)
	if engine.SandboxRunner() != nil {
		t.Fatal("a nil runner was installed as a typed nil")
	}
	a.SetSandbox(&recordingRunner{})
	a.attachSandbox(engine)
	if engine.SandboxRunner() == nil {
		t.Fatal("the runner was not attached")
	}
}

func Test_SandboxWarningsReachSysInfo(t *testing.T) {
	_, store := configuration(t)
	a, e := New(store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	a.SetSandbox(nil, "Windows L2 运行器未能启动")
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	info := object(t, tool(t, client, "sys.info", map[string]any{}))
	warnings, _ := info["warnings"].([]any)
	found := false
	for _, w := range warnings {
		if s, _ := w.(string); strings.Contains(s, "Windows L2 运行器未能启动") {
			found = true
		}
	}
	if !found {
		t.Fatal("startup note missing from sys.info", info["warnings"])
	}
}
