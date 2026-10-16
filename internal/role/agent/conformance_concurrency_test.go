package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestConformanceBackendHelper is the stdio backend process used by
// Test_CONCONC01_NoGlobalLock. It exposes `echo` and a `block` tool that
// does not return until the file named by its `release` argument exists.
func TestConformanceBackendHelper(t *testing.T) {
	helper, logPath := false, ""
	for _, arg := range os.Args {
		if arg == "--xmesh-conformance-backend-helper" {
			helper = true
		}
		if path, ok := strings.CutPrefix(arg, "--xmesh-conformance-log="); ok {
			logPath = path
		}
	}
	if !helper {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "1"}, nil)
	for _, name := range []string{"echo", "block"} {
		name := name
		s.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			_ = json.Unmarshal(r.Params.Arguments, &args)
			if logPath != "" {
				// Every request that reaches the backend leaves a line behind.
				if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
					_, _ = f.WriteString(name + "\n")
					_ = f.Close()
				}
			}
			if name == "block" {
				if started, _ := args["started"].(string); started != "" {
					_ = os.WriteFile(started, []byte("x"), 0o600)
				}
				release, _ := args["release"].(string)
				for {
					if _, err := os.Stat(release); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						return Result(nil, ctx.Err()), nil
					case <-time.After(20 * time.Millisecond):
					}
				}
			}
			return Result(args, nil), nil
		})
	}
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(25)
	}
	os.Exit(0)
}

// waitForPath blocks until the file exists.
func waitForPath(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

// waitParkedInPTYRead returns once a goroutine sits in the pty.read long-poll.
func waitParkedInPTYRead(t *testing.T) {
	t.Helper()
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g, "pty.(*Session).read(") && strings.Contains(g, "[select") {
				return
			}
		}
	}
	t.Fatal("no goroutine parked in the pty.read long-poll")
}

type blockedCall struct {
	result *mcp.CallToolResult
	err    error
}

// start issues a long-running tool call on its own goroutine.
func startBlocked(client *mcp.ClientSession, name string, args any) <-chan blockedCall {
	done := make(chan blockedCall, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		done <- blockedCall{out, err}
	}()
	return done
}

func requireFinished(t *testing.T, what string, done <-chan blockedCall) *mcp.CallToolResult {
	t.Helper()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("%s: %v", what, r.err)
		}
		return r.result
	case <-time.After(20 * time.Second):
		t.Fatalf("%s did not finish after its blocker was released", what)
	}
	return nil
}

// While one resource kind is stuck (a PTY long-poll, a running exec, a held
// file lock, a backend call in progress), the other kinds still complete.
// The four kinds each lock independently; none is serialized behind a
// process-wide mutex.
// conformance: CON-CONC-01
func Test_CONCONC01_NoGlobalLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("blockers are POSIX shell loops")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := []string{bin, "-test.run=TestConformanceBackendHelper", "--", "--xmesh-conformance-backend-helper"}
	a, url, root := conformanceAgent(t, func(agent map[string]any, _ string) {
		agent["pty"] = map[string]any{"default_shell": "/bin/sh"}
		agent["backends"] = map[string]any{
			"slow": map[string]any{"transport": "stdio", "command": command, "policy": "shared"},
			"fast": map[string]any{"transport": "stdio", "command": command, "policy": "shared"},
		}
	})
	signals := t.TempDir()
	blocker, prober := connect(t, url), connect(t, url)
	const bound = 8 * time.Second

	probes := map[string]func(t *testing.T){
		"pty": func(t *testing.T) {
			if out := callWithin(t, prober, bound, "pty.list", map[string]any{}); out.IsError {
				t.Fatal(object(t, out))
			}
			opened := callWithin(t, prober, bound, "pty.open", map[string]any{"cols": 80, "rows": 24})
			if opened.IsError {
				t.Fatal(object(t, opened))
			}
			if out := callWithin(t, prober, bound, "pty.close", map[string]any{"session_id": object(t, opened)["session_id"]}); out.IsError {
				t.Fatal(object(t, out))
			}
		},
		"exec": func(t *testing.T) {
			out := callWithin(t, prober, bound, "exec.run", map[string]any{"cmd": "printf", "args": []string{"probe"}})
			if out.IsError || object(t, out)["stdout"] != "probe" {
				t.Fatal(object(t, out))
			}
		},
		"file": func(t *testing.T) {
			if out := callWithin(t, prober, bound, "fs.write", map[string]any{"path": "probe.txt", "content": "probe"}); out.IsError {
				t.Fatal(object(t, out))
			}
			out := callWithin(t, prober, bound, "fs.read", map[string]any{"path": "probe.txt"})
			if out.IsError || !strings.Contains(object(t, out)["content"].(string), "probe") {
				t.Fatal(object(t, out))
			}
		},
		"backend": func(t *testing.T) {
			out := callWithin(t, prober, bound, "fast.echo", map[string]any{"marker": "probe"})
			if out.IsError || object(t, out)["marker"] != "probe" {
				t.Fatal(object(t, out))
			}
		},
	}
	runProbes := func(t *testing.T, except string) {
		t.Helper()
		if out := callWithin(t, prober, bound, "sys.info", map[string]any{}); out.IsError {
			t.Fatal(object(t, out))
		}
		for kind, probe := range probes {
			if kind != except {
				probe(t)
			}
		}
	}

	t.Run("pty_long_poll", func(t *testing.T) {
		opened := callWithin(t, blocker, bound, "pty.open", map[string]any{"cols": 80, "rows": 24})
		if opened.IsError {
			t.Fatal(object(t, opened))
		}
		id := object(t, opened)["session_id"]
		cursor := float64(0)
		for {
			out := object(t, callWithin(t, blocker, bound, "pty.read", map[string]any{"session_id": id, "cursor": uint64(cursor), "wait_ms": 300}))
			cursor = out["next_cursor"].(float64)
			if out["data"] == "" {
				break
			}
		}
		poll := startBlocked(blocker, "pty.read", map[string]any{"session_id": id, "cursor": uint64(cursor), "wait_ms": 30000})
		waitParkedInPTYRead(t)
		runProbes(t, "pty")
		select {
		case <-poll:
			t.Fatal("the long-poll returned before any output was produced")
		default:
		}
		if out := callWithin(t, blocker, bound, "pty.write", map[string]any{"session_id": id, "data": "echo POLL_DONE\n"}); out.IsError {
			t.Fatal(object(t, out))
		}
		out := requireFinished(t, "pty.read long-poll", poll)
		if out.IsError || object(t, out)["data"] == "" {
			t.Fatal(object(t, out))
		}
	})

	t.Run("exec_running", func(t *testing.T) {
		started, release := filepath.Join(root, "exec-started"), filepath.Join(root, "exec-release")
		running := startBlocked(blocker, "exec.run", map[string]any{"cmd": "sh", "args": []string{"-c", "touch exec-started; while [ ! -f exec-release ]; do sleep 0.02; done; printf finished"}, "timeout": "50s"})
		waitForPath(t, started)
		runProbes(t, "exec")
		select {
		case <-running:
			t.Fatal("the blocking command ended early")
		default:
		}
		if err := os.WriteFile(release, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := requireFinished(t, "exec.run", running)
		if out.IsError || object(t, out)["stdout"] != "finished" {
			t.Fatal(object(t, out))
		}
	})

	t.Run("file_lock_held", func(t *testing.T) {
		// A long file operation holds the file lock; a file call queues behind
		// it, every other kind runs.
		a.fileMu.Lock()
		held := true
		t.Cleanup(func() {
			if held {
				a.fileMu.Unlock()
			}
		})
		queued := startBlocked(blocker, "fs.stat", map[string]any{"path": "probe.txt"})
		select {
		case <-queued:
			t.Fatal("the file call did not wait for the file lock; the blocker is ineffective")
		case <-time.After(300 * time.Millisecond):
		}
		runProbes(t, "file")
		a.fileMu.Unlock()
		held = false
		if out := requireFinished(t, "fs.stat", queued); out.IsError {
			t.Fatal(object(t, out))
		}
	})

	t.Run("backend_call_in_progress", func(t *testing.T) {
		started, release := filepath.Join(signals, "backend-started"), filepath.Join(signals, "backend-release")
		call := startBlocked(blocker, "slow.block", map[string]any{"started": started, "release": release})
		waitForPath(t, started)
		runProbes(t, "backend")
		// A second backend actor is independent of the busy one (checked by the
		// backend probe above); only the busy actor itself is occupied.
		select {
		case <-call:
			t.Fatal("the backend call returned early")
		default:
		}
		if err := os.WriteFile(release, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out := requireFinished(t, "slow.block", call); out.IsError {
			t.Fatal(object(t, out))
		}
	})
}
