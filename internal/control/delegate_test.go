package control

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// fakeDelegate is an agent with one running task of another caller, one finished task, and two runners.
type fakeDelegate struct {
	mu    sync.Mutex
	tasks map[string]string
}

func newFakeDelegate() *fakeDelegate {
	return &fakeDelegate{
		tasks: map[string]string{
			"t-5e0a7c41b9d24f6e8a13c0d7f2e4b916": "running",
			"t-19ab3e02c5f84d7aa6e1b9d0c3f27a48": "done",
		},
	}
}

func (d *fakeDelegate) DelegateState() v2.DelegateState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return v2.DelegateState{
		Enabled: true, AllowWrite: true, AllowSessions: false, MaxConcurrent: 2, Running: func() int {
			if d.tasks["t-5e0a7c41b9d24f6e8a13c0d7f2e4b916"] == "running" {
				return 1
			}
			return 0
		}(),
		Runners:  []v2.DelegateRunner{{Name: "claude", Available: true, Path: "/usr/bin/claude"}, {Name: "codex", Available: false}},
		Sessions: []v2.DelegateSession{},
		Tasks: []v2.DelegateTask{
			{
				TaskID: "t-5e0a7c41b9d24f6e8a13c0d7f2e4b916", Caller: "node-b/bob", Runner: "claude", State: d.tasks["t-5e0a7c41b9d24f6e8a13c0d7f2e4b916"], CWD: "/w/a",
				StartedAt: v2.Now(), DurationMS: 5,
			},
			{
				TaskID: "t-19ab3e02c5f84d7aa6e1b9d0c3f27a48", Caller: "node-b/bob", Runner: "codex", State: d.tasks["t-19ab3e02c5f84d7aa6e1b9d0c3f27a48"], CWD: "/w/b",
				StartedAt: v2.Now(), DurationMS: 12,
			},
		},
	}
}

func (d *fakeDelegate) CancelDelegateTask(_ context.Context, taskID string) (v2.DelegateTaskCancelResponse, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state, ok := d.tasks[taskID]
	if !ok {
		return v2.DelegateTaskCancelResponse{}, false
	}
	if state == "running" {
		state = "cancelled"
		d.tasks[taskID] = state
	}
	return v2.DelegateTaskCancelResponse{TaskID: taskID, State: state}, true
}

// conformance: CTR-CTL-12
//
// A node without an agent does not declare the delegate capability, and the delegate routes answer not_implemented.
// Each fixture is a test of its own: on Windows the control pipe name is per node and user, so two servers in one test collide.
func TestDelegateRouteAbsentWithoutAgent(t *testing.T) {
	f := newFixture(t)
	if f.server.hasCapability(v2.CapDelegate) {
		t.Fatal("delegate capability declared without an agent")
	}
	apiError(t, f.do("GET", "/v2/delegate", nil, nil), v2.ErrNotImplemented)
	apiError(t, f.do("POST", "/v2/delegate/tasks/t-5e0a7c41b9d24f6e8a13c0d7f2e4b916/cancel", nil, nil), v2.ErrNotImplemented)
}

// conformance: CTR-CTL-12
//
// A node with an agent declares the delegate capability, serves DelegateState, and cancels a task by id.
func TestDelegateRouteServedByAgent(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Delegate = newFakeDelegate() })
	if !f.server.hasCapability(v2.CapDelegate) {
		t.Fatal("delegate capability missing with an agent")
	}
	f.ok("GET", "/v2/delegate", nil, "DelegateState")

	apiError(t, f.do("POST", "/v2/delegate/tasks/bad!id/cancel", nil, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v2/delegate/tasks/t-missing/cancel", nil, nil), v2.ErrNotFound)

	var res v2.DelegateTaskCancelResponse
	if err := json.Unmarshal(f.ok("POST", "/v2/delegate/tasks/t-5e0a7c41b9d24f6e8a13c0d7f2e4b916/cancel", nil, "DelegateTaskCancelResponse").Body, &res); err != nil {
		t.Fatal(err)
	}
	if res.TaskID != "t-5e0a7c41b9d24f6e8a13c0d7f2e4b916" || res.State != "cancelled" {
		t.Fatalf("cancel response = %+v", res)
	}

	// Idempotent on an already finished task: state stays done.
	if err := json.Unmarshal(f.ok("POST", "/v2/delegate/tasks/t-19ab3e02c5f84d7aa6e1b9d0c3f27a48/cancel", nil, "DelegateTaskCancelResponse").Body, &res); err != nil {
		t.Fatal(err)
	}
	if res.TaskID != "t-19ab3e02c5f84d7aa6e1b9d0c3f27a48" || res.State != "done" {
		t.Fatalf("idempotent cancel response = %+v", res)
	}
}
