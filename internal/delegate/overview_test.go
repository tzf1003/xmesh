package delegate

import (
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
)

// Overview lists every caller's tasks, newest first, counts the slots in use, and lists sessions only when they are
// allowed. It needs no cwd resolver for the tasks.
func TestOverviewListsEveryCallersTasks(t *testing.T) {
	m := New(nil)
	now := time.Now()
	m.tasks["t-old"] = &task{id: "t-old", caller: callerB, runner: "codex", cwd: "/w/b", started: now.Add(-time.Hour), ended: now.Add(-time.Hour + 2*time.Second), finished: true, state: stateDone}
	m.tasks["t-new"] = &task{id: "t-new", caller: callerA, runner: "claude", cwd: "/w/a", started: now.Add(-time.Minute), sessionID: "s-1", state: stateRunning}
	m.running = 1

	view := m.Overview(config.Delegate{Enabled: true}, nil)
	if view.Running != 1 {
		t.Fatalf("running = %d", view.Running)
	}
	if len(view.Tasks) != 2 || view.Tasks[0].ID != "t-new" || view.Tasks[1].ID != "t-old" {
		t.Fatalf("tasks not newest first: %+v", view.Tasks)
	}
	if got := view.Tasks[0]; got.Caller != callerA || got.State != stateRunning || got.SessionID != "s-1" || got.Duration < time.Minute {
		t.Fatalf("running task: %+v", got)
	}
	if got := view.Tasks[1]; got.Caller != callerB || got.State != stateDone || got.Duration != 2*time.Second {
		t.Fatalf("finished task: %+v", got)
	}
	if len(view.Sessions) != 0 {
		t.Fatalf("sessions listed without a resolver: %+v", view.Sessions)
	}
	if len(view.Runners) != 2 || view.Runners[0].Name != "claude" || view.Runners[1].Name != "codex" {
		t.Fatalf("runners: %+v", view.Runners)
	}
}
