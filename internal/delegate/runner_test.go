package delegate

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

// kinds lists the event types of a parsed line, in order.
func kinds(p parsed) []string {
	var out []string
	for _, e := range p.events {
		out = append(out, e.typ)
	}
	return out
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_ClaudeMappingKeepsSessionAnswerAndTools(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		types   []string
		text    string // the text of the only event, when types has one
		session string
		answer  string
		hasAns  bool
		failed  bool
	}{
		{name: "init", line: `{"type":"system","subtype":"init","session_id":"s-1"}`, types: []string{"system"}, session: "s-1"},
		{name: "other system", line: `{"type":"system","subtype":"hook"}`, types: []string{"system"}},
		{name: "assistant text", line: `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`, types: []string{"assistant"}, text: "hi"},
		{name: "tool use", line: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Read","input":{}}]}}`, types: []string{"tool"}},
		{name: "tool result", line: `{"type":"user","message":{"content":[{"type":"tool_result","content":"body"}]}}`, types: []string{"tool"}, text: "body"},
		{name: "final result", line: `{"type":"result","subtype":"success","is_error":false,"result":"done"}`, answer: "done", hasAns: true},
		{name: "error result", line: `{"type":"result","subtype":"error","is_error":true,"result":"denied"}`, answer: "denied", hasAns: true, failed: true},
		{name: "unknown type", line: `{"type":"stream_event"}`, types: []string{"raw"}},
		{name: "not JSON", line: `plain words`, types: []string{"raw"}, text: "plain words"},
	}
	for _, c := range cases {
		p := parseLine("claude", []byte(c.line))
		if got := kinds(p); !slices.Equal(got, c.types) {
			t.Fatalf("%s: event types %q, want %q", c.name, got, c.types)
		}
		if len(c.types) == 1 && c.text != "" && p.events[0].text != c.text {
			t.Fatalf("%s: event text %q, want %q", c.name, p.events[0].text, c.text)
		}
		if p.session != c.session || p.answer != c.answer || p.hasAnswer != c.hasAns || p.failed != c.failed {
			t.Fatalf("%s: session %q answer %q hasAnswer %v failed %v", c.name, p.session, p.answer, p.hasAnswer, p.failed)
		}
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_CodexMappingKeepsThreadAndLastMessage(t *testing.T) {
	started := parseLine("codex", []byte(`{"type":"thread.started","thread_id":"thr-1"}`))
	if started.session != "thr-1" || !slices.Equal(kinds(started), []string{"system"}) {
		t.Fatalf("thread.started: session %q, events %q", started.session, kinds(started))
	}
	command := parseLine("codex", []byte(`{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}`))
	if !slices.Equal(kinds(command), []string{"tool"}) {
		t.Fatalf("command_execution: events %q, want tool", kinds(command))
	}
	draft := parseLine("codex", []byte(`{"type":"item.completed","item":{"type":"agent_message","text":"first draft"}}`))
	if !slices.Equal(kinds(draft), []string{"assistant"}) || !draft.hasAnswer || draft.answer != "first draft" {
		t.Fatalf("agent_message: events %q, answer %q", kinds(draft), draft.answer)
	}
	end := parseLine("codex", []byte(`{"type":"turn.completed"}`))
	if !slices.Equal(kinds(end), []string{"raw"}) || end.hasAnswer {
		t.Fatalf("turn.completed: events %q, hasAnswer %v", kinds(end), end.hasAnswer)
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_GenericRuleReadsOnlyAStringResult(t *testing.T) {
	final := parseLine("mytool", []byte(`{"type":"result","result":"fin"}`))
	if !final.hasAnswer || final.answer != "fin" || len(final.events) != 0 {
		t.Fatalf("result line: answer %q, hasAnswer %v, events %q", final.answer, final.hasAnswer, kinds(final))
	}
	for _, line := range []string{
		`{"type":"result","result":5}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"x"}]}}`,
		`[1,2]`,
	} {
		p := parseLine("mytool", []byte(line))
		if p.hasAnswer || !slices.Equal(kinds(p), []string{"raw"}) {
			t.Fatalf("%s: events %q, hasAnswer %v; want one raw event and no answer", line, kinds(p), p.hasAnswer)
		}
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_NonJSONLinesAreRawEvents(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"raw": fakeRunner(t, "raw")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "raw", "prompt": "x", "wait_ms": 20000})
	if out["state"] != "done" || out["result"] != "done" {
		t.Fatalf("reply = %v, want done with the final result", out)
	}
	events, _ := h.finish(callerA, out["task_id"].(string))
	var got []string
	for _, e := range events {
		if e["type"] != "raw" {
			t.Fatalf("event %v is not raw; the generic rule keeps every other line raw", e)
		}
		got = append(got, e["text"].(string))
	}
	if want := []string{"not json at all", "[1,2]", `{"type":"mystery","x":1}`}; !slices.Equal(got, want) {
		t.Fatalf("raw events = %q, want %q", got, want)
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_FallbackReplyJoinsAssistantText(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"claude": fakeRunner(t, "assistants")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "claude", "prompt": "x", "wait_ms": 20000})
	if out["state"] != "done" || out["result"] != "Hello" {
		t.Fatalf("reply = %v, want done with the assistant text joined", out)
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_CodexRunnerEndToEnd(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"codex": fakeRunner(t, "codex")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "codex", "prompt": "fix", "wait_ms": 20000})
	if out["state"] != "done" || out["result"] != "codex:fix" || out["session_id"] != "thr-1" {
		t.Fatalf("reply = %v", out)
	}
	events, _ := h.finish(callerA, out["task_id"].(string))
	var types []string
	for _, e := range events {
		types = append(types, e["type"].(string))
	}
	if want := []string{"system", "tool", "assistant", "assistant", "raw"}; !slices.Equal(types, want) {
		t.Fatalf("event types = %q, want %q", types, want)
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_ArgvAssembly(t *testing.T) {
	claude := builtinRunners["claude"]
	codex := builtinRunners["codex"]
	cases := []struct {
		name string
		r    runner
		mode string
		sid  string
		want []string
	}{
		{"claude read", claude, "read", "", []string{"claude", "-p", "--output-format", "stream-json", "--verbose",
			"--permission-mode", "dontAsk", "--permission-prompts", "none", "--allowedTools", "Read,Glob,Grep"}},
		{"claude write", claude, "write", "", []string{"claude", "-p", "--output-format", "stream-json", "--verbose",
			"--permission-mode", "acceptEdits", "--permission-prompts", "none", "--allowedTools", "Read,Glob,Grep,Edit,Write,Bash"}},
		{"claude resume", claude, "read", "sess-1", []string{"claude", "-p", "--output-format", "stream-json", "--verbose",
			"--resume", "sess-1", "--permission-mode", "dontAsk", "--permission-prompts", "none", "--allowedTools", "Read,Glob,Grep"}},
		{"codex read", codex, "read", "", []string{"codex", "exec", "--json", "--skip-git-repo-check", "--sandbox", "read-only", "-"}},
		{"codex write", codex, "write", "", []string{"codex", "exec", "--json", "--skip-git-repo-check", "--sandbox", "workspace-write", "-"}},
		{"codex resume", codex, "write", "thr-9", []string{"codex", "exec", "resume", "thr-9", "--json", "--skip-git-repo-check", "--sandbox", "workspace-write", "-"}},
		{"no trailing dash keeps the order", runner{cmd: "x", args: []string{"a"}, readArgs: []string{"r"}}, "read", "", []string{"x", "a", "r"}},
	}
	for _, c := range cases {
		if got := c.r.argv(c.mode, c.sid); !slices.Equal(got, c.want) {
			t.Fatalf("%s: argv = %q, want %q", c.name, got, c.want)
		}
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_ConfigOverridesOnlyTheFieldsItNames(t *testing.T) {
	cfg := config.Delegate{Runners: map[string]config.DelegateRunner{
		"claude": {Cmd: "/opt/bin/claude", ReadArgs: []string{}},
		"mytool": {Cmd: "mytool", Args: []string{"run"}},
		"bare":   {Args: []string{"x"}},
	}}
	claude, ok := runnerFor(cfg, "claude")
	if !ok || claude.cmd != "/opt/bin/claude" || claude.kind != "claude" {
		t.Fatalf("claude = %+v, ok %v", claude, ok)
	}
	if !slices.Equal(claude.args, builtinRunners["claude"].args) || !slices.Equal(claude.writeArgs, builtinRunners["claude"].writeArgs) {
		t.Fatal("fields the override does not name must keep the built-in value")
	}
	if len(claude.readArgs) != 0 {
		t.Fatalf("an empty list must clear the read args, got %q", claude.readArgs)
	}
	mine, ok := runnerFor(cfg, "mytool")
	if !ok || mine.kind != "generic" || mine.cmd != "mytool" || !slices.Equal(mine.args, []string{"run"}) {
		t.Fatalf("mytool = %+v, ok %v", mine, ok)
	}
	if _, ok := runnerFor(cfg, "bare"); ok {
		t.Fatal("a runner without cmd that no built-in defines must not resolve")
	}
	if _, ok := runnerFor(cfg, "nobody"); ok {
		t.Fatal("an unknown runner must not resolve")
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_RunnerListShowsAvailability(t *testing.T) {
	cfg := config.Delegate{Runners: map[string]config.DelegateRunner{
		"here": {Cmd: testExe(t)},
		"gone": {Cmd: "xmesh-no-such-runner"},
	}}
	byName := map[string]Object{}
	for _, row := range runnerViews(cfg, t.TempDir()) {
		byName[row["name"].(string)] = row
	}
	for _, name := range []string{"claude", "codex", "here", "gone"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("runner %s is not listed", name)
		}
	}
	if byName["here"]["available"] != true || byName["here"]["path"] == nil || byName["here"]["path"] == "" {
		t.Fatalf("here = %v, want available with a path", byName["here"])
	}
	if byName["gone"]["available"] != false {
		t.Fatalf("gone = %v, want unavailable", byName["gone"])
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_ChildSeesTheDelegateMarker(t *testing.T) {
	h := newHarness(t, testSettings(map[string]config.DelegateRunner{"env": fakeRunner(t, "env")}))
	out := h.ok(callerA, "delegate.run", map[string]any{"runner": "env", "prompt": "x", "wait_ms": 20000})
	if out["result"] != "XMESH_DELEGATE=1" {
		t.Fatalf("result = %v, want XMESH_DELEGATE=1", out["result"])
	}
}

// conformance: CTR-DLG-05
func Test_CTRDLG05_ParseKeepsLinesLongerThanTheBuffer(t *testing.T) {
	long := strings.Repeat("y", 70<<10)
	var got []string
	scanLines(strings.NewReader("first\r\n\n"+long+"\nlast"), maxLineBytes, func(line []byte) {
		got = append(got, string(line))
	})
	if len(got) != 3 || got[0] != "first" || got[1] != long || got[2] != "last" {
		t.Fatalf("scanLines returned %d lines; blank lines and CRLF must be handled", len(got))
	}
}

// conformance: CTR-DLG-02
func Test_CTRDLG02_StartFailureIsInternalAndReleasesTheSlot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an executable without a program format is started differently on Windows")
	}
	// The file is executable, so the runner is found, but it is no program, so the start fails.
	bad := filepath.Join(t.TempDir(), "not-a-program")
	if err := os.WriteFile(bad, []byte("this is not a program\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := testSettings(map[string]config.DelegateRunner{"claude": {Cmd: bad}})
	settings.MaxConcurrent = 1
	h := newHarness(t, settings)
	args := map[string]any{"runner": "claude", "prompt": "x", "wait_ms": 0}
	_, err := h.call(callerA, "delegate.run", args)
	h.wantCode(err, "internal")
	// The failed start gave its slot back, so the next start is not refused as saturated.
	_, err = h.call(callerA, "delegate.run", args)
	h.wantCode(err, "internal")
	if n := len(h.ok(callerA, "delegate.list", map[string]any{})["tasks"].([]Object)); n != 0 {
		t.Fatalf("%d tasks remain after failed starts, want none", n)
	}
}
