package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// The control view of the delegation is off and empty on a fresh node: no task, no session, and the built-in runners.
func Test_ControlDelegateStateIsOffByDefault(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	_, store := configuration(t)
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	st := a.DelegateState()
	if st.Enabled || st.AllowWrite || st.AllowSessions || st.MaxConcurrent != 2 || st.Running != 0 {
		t.Fatalf("%+v", st)
	}
	if len(st.Tasks) != 0 || len(st.Sessions) != 0 || len(st.Runners) != 2 || st.Runners[0].Name != "claude" || st.Runners[1].Name != "codex" {
		t.Fatalf("%+v", st)
	}
}

// The control view lists a finished task with its caller and session id, and never carries the prompt or the reply.
func Test_ControlDelegateStateShowsTasksWithoutPromptOrReply(t *testing.T) {
	a, _, client := startDelegate(t, map[string]any{"enabled": true, "runners": map[string]any{"claude": fakeRunnerConfig(t, "claude")}})
	const secret = "PROMPT-SECRET-4452"
	out := object(t, tool(t, client, "delegate.run", map[string]any{"runner": "claude", "prompt": secret, "wait_ms": 20000}))
	if out["state"] != "done" {
		t.Fatal("delegate.run reply", out)
	}
	st := a.DelegateState()
	if !st.Enabled || len(st.Tasks) != 1 {
		t.Fatalf("%+v", st)
	}
	task := st.Tasks[0]
	if task.TaskID != out["task_id"] || task.State != "done" || task.Caller == "" || task.Runner != "claude" || task.StartedAt.IsZero() {
		t.Fatalf("%+v", task)
	}
	if task.SessionID != "sess-1" {
		t.Fatalf("session id %q", task.SessionID)
	}
	var claude *v2.DelegateRunner
	for i := range st.Runners {
		if st.Runners[i].Name == "claude" {
			claude = &st.Runners[i]
		}
	}
	if claude == nil || !claude.Available || claude.Path == "" {
		t.Fatalf("runners %+v", st.Runners)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "answer:") {
		t.Fatalf("the control view carries the prompt or the reply: %s", raw)
	}
}

// CancelDelegateTask cancels a running task started by any caller and leaves finished tasks intact.
func Test_ControlCancelDelegateTask(t *testing.T) {
	a, _, client := startDelegate(t, map[string]any{"enabled": true, "runners": map[string]any{"sleepy": fakeRunnerConfig(t, "sleep")}})
	out := object(t, tool(t, client, "delegate.run", map[string]any{"runner": "sleepy", "prompt": "wait", "wait_ms": 0}))
	taskID, _ := out["task_id"].(string)
	if taskID == "" {
		t.Fatalf("missing task_id: %v", out)
	}
	res, ok := a.CancelDelegateTask(context.Background(), taskID)
	if !ok || res.TaskID != taskID || res.State != "cancelled" {
		t.Fatalf("CancelDelegateTask = (%+v, %v)", res, ok)
	}
	st := a.DelegateState()
	if st.Running != 0 || len(st.Tasks) != 1 || st.Tasks[0].State != "cancelled" {
		t.Fatalf("DelegateState after cancel = %+v", st)
	}
	// Missing task returns false.
	if _, ok := a.CancelDelegateTask(context.Background(), "t-missing"); ok {
		t.Fatal("CancelDelegateTask on missing task returned ok=true")
	}
}
