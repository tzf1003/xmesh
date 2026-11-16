// Package delegatetest is the fake runner that the delegate tests run in place of Claude Code and Codex. A test
// points a runner's cmd at its own test binary and passes Marker as the first argument, followed by a scenario name
// and its parameters. The environment cannot carry the scenario, because the policy engine filters it, so it travels
// in argv. TestMain calls Serve before testing.M.Run, so the test framework never starts in the fake process.
package delegatetest

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Marker is the first argument that makes a test binary behave as the fake runner.
const Marker = "--xmesh-delegate-fake"

// Serve runs the fake when args select it, and exits the process with the fake's status. Otherwise it returns.
func Serve(args []string) {
	if len(args) < 3 || args[1] != Marker {
		return
	}
	os.Exit(run(args[2], args[3:]))
}

type obj = map[string]any

// emit writes one line of stream output.
func emit(v any) {
	b, _ := json.Marshal(v)
	_, _ = os.Stdout.Write(append(b, '\n'))
}

// emitText writes one line as is, which is how a runner prints something that is not JSON.
func emitText(line string) {
	_, _ = os.Stdout.WriteString(line + "\n")
}

func readStdin() string {
	b, _ := io.ReadAll(os.Stdin)
	return string(b)
}

func argOr(extra []string, i int, def string) string {
	if i < len(extra) && extra[i] != "" {
		return extra[i]
	}
	return def
}

func argInt(extra []string, i, def int) int {
	if n, err := strconv.Atoi(argOr(extra, i, "")); err == nil {
		return n
	}
	return def
}

func sleepMS(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// run behaves as the named runner scenario and returns the process status.
func run(scenario string, extra []string) int {
	switch scenario {
	case "claude":
		// A successful run: init, assistant text, a tool round trip and a final result that echoes stdin.
		prompt := readStdin()
		session := argOr(extra, 0, "sess-1")
		emit(obj{"type": "system", "subtype": "init", "session_id": session})
		emit(obj{"type": "assistant", "message": obj{"content": []any{obj{"type": "text", "text": "working"}}}})
		emit(obj{"type": "assistant", "message": obj{"content": []any{obj{"type": "tool_use", "id": "tu-1", "name": "Read", "input": obj{"file_path": "a.txt"}}}}})
		emit(obj{"type": "user", "message": obj{"content": []any{obj{"type": "tool_result", "content": "file body"}}}})
		emit(obj{"type": "result", "subtype": "success", "is_error": false, "result": "answer:" + prompt, "session_id": session})
		return 0
	case "claude-error":
		// The final result is marked as an error, though the process exits with 0.
		_ = readStdin()
		emit(obj{"type": "system", "subtype": "init", "session_id": "sess-err"})
		emit(obj{"type": "result", "subtype": "error", "is_error": true, "result": "permission denied"})
		return 0
	case "codex":
		// The codex mapping: thread.started, a command item, and a final agent message.
		prompt := readStdin()
		thread := argOr(extra, 0, "thr-1")
		emit(obj{"type": "thread.started", "thread_id": thread})
		emit(obj{"type": "item.completed", "item": obj{"type": "command_execution", "command": "ls"}})
		emit(obj{"type": "item.completed", "item": obj{"type": "agent_message", "text": "first draft"}})
		emit(obj{"type": "item.completed", "item": obj{"type": "agent_message", "text": "codex:" + prompt}})
		emit(obj{"type": "turn.completed"})
		return 0
	case "assistants":
		// Assistant text with no final result, so the reply is the text joined in order.
		_ = readStdin()
		emit(obj{"type": "assistant", "message": obj{"content": []any{obj{"type": "text", "text": "Hel"}}}})
		emit(obj{"type": "assistant", "message": obj{"content": []any{obj{"type": "text", "text": "lo"}}}})
		return 0
	case "exit":
		// Writes a line to each stream and exits with the given status, which is 3 by default.
		_ = readStdin()
		emitText("plain text before the failure")
		_, _ = os.Stderr.WriteString("boom\n")
		return argInt(extra, 0, 3)
	case "raw":
		// Lines that are not JSON, or that the mapping does not know, followed by a final result.
		_ = readStdin()
		emitText("not json at all")
		emitText("[1,2]")
		emit(obj{"type": "mystery", "x": 1})
		emit(obj{"type": "result", "subtype": "success", "is_error": false, "result": "done"})
		return 0
	case "argv":
		// Reports its own argument list (after the marker) as the final result.
		_ = readStdin()
		argv := append([]string{}, os.Args[2:]...)
		b, _ := json.Marshal(argv)
		emit(obj{"type": "result", "subtype": "success", "is_error": false, "result": string(b)})
		return 0
	case "env":
		// Reports the XMESH_DELEGATE variable of its environment.
		_ = readStdin()
		emit(obj{"type": "result", "subtype": "success", "is_error": false, "result": "XMESH_DELEGATE=" + os.Getenv("XMESH_DELEGATE")})
		return 0
	case "sleep":
		// Starts, reports a session, and then runs until it is terminated.
		_ = readStdin()
		emit(obj{"type": "system", "subtype": "init", "session_id": "sess-sleep"})
		sleepMS(int(time.Hour / time.Millisecond))
		return 0
	case "tree":
		// Starts a grandchild that holds the output pipes and beats into the file named by extra[0], then sleeps.
		_ = readStdin()
		child := exec.Command(os.Args[0], Marker, "heartbeat", argOr(extra, 0, ""))
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			return 4
		}
		emit(obj{"type": "system", "subtype": "init", "session_id": "sess-tree"})
		sleepMS(int(time.Hour / time.Millisecond))
		return 0
	case "heartbeat":
		// Appends a line every 20 milliseconds, so a test can tell whether the process is still running.
		file := argOr(extra, 0, "")
		for {
			f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return 5
			}
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + " " + strconv.FormatInt(time.Now().UnixNano(), 10) + "\n")
			_ = f.Close()
			sleepMS(20)
		}
	case "drip":
		// Reports a session, waits extra[0] milliseconds, reports a second event, waits extra[1], then finishes.
		_ = readStdin()
		emit(obj{"type": "system", "subtype": "init", "session_id": "sess-drip"})
		sleepMS(argInt(extra, 0, 300))
		emit(obj{"type": "assistant", "message": obj{"content": []any{obj{"type": "text", "text": "second"}}}})
		sleepMS(argInt(extra, 1, 300))
		emit(obj{"type": "result", "subtype": "success", "is_error": false, "result": "drip done"})
		return 0
	case "big":
		// One long line whose cut point falls inside a two-byte character, then a final result.
		_ = readStdin()
		emit(obj{"type": "assistant", "message": obj{"content": []any{obj{"type": "text", "text": "a" + strings.Repeat("é", 5000)}}}})
		emit(obj{"type": "result", "subtype": "success", "is_error": false, "result": "big done"})
		return 0
	}
	_, _ = os.Stderr.WriteString("unknown fake scenario " + scenario + "\n")
	return 2
}
