package winbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/exec"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/pty"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"golang.org/x/sys/windows"
)

type runnerCloser func() error

func (c runnerCloser) Close() error { return c() }

// runnerFixture wires the real tool managers to a managed AppContainer runner
// over one workspace. The coordinator is a test double: the fixture itself keeps
// external writers quiescent, which is the property a production coordinator
// must establish.
type runnerFixture struct {
	root, outside, helper string
	runner                *Runner
	policy                *policy.Engine
	resolver              *cwd.Resolver
	mu                    sync.Mutex
	prepared, released    int
	journals              []string
}

func newRunnerFixture(t *testing.T) *runnerFixture {
	t.Helper()
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	// The cwd resolver reports the canonical long path, not an 8.3 form.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &runnerFixture{root: filepath.Join(base, "workspace"), outside: filepath.Join(base, "outside")}
	for _, dir := range []string{f.root, f.outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	setMediumLabel(t, f.root)
	if err := os.WriteFile(filepath.Join(f.outside, "secret.txt"), []byte("host-only"), 0600); err != nil {
		t.Fatal(err)
	}
	// The helper is this test binary, copied so the sandbox may execute it from
	// the granted workspace.
	f.helper = filepath.Join(f.root, "helper.exe")
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.helper, body, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(base, "state")
	f.runner, err = NewRunner(context.Background(), RunnerConfig{
		StateDir: state, MaxObjects: 64, Interval: 20 * time.Millisecond,
		SupervisorCommand: supervisorTestCommand(t, false),
		Coordinator: func(_ context.Context, workspace, journal string) (io.Closer, error) {
			if !strings.EqualFold(workspace, f.root) {
				return nil, fmt.Errorf("unexpected workspace %q", workspace)
			}
			f.mu.Lock()
			f.prepared++
			f.journals = append(f.journals, journal)
			f.mu.Unlock()
			return runnerCloser(func() error {
				f.mu.Lock()
				f.released++
				f.mu.Unlock()
				return nil
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := f.runner.Close(ctx); err != nil {
			t.Error("runner close", err)
		}
	})
	f.policy, err = policy.New(config.Policy{Isolation: "L2"})
	if err != nil {
		t.Fatal(err)
	}
	f.policy.UseSandboxRunner(f.runner)
	f.resolver, err = cwd.New(f.root, nil, cwd.Policy{Allow: []string{f.root}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *runnerFixture) execManager(t *testing.T, timeout string) *exec.Manager {
	t.Helper()
	m, err := exec.New(config.Exec{MaxConcurrent: 2, ExclusiveSlots: 1, DefaultTimeout: timeout, IdempotencyCache: 10, InlineOutputBytes: "64KiB"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func (f *runnerFixture) run(m *exec.Manager, arguments map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(arguments)
	result, err := m.Run(context.Background(), "runner-test", f.resolver, f.policy, raw)
	return result, describeToolError(err)
}

// describeToolError exposes the cause that tool errors hide from callers.
func describeToolError(err error) error {
	var tool *toolerr.Error
	if errors.As(err, &tool) && tool.Cause != nil {
		return fmt.Errorf("%w: %v", err, tool.Cause)
	}
	return err
}

func (f *runnerFixture) helperCall(mode string, paths ...string) map[string]any {
	args := append([]string{"-test.run=^Test_WindowsAppContainerRunnerHelper$", "--", "--xmesh-runner-helper", mode}, paths...)
	return map[string]any{"cmd": `.\helper`, "args": args, "env": map[string]string{"GORACE": "log_path=runner-race"}}
}

// requireReclaimed checks the outcome every finished execution must leave:
// nothing live in the runner, the writer guard released, the root descriptor
// byte-for-byte restored and no journal left in the state directory.
func (f *runnerFixture) requireReclaimed(t *testing.T, before []byte, control windows.SECURITY_DESCRIPTOR_CONTROL, executions int) {
	t.Helper()
	f.runner.mu.Lock()
	active, pending := len(f.runner.active), len(f.runner.pending)
	f.runner.mu.Unlock()
	f.mu.Lock()
	prepared, released := f.prepared, f.released
	f.mu.Unlock()
	if active != 0 || pending != 0 || prepared != executions || released != executions {
		t.Fatalf("runner state: active=%d pending=%d prepared=%d released=%d want %d", active, pending, prepared, released, executions)
	}
	after, afterControl := kernelJournalState(t, f.root)
	if !bytes.Equal(before, after) || control != afterControl {
		t.Fatal("workspace root descriptor changed")
	}
	entries, err := os.ReadDir(f.runner.config.StateDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("completed journals remain", entries, err)
	}
}

type runnerProof struct {
	AppContainer bool
	Input        string
	Directory    string
	Wrote        bool
	OutsideRead  string
	Environment  string
}

func Test_WindowsAppContainerRunnerExec(t *testing.T) {
	f := newRunnerFixture(t)
	before, control := kernelJournalState(t, f.root)
	m := f.execManager(t, "60s")
	input := "输入\x00data\n" + strings.Repeat("abc", 2048)
	call := f.helperCall("proof", filepath.Join(f.outside, "secret.txt"))
	call["stdin"] = input
	call["env"].(map[string]string)["PROOF_VALUE"] = "子环境"
	result, err := f.run(m, call)
	if err != nil {
		t.Fatal(err)
	}
	if result["exit_code"] != 37 || result["stderr"] != "proof-stderr" {
		t.Fatalf("exec result: %v", result)
	}
	var proof runnerProof
	if err := json.Unmarshal([]byte(result["stdout"].(string)), &proof); err != nil {
		t.Fatal(err, result["stdout"])
	}
	if !proof.AppContainer || proof.Input != input || !proof.Wrote || proof.OutsideRead != "denied" || proof.Environment != "子环境" {
		t.Fatalf("sandbox proof: %+v", proof)
	}
	if !strings.EqualFold(filepath.Clean(proof.Directory), filepath.Clean(f.root)) {
		t.Fatal("command directory", proof.Directory)
	}
	if body, err := os.ReadFile(filepath.Join(f.root, "inside.txt")); err != nil || string(body) != "inside" {
		t.Fatal("sandbox write missing", string(body), err)
	}
	f.requireReclaimed(t, before, control, 1)
}

func Test_WindowsAppContainerRunnerExecTree(t *testing.T) {
	for _, mode := range []string{"tree-timeout", "tree-exit"} {
		t.Run(mode, func(t *testing.T) {
			f := newRunnerFixture(t)
			before, control := kernelJournalState(t, f.root)
			m := f.execManager(t, "60s")
			pids := filepath.Join(f.root, "pids.txt")
			call := f.helperCall(mode, pids)
			if mode == "tree-timeout" {
				call["timeout"] = 12000
			}
			start := time.Now()
			result, err := f.run(m, call)
			if mode == "tree-timeout" {
				var tool *toolerr.Error
				if !errors.As(err, &tool) || tool.Code != "timeout" {
					t.Fatal("timeout was not reported", result, err)
				}
			} else if err != nil || result["exit_code"] != 0 {
				t.Fatal(result, err)
			}
			if mode == "tree-timeout" && time.Since(start) > 40*time.Second {
				t.Fatal("termination did not complete promptly", time.Since(start))
			}
			body, readErr := os.ReadFile(pids)
			if readErr != nil {
				t.Fatal("process census file", readErr)
			}
			fields := strings.Fields(string(body))
			if len(fields) != 2 {
				t.Fatal("process census", string(body))
			}
			for _, field := range fields {
				pid, err := strconv.Atoi(field)
				if err != nil {
					t.Fatal(err)
				}
				requireProcessGone(t, uint32(pid))
			}
			f.requireReclaimed(t, before, control, 1)
		})
	}
}

func requireProcessGone(t *testing.T, pid uint32) {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	if result, _ := windows.WaitForSingleObject(h, 0); result != windows.WAIT_OBJECT_0 {
		t.Fatalf("process %d survived its execution", pid)
	}
}

func Test_WindowsAppContainerRunnerExecConcurrent(t *testing.T) {
	f := newRunnerFixture(t)
	before, control := kernelJournalState(t, f.root)
	m := f.execManager(t, "90s")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	results := make([]map[string]any, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = f.run(m, f.helperCall("hold"))
		}()
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || results[i]["exit_code"] != 0 || results[i]["stdout"] != "held" {
			t.Fatal(i, results[i], errs[i])
		}
	}
	f.requireReclaimed(t, before, control, 2)
}

func Test_WindowsAppContainerRunnerFailures(t *testing.T) {
	f := newRunnerFixture(t)
	m := f.execManager(t, "60s")
	if _, err := f.run(m, map[string]any{"cmd": "xmesh-command-that-does-not-exist"}); err == nil {
		t.Fatal("unresolved command was accepted")
	}
	f.mu.Lock()
	prepared := f.prepared
	f.mu.Unlock()
	if prepared != 0 {
		t.Fatal("unresolved command prepared a sandbox")
	}
	// A coordinator failure leaves nothing running or reserved.
	failing, err := NewRunner(context.Background(), RunnerConfig{
		StateDir: filepath.Join(t.TempDir(), "state"),
		Coordinator: func(context.Context, string, string) (io.Closer, error) {
			return runnerCloser(func() error { return nil }), errors.New("writers could not be frozen")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.policy.UseSandboxRunner(failing)
	if _, err := f.run(m, f.helperCall("proof", "x")); err == nil {
		t.Fatal("start succeeded without a frozen workspace")
	}
	if err := failing.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(context.Background(), RunnerConfig{StateDir: filepath.Join(t.TempDir(), "state")}); err == nil {
		t.Fatal("runner accepted a missing coordinator")
	}
}

type ptyOutput struct {
	t       *testing.T
	m       *pty.Manager
	f       *runnerFixture
	caller  identity.Caller
	session string
	cursor  uint64
	text    strings.Builder
	eof     bool
}

func (o *ptyOutput) call(name string, arguments map[string]any) map[string]any {
	o.t.Helper()
	raw, _ := json.Marshal(arguments)
	result, err := o.m.Call(context.Background(), o.caller, o.f.resolver, o.f.policy, name, raw)
	if err != nil {
		o.t.Fatal(name, describeToolError(err))
	}
	return result
}

func (o *ptyOutput) until(expression string) {
	o.t.Helper()
	re := regexp.MustCompile(expression)
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		if re.MatchString(o.text.String()) {
			return
		}
		if o.eof {
			break
		}
		result := o.call("pty.read", map[string]any{"session_id": o.session, "cursor": o.cursor, "wait_ms": 100})
		o.text.WriteString(result["data"].(string))
		o.cursor = result["next_cursor"].(uint64)
		o.eof = result["eof"] == true
	}
	o.t.Fatalf("terminal output did not match %q (eof=%v): %q", expression, o.eof, o.text.String())
}

// drain reads to end of file, which the terminal reaches only after its
// execution has been terminated and the pseudoconsole closed.
func (o *ptyOutput) drain() {
	o.t.Helper()
	for deadline := time.Now().Add(90 * time.Second); !o.eof; {
		if time.Now().After(deadline) {
			o.t.Fatalf("terminal did not end: %q", o.text.String())
		}
		result := o.call("pty.read", map[string]any{"session_id": o.session, "cursor": o.cursor, "wait_ms": 200})
		o.text.WriteString(result["data"].(string))
		o.cursor = result["next_cursor"].(uint64)
		o.eof = result["eof"] == true
	}
}

func (f *runnerFixture) openTerminal(t *testing.T, m *pty.Manager, reg *identity.Registry, name string) *ptyOutput {
	t.Helper()
	c := identity.Caller{ID: "node/" + name}
	if err := reg.Add(c); err != nil {
		t.Fatal(err)
	}
	o := &ptyOutput{t: t, m: m, f: f, caller: c}
	result := o.call("pty.open", map[string]any{"cols": 80, "rows": 24, "shell": os.Getenv("ComSpec")})
	o.session = result["session_id"].(string)
	return o
}

func newTerminalManager(t *testing.T) (*pty.Manager, *identity.Registry) {
	t.Helper()
	reg := identity.NewRegistry()
	m := pty.New(config.PTY{MaxSessions: 4, MaxPerCaller: 4, IdleTimeout: "30m", OrphanGrace: "10m", ScrollbackBytes: "1MiB", DefaultShell: "auto", ClosedRetain: "5m", ClosedRetainMax: 20}, reg)
	t.Cleanup(m.Shutdown)
	return m, reg
}

func Test_WindowsAppContainerRunnerTerminal(t *testing.T) {
	f := newRunnerFixture(t)
	before, control := kernelJournalState(t, f.root)
	m, reg := newTerminalManager(t)
	o := f.openTerminal(t, m, reg, "conpty")
	write := func(data string) { o.call("pty.write", map[string]any{"session_id": o.session, "data": data}) }
	write("set MARK=OK\r\n")
	write("echo MARK-%MARK%-END\r\n")
	o.until(`MARK-OK-END`)
	write(`helper.exe -test.run=Test_WindowsAppContainerRunnerHelper -- --xmesh-runner-helper conpty` + "\r\n")
	o.until(`CONPTY-APPCONTAINER-1`)
	o.call("pty.resize", map[string]any{"session_id": o.session, "cols": 137, "rows": 43})
	write("mode con\r\n")
	o.until(`Columns:\s+137`)
	write("exit\r\n")
	o.drain()
	waitRunnerIdle(t, f, 1)
	f.requireReclaimed(t, before, control, 1)
}

func Test_WindowsAppContainerRunnerTerminalKill(t *testing.T) {
	f := newRunnerFixture(t)
	before, control := kernelJournalState(t, f.root)
	m, reg := newTerminalManager(t)
	o := f.openTerminal(t, m, reg, "kill")
	o.call("pty.write", map[string]any{"session_id": o.session, "data": "echo READY-%OS%\r\n"})
	o.until(`READY-Windows_NT`)
	// A descendant that would outlive the shell keeps running until the
	// execution is reclaimed.
	o.call("pty.write", map[string]any{"session_id": o.session, "data": "start /b helper.exe -test.run=Test_WindowsAppContainerRunnerHelper -- --xmesh-runner-helper tree-exit " + filepath.Join(f.root, "terminal-pids.txt") + "\r\n"})
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if body, err := os.ReadFile(filepath.Join(f.root, "terminal-pids.txt")); err == nil && len(strings.Fields(string(body))) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant did not start")
		}
	}
	o.call("pty.signal", map[string]any{"session_id": o.session, "signal": "KILL"})
	o.drain()
	waitRunnerIdle(t, f, 1)
	body, _ := os.ReadFile(filepath.Join(f.root, "terminal-pids.txt"))
	for _, field := range strings.Fields(string(body)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		requireProcessGone(t, uint32(pid))
	}
	f.requireReclaimed(t, before, control, 1)
}

// Session end is observed by the terminal manager before the runner finishes
// reclaiming, so wait for the retained execution to drain.
func waitRunnerIdle(t *testing.T, f *runnerFixture, executions int) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		f.runner.mu.Lock()
		active, pending := len(f.runner.active), len(f.runner.pending)
		f.runner.mu.Unlock()
		f.mu.Lock()
		released := f.released
		f.mu.Unlock()
		if active == 0 && pending == 0 && released == executions {
			return
		}
	}
	t.Fatal("sandbox execution was not reclaimed after the terminal ended")
}

func Test_WindowsAppContainerRunnerHelper(t *testing.T) {
	// Policy withholds XMESH_* variables from commands, so the helper is
	// selected by its arguments.
	var mode string
	var paths []string
	for i, arg := range os.Args {
		if arg == "--xmesh-runner-helper" && i+1 < len(os.Args) {
			mode, paths = os.Args[i+1], os.Args[i+2:]
		}
	}
	if mode == "" {
		t.Skip("AppContainer runner helper")
	}
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "proof":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		directory, _ := os.Getwd()
		proof := runnerProof{AppContainer: container != 0, Input: string(input), Directory: directory, Environment: os.Getenv("PROOF_VALUE")}
		proof.Wrote = os.WriteFile("inside.txt", []byte("inside"), 0600) == nil
		if body, err := os.ReadFile(paths[0]); err != nil {
			proof.OutsideRead = "denied"
		} else {
			proof.OutsideRead = string(body)
		}
		if err := json.NewEncoder(os.Stdout).Encode(proof); err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(os.Stderr, "proof-stderr")
		os.Exit(37)
	case "conpty":
		fmt.Printf("CONPTY-APPCONTAINER-%d\n", container)
		os.Exit(0)
	case "hold":
		time.Sleep(1500 * time.Millisecond)
		fmt.Print("held")
		os.Exit(0)
	case "sleep":
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	case "tree-timeout", "tree-exit":
		child := osexec.Command(os.Args[0], "-test.run=^Test_WindowsAppContainerRunnerHelper$", "--", "--xmesh-runner-helper", "sleep")
		child.Env = os.Environ()
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths[0], []byte(fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)), 0600); err != nil {
			t.Fatal(err)
		}
		if mode == "tree-exit" {
			os.Exit(0)
		}
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	t.Fatal("unknown runner helper mode", mode)
}
