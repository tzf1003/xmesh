package delegate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/delegate/delegatetest"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

const (
	callerA = "node-a/alice"
	callerB = "node-b/bob"
)

// testExe is the test binary. The runners of these tests re-execute it as the fake of package delegatetest.
func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// fakeRunner is a runner that runs one fake scenario with the given parameters. Its mode arguments are cleared, so the
// scenario sees only its own parameters. A resumed run runs the same scenario, with the session id as its parameter.
func fakeRunner(t *testing.T, scenario string, params ...string) config.DelegateRunner {
	t.Helper()
	return config.DelegateRunner{
		Cmd:        testExe(t),
		Args:       append([]string{delegatetest.Marker, scenario}, params...),
		ReadArgs:   []string{},
		WriteArgs:  []string{},
		ResumeArgs: []string{delegatetest.Marker, scenario, "{session_id}"},
	}
}

// testSettings enables delegation with the given runners and the default limits.
func testSettings(runners map[string]config.DelegateRunner) config.Delegate {
	return config.Delegate{Enabled: true, Runners: runners}
}

// harness drives a Manager as the tools would: one working directory allowed by the cwd policy, one settings value
// that the tests may change between calls, and a record of every End the Manager reports.
type harness struct {
	t        *testing.T
	m        *Manager
	dir      string
	settings config.Delegate

	mu   sync.Mutex
	ends []End
}

func newHarness(t *testing.T, settings config.Delegate) *harness {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// No live Claude Code session of the machine may show up in delegate.list, so the configuration directory is empty.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	h := &harness{t: t, dir: dir, settings: settings}
	h.m = New(func(end End) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.ends = append(h.ends, end)
	})
	t.Cleanup(h.m.Close)
	return h
}

// request is what one tool call of caller sees.
func (h *harness) request(caller string) Request {
	h.t.Helper()
	resolver, err := cwd.New(h.dir, nil, cwd.Policy{Allow: []string{h.dir}})
	if err != nil {
		h.t.Fatal(err)
	}
	engine, err := policy.New(config.Policy{})
	if err != nil {
		h.t.Fatal(err)
	}
	return Request{Caller: caller, Settings: h.settings, Resolver: resolver, Policy: engine, Gen: h.m.Generation()}
}

// call runs one tool as caller and returns its reply or its error.
func (h *harness) call(caller, name string, args map[string]any) (Object, error) {
	h.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.m.Tool(context.Background(), h.request(caller), name, raw)
}

// ok runs one tool as caller and fails the test when it returns an error.
func (h *harness) ok(caller, name string, args map[string]any) Object {
	h.t.Helper()
	out, err := h.call(caller, name, args)
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	return out
}

// finish reads the output of a task until it reports end of file. It returns every event in order and the last
// reply.
func (h *harness) finish(caller, taskID string) ([]Object, Object) {
	h.t.Helper()
	var events []Object
	cursor := int64(0)
	deadline := time.Now().Add(60 * time.Second)
	for {
		out := h.ok(caller, "delegate.output", map[string]any{"task_id": taskID, "cursor": cursor, "wait_ms": 1000})
		events = append(events, out["events"].([]Object)...)
		if out["eof"] == true {
			return events, out
		}
		cursor = out["next_cursor"].(int64)
		if time.Now().After(deadline) {
			h.t.Fatalf("task %s did not end", taskID)
		}
	}
}

// ended returns the End records seen so far. The Manager reports an end after the task has become visible as
// finished, so a test that has just seen eof waits for the record with eventually.
func (h *harness) ended() []End {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]End(nil), h.ends...)
}

// codeOf returns the toolerr code of err, or "" when err is nil or is not a toolerr.
func codeOf(err error) string {
	var te *toolerr.Error
	if errors.As(err, &te) {
		return te.Code
	}
	return ""
}

// fieldOf returns one field of a toolerr, or nil.
func fieldOf(err error, key string) any {
	var te *toolerr.Error
	if errors.As(err, &te) {
		return te.Fields[key]
	}
	return nil
}

// wantCode fails the test unless err is a toolerr with the given code.
func (h *harness) wantCode(err error, code string) {
	h.t.Helper()
	if got := codeOf(err); got != code {
		h.t.Fatalf("error code %q (%v), want %q", got, err, code)
	}
}

// eventually polls cond for up to ten seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// heartbeatPID reads the process id that a heartbeat process wrote first. It reports false until the file exists.
func heartbeatPID(file string) (int, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	return pid, err == nil
}

// killHeartbeat kills the heartbeat process of a test that fails before it has been stopped, so no test leaves a
// process behind.
func killHeartbeat(t *testing.T, file string) {
	t.Cleanup(func() {
		pid, ok := heartbeatPID(file)
		if !ok {
			return
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
}

// assertStopped fails unless the heartbeat file stops growing: the process that wrote it is gone.
func assertStopped(t *testing.T, file string) {
	t.Helper()
	size := func() int64 {
		info, err := os.Stat(file)
		if err != nil {
			return -1
		}
		return info.Size()
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		before := size()
		time.Sleep(300 * time.Millisecond)
		if before > 0 && size() == before {
			return
		}
	}
	t.Fatal("the process of the task is still writing its heartbeat after the task ended")
}
