package delegate

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/delegate/delegatetest"
)

// conformance: CTR-DLG-01
func Test_CTRDLG01_ListShowsRunnersAndOnlyTheCallersTasks(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"sleepy": fakeRunner(t, "sleep")}))
	started := h.ok(callerA, "delegate.run", map[string]any{"runner": "sleepy", "prompt": "wait", "wait_ms": 0})
	taskID := started["task_id"].(string)

	list := h.ok(callerA, "delegate.list", map[string]any{})
	names := map[string]bool{}
	for _, r := range list["runners"].([]Object) {
		names[r["name"].(string)] = true
	}
	for _, want := range []string{"claude", "codex", "sleepy"} {
		if !names[want] {
			t.Fatalf("runner %s is not listed: %v", want, list["runners"])
		}
	}
	tasks := list["tasks"].([]Object)
	if len(tasks) != 1 || tasks[0]["task_id"] != taskID || tasks[0]["state"] != "running" {
		t.Fatalf("tasks = %v, want the one running task of the caller", tasks)
	}
	if tasks[0]["cwd"] != h.dir {
		t.Fatalf("cwd = %v, want %s", tasks[0]["cwd"], h.dir)
	}
	if _, err := time.Parse(time.RFC3339Nano, tasks[0]["started_at"].(string)); err != nil {
		t.Fatalf("started_at: %v", err)
	}
	other := h.ok(callerB, "delegate.list", map[string]any{})
	if got := other["tasks"].([]Object); len(got) != 0 {
		t.Fatalf("another caller sees %v", got)
	}
	h.ok(callerA, "delegate.cancel", map[string]any{"task_id": taskID})
}

// conformance: CTR-DLG-01
func Test_CTRDLG01_DisabledRefusesEveryTool(t *testing.T) {
	h := newHarness(t, config.Delegate{Enabled: false})
	for _, name := range []string{"delegate.list", "delegate.run", "delegate.output", "delegate.send", "delegate.cancel"} {
		_, err := h.call(callerA, name, map[string]any{})
		h.wantCode(err, "policy_denied")
		if rule := fieldOf(err, "rule"); rule != "delegate.disabled" {
			t.Fatalf("%s: rule %v, want delegate.disabled", name, rule)
		}
	}
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_RunReturnsTheResultAndSession(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "fix the bug", "wait_ms": 20000})
	if out["state"] != "done" {
		t.Fatalf("state = %v, want done (%v)", out["state"], out)
	}
	if out["result"] != "answer:fix the bug" {
		t.Fatalf("result = %v", out["result"])
	}
	if out["session_id"] != "sess-1" || out["exit_code"] != 0 || out["runner"] != "claude" {
		t.Fatalf("reply = %v", out)
	}
	if out["resolved_path"] != h.dir {
		t.Fatalf("resolved_path = %v, want %s", out["resolved_path"], h.dir)
	}
	if out["next_cursor"].(int64) == 0 {
		t.Fatal("a finished task reports no events")
	}
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_PromptTravelsOnlyOnStdin(t *testing.T) {
	const secret = "PROMPT-THAT-MUST-STAY-OFF-THE-COMMAND-LINE"
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"argv": fakeRunner(t, "argv")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "argv", "prompt": secret, "wait_ms": 20000})
	var argv []string
	if err := json.Unmarshal([]byte(out["result"].(string)), &argv); err != nil {
		t.Fatal(err)
	}
	for _, arg := range argv {
		if strings.Contains(arg, secret) {
			t.Fatalf("the prompt appeared in argv: %q", argv)
		}
	}
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_ResumeArgumentAssembly(t *testing.T) {
	runner := config.DelegateRunner{
		Cmd:        testExe(t),
		Args:       []string{delegatetest.Marker, "claude", "sess-1"},
		ReadArgs:   []string{"--mode-read"},
		WriteArgs:  []string{},
		ResumeArgs: []string{delegatetest.Marker, "argv", "--resume", "{session_id}"},
	}
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": runner}))
	first := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "one", "wait_ms": 20000})
	sessionID, _ := first["session_id"].(string)
	if sessionID != "sess-1" {
		t.Fatalf("session_id = %q, want sess-1", sessionID)
	}

	second := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "two", "session_id": sessionID, "wait_ms": 20000})
	var argv []string
	if err := json.Unmarshal([]byte(second["result"].(string)), &argv); err != nil {
		t.Fatal(err)
	}
	if want := []string{"argv", "--resume", "sess-1", "--mode-read"}; !slices.Equal(argv, want) {
		t.Fatalf("resumed argv = %q, want %q", argv, want)
	}

	_, err := h.call(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "three", "session_id": "sess-unknown"})
	h.wantCode(err, "invalid_args")
	_, err = h.call(callerB, "delegate.run", map[string]any{"runner": "claude", "prompt": "four", "session_id": sessionID})
	h.wantCode(err, "invalid_args")
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_WriteModeNeedsAllowWrite(t *testing.T) {
	runner := config.DelegateRunner{Cmd: testExe(t), Args: []string{delegatetest.Marker, "argv"}, ReadArgs: []string{}, WriteArgs: []string{"--write-mode"}}
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"argv": runner}))
	args := map[string]any{"runner": "argv", "prompt": "edit", "mode": "write", "wait_ms": 20000}

	_, err := h.call(callerA, "delegate.run", args)
	h.wantCode(err, "policy_denied")
	if rule := fieldOf(err, "rule"); rule != "delegate.write_disabled" {
		t.Fatalf("rule = %v, want delegate.write_disabled", rule)
	}

	h.settings.AllowWrite = true
	out := h.ok(callerA, "delegate.run", args)
	var argv []string
	if err := json.Unmarshal([]byte(out["result"].(string)), &argv); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argv, "--write-mode") {
		t.Fatalf("write arguments missing from %q", argv)
	}
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_InvalidArgumentsAreRefusedBeforeAnyProcess(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude")}))
	tooLarge := strings.Repeat("x", 64<<10+1)
	cases := []struct {
		name  string
		args  map[string]any
		code  string
		field string
	}{
		{"no prompt", map[string]any{"runner": "claude"}, "invalid_args", "prompt"},
		{"empty prompt", map[string]any{"runner": "claude", "prompt": ""}, "invalid_args", "prompt"},
		{"prompt over 64 KiB", map[string]any{"runner": "claude", "prompt": tooLarge}, "invalid_args", "prompt"},
		{"unknown runner", map[string]any{"runner": "nobody", "prompt": "p"}, "invalid_args", "runner"},
		{"unknown mode", map[string]any{"runner": "claude", "prompt": "p", "mode": "sideways"}, "invalid_args", "mode"},
		{"bad key", map[string]any{"runner": "claude", "prompt": "p", "idempotency_key": "has space"}, "invalid_args", "idempotency_key"},
		{"bad timeout", map[string]any{"runner": "claude", "prompt": "p", "timeout": "soon"}, "invalid_args", "timeout"},
		{"wait over the maximum", map[string]any{"runner": "claude", "prompt": "p", "wait_ms": 60001}, "invalid_args", "wait_ms"},
		{"empty session", map[string]any{"runner": "claude", "prompt": "p", "session_id": ""}, "invalid_args", "session_id"},
		{"session never returned", map[string]any{"runner": "claude", "prompt": "p", "session_id": "sess-9"}, "invalid_args", "session_id"},
		{"cwd missing", map[string]any{"runner": "claude", "prompt": "p", "cwd": "no-such-directory"}, "invalid_cwd", ""},
	}
	for _, c := range cases {
		_, err := h.call(callerA, "delegate.run", c.args)
		if codeOf(err) != c.code {
			t.Fatalf("%s: error %q (%v), want %q", c.name, codeOf(err), err, c.code)
		}
		if c.field != "" && fieldOf(err, "field") != c.field {
			t.Fatalf("%s: field %v, want %s", c.name, fieldOf(err, "field"), c.field)
		}
	}
	if tasks := h.ok(callerA, "delegate.list", map[string]any{})["tasks"].([]Object); len(tasks) != 0 {
		t.Fatalf("a refused call left tasks behind: %v", tasks)
	}
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_WaitReturnsOnceTheTaskEnds(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude"), "sleepy": fakeRunner(t, "sleep")}))
	began := time.Now()
	done := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "quick", "wait_ms": 60000})
	if done["state"] != "done" {
		t.Fatalf("state = %v, want done", done["state"])
	}
	if elapsed := time.Since(began); elapsed > 20*time.Second {
		t.Fatalf("a task that ended at once held the call for %s", elapsed)
	}

	began = time.Now()
	running := h.ok(callerA, "delegate.run", map[string]any{"runner": "sleepy", "prompt": "slow", "wait_ms": 200})
	if running["state"] != "running" {
		t.Fatalf("state = %v, want running", running["state"])
	}
	if elapsed := time.Since(began); elapsed > 5*time.Second {
		t.Fatalf("wait_ms 200 held the call for %s", elapsed)
	}
	h.ok(callerA, "delegate.cancel", map[string]any{"task_id": running["task_id"]})
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_IdempotentRetryReturnsTheSameTask(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude")}))
	args := map[string]any{"runner": "claude", "prompt": "same", "idempotency_key": "job-1", "wait_ms": 20000}
	first := h.ok(callerA, "delegate.run", args)
	second := h.ok(callerA, "delegate.run", args)
	if first["task_id"] != second["task_id"] {
		t.Fatalf("retry started %v, want %v", second["task_id"], first["task_id"])
	}
	if second["state"] != "done" {
		t.Fatalf("retry of a finished task reports %v", second["state"])
	}
	if n := len(h.ok(callerA, "delegate.list", map[string]any{})["tasks"].([]Object)); n != 1 {
		t.Fatalf("%d tasks after a retry, want 1", n)
	}
	_, err := h.call(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "different", "idempotency_key": "job-1"})
	h.wantCode(err, "invalid_args")
	other := h.ok(callerB, "delegate.run", args)
	if other["task_id"] == first["task_id"] {
		t.Fatal("another caller reused the key of this caller")
	}
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_SaturationReportsInUseAndLimit(t *testing.T) {
	settings := testSettings(map[string]config.DelegateRunner{"sleepy": fakeRunner(t, "sleep")})
	settings.MaxConcurrent = 2
	h := newHarness(t, settings)
	args := func(prompt string) map[string]any {
		return map[string]any{"runner": "sleepy", "prompt": prompt, "wait_ms": 0}
	}
	a := h.ok(callerA, "delegate.run", args("a"))
	b := h.ok(callerA, "delegate.run", args("b"))
	_, err := h.call(callerA, "delegate.run", args("c"))
	h.wantCode(err, "delegate_saturated")
	if fieldOf(err, "in_use") != 2 || fieldOf(err, "limit") != 2 {
		t.Fatalf("in_use %v, limit %v; want 2 and 2", fieldOf(err, "in_use"), fieldOf(err, "limit"))
	}
	h.ok(callerA, "delegate.cancel", map[string]any{"task_id": a["task_id"]})
	c := h.ok(callerA, "delegate.run", args("c"))
	h.ok(callerA, "delegate.cancel", map[string]any{"task_id": b["task_id"]})
	h.ok(callerA, "delegate.cancel", map[string]any{"task_id": c["task_id"]})
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_TimeoutAboveMaxIsCappedAtMax(t *testing.T) {
	settings := testSettings(map[string]config.DelegateRunner{"sleepy": fakeRunner(t, "sleep")})
	settings.MaxTimeout = "500ms"
	h := newHarness(t, settings)
	began := time.Now()
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "sleepy", "prompt": "x", "timeout": "1h", "wait_ms": 20000})
	if out["state"] != "timeout" {
		t.Fatalf("state = %v, want timeout at the max_timeout cap (%v)", out["state"], out)
	}
	if elapsed := time.Since(began); elapsed > 15*time.Second {
		t.Fatalf("a 1h timeout held the call for %s, so the cap did not apply", elapsed)
	}
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_ErrorResultIsFailedEvenWithExitZero(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude-error")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "x", "wait_ms": 20000})
	if out["state"] != "failed" || out["exit_code"] != 0 || out["result"] != "permission denied" || out["session_id"] != "sess-err" {
		t.Fatalf("reply = %v, want failed with exit code 0 and the error result", out)
	}
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_NonZeroExitIsFailedWithItsStatus(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"exit": fakeRunner(t, "exit", "5")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "exit", "prompt": "x", "wait_ms": 20000})
	if out["state"] != "failed" || out["exit_code"] != 5 {
		t.Fatalf("reply = %v, want failed with exit code 5", out)
	}
	events, _ := h.finish(callerA, out["task_id"].(string))
	seen := map[string]bool{}
	for _, e := range events {
		if e["type"] == "raw" {
			seen[e["text"].(string)] = true
		}
	}
	if !seen["plain text before the failure"] || !seen["boom"] {
		t.Fatalf("output lines not kept as raw events: %v", events)
	}
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_TimeoutTerminatesTheProcessTree(t *testing.T) {
	heartbeat := filepath.Join(t.TempDir(), "beat.txt")
	killHeartbeat(t, heartbeat)
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"tree": fakeRunner(t, "tree", heartbeat)}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "tree", "prompt": "x", "timeout": "1s", "wait_ms": 60000})
	if out["state"] != "timeout" {
		t.Fatalf("state = %v, want timeout (%v)", out["state"], out)
	}
	assertStopped(t, heartbeat)
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_CancelEndsTheTaskAndIsIdempotent(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"sleepy": fakeRunner(t, "sleep"), "claude": fakeRunner(t, "claude")}))
	started := h.ok(callerA, "delegate.run", map[string]any{"runner": "sleepy", "prompt": "x", "wait_ms": 0})
	taskID := started["task_id"].(string)
	if started["state"] != "running" {
		t.Fatalf("state = %v, want running", started["state"])
	}
	if first := h.ok(callerA, "delegate.cancel", map[string]any{"task_id": taskID}); first["state"] != "cancelled" {
		t.Fatalf("cancel reports %v", first["state"])
	}
	if again := h.ok(callerA, "delegate.cancel", map[string]any{"task_id": taskID}); again["state"] != "cancelled" {
		t.Fatalf("second cancel reports %v", again["state"])
	}
	if _, last := h.finish(callerA, taskID); last["state"] != "cancelled" {
		t.Fatalf("output reports %v after cancel", last["state"])
	}

	done := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "q", "wait_ms": 20000})
	if after := h.ok(callerA, "delegate.cancel", map[string]any{"task_id": done["task_id"]}); after["state"] != "done" {
		t.Fatalf("cancel of a finished task reports %v, want done", after["state"])
	}
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_CancelTerminatesTheProcessTree(t *testing.T) {
	heartbeat := filepath.Join(t.TempDir(), "beat.txt")
	killHeartbeat(t, heartbeat)
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"tree": fakeRunner(t, "tree", heartbeat)}))
	started := h.ok(callerA, "delegate.run", map[string]any{"runner": "tree", "prompt": "x", "wait_ms": 0})
	eventually(t, "the grandchild heartbeat", func() bool { _, ok := heartbeatPID(heartbeat); return ok })

	out := h.ok(callerA, "delegate.cancel", map[string]any{"task_id": started["task_id"]})
	if out["state"] != "cancelled" {
		t.Fatalf("cancel reports %v, want cancelled", out["state"])
	}
	assertStopped(t, heartbeat)
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_OutputPagesEventsFromSeqOne(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "pages", "wait_ms": 20000})
	taskID := out["task_id"]

	first := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID, "cursor": 0, "limit": 2})
	events := first["events"].([]Object)
	if len(events) != 2 || events[0]["seq"] != uint64(1) || events[1]["seq"] != uint64(2) {
		t.Fatalf("first page = %v, want seq 1 and 2", events)
	}
	if first["next_cursor"] != int64(2) {
		t.Fatalf("next_cursor = %v, want 2", first["next_cursor"])
	}
	second := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID, "cursor": first["next_cursor"]})
	events = second["events"].([]Object)
	if len(events) != 2 || events[0]["seq"] != uint64(3) || second["eof"] != true {
		t.Fatalf("second page = %v (eof %v), want seq 3 and 4 with eof", events, second["eof"])
	}
	empty := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID, "cursor": 4})
	if len(empty["events"].([]Object)) != 0 || empty["next_cursor"] != int64(4) || empty["eof"] != true {
		t.Fatalf("page past the end = %v", empty)
	}
	for _, args := range []map[string]any{
		{"task_id": taskID, "cursor": -1},
		{"task_id": taskID, "limit": 0},
		{"task_id": taskID, "limit": 201},
	} {
		_, err := h.call(callerA, "delegate.output", args)
		h.wantCode(err, "invalid_args")
	}
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_OutputLongPollWakesOnNewEvent(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "drip", "200", "10000")}))
	started := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "x", "wait_ms": 0})
	taskID := started["task_id"]

	began := time.Now()
	out := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID, "cursor": 1, "wait_ms": 20000})
	elapsed := time.Since(began)
	events := out["events"].([]Object)
	if len(events) == 0 || events[0]["text"] != "second" {
		t.Fatalf("long poll returned %v", events)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("long poll took %s for an event due after 200ms", elapsed)
	}
	h.ok(callerA, "delegate.cancel", map[string]any{"task_id": taskID})
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_OtherCallerGetsNotFound(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "private", "wait_ms": 20000})
	taskID := out["task_id"]

	_, err := h.call(callerB, "delegate.output", map[string]any{"task_id": taskID})
	h.wantCode(err, "not_found")
	_, err = h.call(callerB, "delegate.cancel", map[string]any{"task_id": taskID})
	h.wantCode(err, "not_found")
	_, err = h.call(callerA, "delegate.output", map[string]any{"task_id": "t-0000"})
	h.wantCode(err, "not_found")
	h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID})
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_RetentionCapsFinishedTasksAtSixtyFour(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude")}))
	var first, last string
	for i := 0; i < 66; i++ {
		out := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": fmt.Sprint(i), "wait_ms": 20000})
		if i == 0 {
			first = out["task_id"].(string)
		}
		last = out["task_id"].(string)
	}
	_, err := h.call(callerA, "delegate.output", map[string]any{"task_id": first})
	h.wantCode(err, "not_found")
	h.ok(callerA, "delegate.output", map[string]any{"task_id": last})
	if n := len(h.ok(callerA, "delegate.list", map[string]any{})["tasks"].([]Object)); n != maxFinished {
		t.Fatalf("%d tasks retained, want %d", n, maxFinished)
	}
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_RetentionExpiresFinishedTasks(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude")}))
	h.settings.Retain = "50ms"
	old := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "old", "wait_ms": 20000})
	time.Sleep(100 * time.Millisecond)
	// No other task runs here: the call that asks for the expired task prunes it (CTR-DLG-03).
	_, err := h.call(callerA, "delegate.output", map[string]any{"task_id": old["task_id"]})
	h.wantCode(err, "not_found")
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_EventTextIsClippedAtACharacterBoundary(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "big")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "x", "wait_ms": 20000})
	events, _ := h.finish(callerA, out["task_id"].(string))
	var text string
	var truncated any
	for _, e := range events {
		if e["type"] == "assistant" {
			text, truncated = e["text"].(string), e["truncated"]
		}
	}
	// The text is "a" followed by 5000 two-byte characters. The 8 KiB cut falls inside one of them, so it moves back to
	// the start of that character: 8191 bytes.
	if truncated != true || len(text) != 8191 || !utf8.ValidString(text) {
		t.Fatalf("event of %d bytes, truncated %v; want 8191 bytes, truncated, valid UTF-8", len(text), truncated)
	}
}

// conformance: CTR-DLG-03
func Test_CTRDLG03_RunReportsEventSeqsAfterFinish(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "x", "wait_ms": 20000})
	events, last := h.finish(callerA, out["task_id"].(string))
	if n := int64(len(events)); last["next_cursor"] != n {
		t.Fatalf("next_cursor = %v, want the last seq %d", last["next_cursor"], n)
	}
	for i, e := range events {
		if e["seq"] != uint64(i+1) {
			t.Fatalf("event %d has seq %v", i, e["seq"])
		}
	}
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_EndRecordCarriesNoPromptOrReply(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "claude")}))
	const secret = "CONFIDENTIAL-PROMPT-TEXT"
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": secret, "wait_ms": 20000})
	eventually(t, "the end record", func() bool { return len(h.ended()) == 1 })
	end := h.ended()[0]
	if end.Caller != callerA || end.TaskID != out["task_id"] || end.Runner != "claude" || end.ResolvedPath != h.dir || end.Mode != "read" || end.State != "done" {
		t.Fatalf("end record = %+v", end)
	}
	if end.Duration < 0 {
		t.Fatalf("negative duration %s", end.Duration)
	}
	if strings.Contains(fmt.Sprintf("%+v", end), secret) {
		t.Fatal("the end record carries the prompt")
	}
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_CloseTerminatesRunningTasks(t *testing.T) {
	heartbeat := filepath.Join(t.TempDir(), "beat.txt")
	killHeartbeat(t, heartbeat)
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"tree": fakeRunner(t, "tree", heartbeat)}))
	started := h.ok(callerA, "delegate.run", map[string]any{"runner": "tree", "prompt": "x", "wait_ms": 0})
	taskID := started["task_id"]
	eventually(t, "the heartbeat process", func() bool { _, ok := heartbeatPID(heartbeat); return ok })

	h.m.Close()
	assertStopped(t, heartbeat)
	if out := h.ok(callerA, "delegate.output", map[string]any{"task_id": taskID}); out["state"] != "cancelled" {
		t.Fatalf("state after Close = %v, want cancelled", out["state"])
	}
	_, err := h.call(callerA, "delegate.run", map[string]any{"runner": "tree", "prompt": "later"})
	h.wantCode(err, "internal")
}

// conformance: CTR-DLG-06
func Test_CTRDLG06_CancelAllEndsRunningTasks(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"sleepy": fakeRunner(t, "sleep")}))
	started := h.ok(callerA, "delegate.run", map[string]any{"runner": "sleepy", "prompt": "x", "wait_ms": 0})
	h.m.CancelAll()
	if _, last := h.finish(callerA, started["task_id"].(string)); last["state"] != "cancelled" {
		t.Fatalf("state = %v after CancelAll, want cancelled", last["state"])
	}
}
