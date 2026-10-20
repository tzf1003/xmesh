package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func executionLogger(t *testing.T) (*Logger, string) {
	t.Helper()
	dir := t.TempDir()
	l, err := New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l, dir
}

func awaitExecution(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("execution state did not converge")
}

func TestExecutionMetadataAndAuditCorrelation(t *testing.T) {
	l, dir := executionLogger(t)
	x := l.BeginExecution(context.Background(), "node/session", "client", "target", "ui.script", "gateway")
	queued, limits := l.Executions()
	if len(queued) != 1 || queued[0].State != "queued" || limits.Active != 1 {
		t.Fatalf("queued snapshot: %+v %+v", queued, limits)
	}
	ctx := x.Context(context.Background())
	SetExecutionState(ctx, "running")
	running, _ := l.Executions()
	if running[0].ID != queued[0].ID || running[0].State != "running" || running[0].StartedAt != queued[0].StartedAt {
		t.Fatalf("unstable execution: %+v", running)
	}
	args := json.RawMessage(`{"protocol_version":1,"code":"PRIVATE_SCRIPT_BODY","env":{"TOKEN":"PRIVATE_ENV_VALUE"}}`)
	out := map[string]any{"error": "unauthorized", "message": "PRIVATE_ERROR_BODY"}
	x.FinishCall(args, out, true)
	if err := l.RecordExecution(x, "panel", "grant-hash", "node/session", "client", "target", "ui.script", args, out, true, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if active, _ := l.Executions(); len(active) != 0 {
		t.Fatal("finished error remained live", active)
	}
	rows := records(t, dir)
	if len(rows) != 1 || rows[0]["execution_id"] != x.row.ID || rows[0]["layer"] != "gateway" || rows[0]["started_at"] == nil || rows[0]["event"] != "authz_deny" {
		t.Fatal("audit correlation missing", rows)
	}
	for _, value := range []any{queued, running, rows} {
		raw, _ := json.Marshal(value)
		for _, secret := range []string{"PRIVATE_SCRIPT_BODY", "PRIVATE_ENV_VALUE", "PRIVATE_ERROR_BODY"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("body appeared in execution metadata")
			}
		}
	}
}

func TestExecutionLimitPreservesActiveEntries(t *testing.T) {
	l, _ := executionLogger(t)
	var first *Execution
	for n := 0; n < maxExecutions+3; n++ {
		x := l.BeginExecution(context.Background(), "node/session", "", "target", "fs.read", "agent")
		if n == 0 {
			first = x
		}
	}
	active, limits := l.Executions()
	if len(active) != maxExecutions || limits.Active != maxExecutions || limits.RejectedTotal != 3 {
		t.Fatalf("limits: %+v active=%d", limits, len(active))
	}
	seen := false
	for _, row := range active {
		seen = seen || row.ID == first.row.ID
	}
	if !seen {
		t.Fatal("active call evicted")
	}
	first.FinishCall(json.RawMessage(`{}`), nil, false)
	l.BeginExecution(context.Background(), "node/session", "", "target", "sys.info", "agent")
	_, limits = l.Executions()
	if limits.Active != maxExecutions || limits.RejectedTotal != 3 {
		t.Fatal(limits)
	}
}

func TestExecutionAcceptedTaskCompletesThroughPoll(t *testing.T) {
	l, dir := executionLogger(t)
	x := l.BeginExecution(context.Background(), "node/session", "client", "target", "ui.run", "agent")
	SetResultPoll(x.Context(context.Background()), func(ctx context.Context, runID string) (any, error) {
		if runID != "r_task" {
			t.Errorf("wrong run: %q", runID)
		}
		return map[string]any{"protocol_version": 1, "run_id": runID, "status": "passed", "steps": []any{"PRIVATE_RESULT_BODY"}}, nil
	})
	args := json.RawMessage(`{"protocol_version":1,"steps":[{"value":"PRIVATE_ARGUMENT_BODY"}]}`)
	accepted := map[string]any{"run_id": "r_task", "status": "accepted"}
	x.FinishCall(args, accepted, false)
	if err := l.RecordExecution(x, "", "", "node/session", "client", "target", "ui.run", args, accepted, false, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	active, _ := l.Executions()
	if len(active) != 1 || active[0].State != "async" || active[0].TaskStatus != "accepted" {
		t.Fatalf("accepted task was treated as complete: %+v", active)
	}
	awaitExecution(t, func() bool { active, _ := l.Executions(); return len(active) == 0 })
	rows := records(t, dir)
	if len(rows) != 2 || rows[1]["event"] != "task_complete" || rows[1]["task_status"] != "passed" || rows[1]["execution_id"] != rows[0]["execution_id"] {
		t.Fatal("task history missing", rows)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "PRIVATE_") {
		t.Fatal("task body appeared in audit")
	}
}

func TestExecutionPollFailureRetainsUnknownAndRecovers(t *testing.T) {
	l, _ := executionLogger(t)
	x := l.BeginExecution(context.Background(), "node/session", "", "target", "ui.script", "gateway")
	var calls atomic.Int32
	SetResultPoll(x.Context(context.Background()), func(ctx context.Context, runID string) (any, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("PRIVATE_POLL_ERROR")
		}
		return map[string]any{"protocol_version": 1, "run_id": runID, "status": "cancelled"}, nil
	})
	x.FinishCall(json.RawMessage(`{"protocol_version":1}`), map[string]any{"run_id": "r_task", "status": "accepted"}, false)
	awaitExecution(t, func() bool {
		active, _ := l.Executions()
		return len(active) == 1 && active[0].State == "unknown" && active[0].TaskStatus == "unknown"
	})
	awaitExecution(t, func() bool { active, _ := l.Executions(); return len(active) == 0 })
}

func TestExecutionPollingStopsWithLifecycleAndLogger(t *testing.T) {
	for _, shutdown := range []string{"receiver", "logger"} {
		t.Run(shutdown, func(t *testing.T) {
			l, _ := executionLogger(t)
			lifecycle, cancel := context.WithCancel(context.Background())
			defer cancel()
			x := l.BeginExecution(lifecycle, "node/session", "", "target", "ui.run", "agent")
			started := make(chan struct{})
			exited := make(chan struct{})
			SetResultPoll(x.Context(context.Background()), func(ctx context.Context, _ string) (any, error) {
				close(started)
				<-ctx.Done()
				close(exited)
				return nil, ctx.Err()
			})
			x.FinishCall(json.RawMessage(`{"protocol_version":1}`), map[string]any{"run_id": "r_task", "status": "accepted"}, false)
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("poll did not start")
			}
			if shutdown == "receiver" {
				cancel()
			} else {
				l.Close()
			}
			select {
			case <-exited:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not cancel in-flight poll")
			}
			awaitExecution(t, func() bool { active, _ := l.Executions(); return len(active) == 1 && active[0].State == "unknown" })
		})
	}
}

func TestExecutionAsyncDetectionRequiresProtocolAndKnownStatus(t *testing.T) {
	l, _ := executionLogger(t)
	for _, testcase := range []struct{ tool, args, status string }{
		{"ui.run", `{}`, "accepted"},
		{"exec.run", `{"protocol_version":1}`, "ok"},
		{"ui.open", `{"protocol_version":1}`, "accepted"},
		{"ui.script", `{"protocol_version":1}`, "unrecognized"},
	} {
		x := l.BeginExecution(context.Background(), "node/session", "", "target", testcase.tool, "agent")
		x.FinishCall(json.RawMessage(testcase.args), map[string]any{"run_id": "r_task", "status": testcase.status}, false)
	}
	if active, _ := l.Executions(); len(active) != 0 {
		t.Fatal("unrelated results became tasks", active)
	}
}
