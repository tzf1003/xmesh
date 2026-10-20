package audit

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// The tracker bounds retained metadata and polling workers together. Calls that
// exceed this limit still execute and produce audit history; the control view
// exposes the missed tracking admissions through rejected_total.
const maxExecutions = 256

type executionContextKey struct{}

// ResultPoll reads one UI MCP result using the transport and caller that
// accepted the run. Implementations must honor context cancellation.
type ResultPoll func(context.Context, string) (any, error)

// Execution retains only call metadata. Request and result bodies are inspected
// transiently and are never stored in the live view or the polling worker.
type Execution struct {
	mu          sync.Mutex
	log         *Logger
	row         v2.ToolExecution
	ctx         context.Context
	poll        ResultPoll
	pollAllowed func() bool
	tracked     bool
	closed      bool
}

// BeginExecution starts before admission and authorization checks. Lifecycle
// must be cancelled when the receiving Agent or Gateway closes.
func (l *Logger) BeginExecution(lifecycle context.Context, caller, client, device, tool, layer string) *Execution {
	now := v2.Now()
	x := &Execution{log: l, ctx: lifecycle, row: v2.ToolExecution{ID: "x-" + rand.Text(), Caller: boundedMetadata(caller, 512), OAuthClient: boundedMetadata(client, 256), Device: boundedMetadata(device, 128), Tool: boundedMetadata(tool, 256), Layer: layer, State: "queued", StartedAt: now, UpdatedAt: now}}
	l.executionMu.Lock()
	if l.executions == nil {
		l.executions = make(map[string]*Execution)
	}
	if !l.executionsClosed && len(l.executions) < maxExecutions {
		x.tracked = true
		l.executions[x.row.ID] = x
	} else {
		l.executionRejected++
	}
	l.executionMu.Unlock()
	return x
}

func (x *Execution) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, executionContextKey{}, x)
}

// SetResultPoll binds result monitoring to the exact backend connection used
// for this call. The callback receives only the opaque run identifier.
func SetResultPoll(ctx context.Context, poll ResultPoll) {
	if x, ok := ctx.Value(executionContextKey{}).(*Execution); ok {
		x.mu.Lock()
		x.poll = poll
		x.mu.Unlock()
	}
}

// SetResultPollAuthorization rechecks the owning Agent's current caller and
// tool grants before reading a background result. Losing authorization ends
// this monitor; a disconnected session cannot acquire its old identity again.
func SetResultPollAuthorization(ctx context.Context, allowed func() bool) {
	if x, ok := ctx.Value(executionContextKey{}).(*Execution); ok {
		x.mu.Lock()
		x.pollAllowed = allowed
		x.mu.Unlock()
	}
}

// SetExecutionState distinguishes admission/backend waiting from actual work.
func SetExecutionState(ctx context.Context, state string) {
	if x, ok := ctx.Value(executionContextKey{}).(*Execution); ok {
		x.mu.Lock()
		if !x.closed && (state == "queued" || state == "running") {
			x.row.State, x.row.UpdatedAt = state, v2.Now()
		}
		x.mu.Unlock()
	}
}

func (x *Execution) SetTarget(caller, device, tool string) {
	x.mu.Lock()
	x.row.Caller, x.row.Device, x.row.Tool = boundedMetadata(caller, 512), boundedMetadata(device, 128), boundedMetadata(tool, 256)
	x.mu.Unlock()
}

func SetExecutionTarget(ctx context.Context, caller, device, tool string) {
	if x, ok := ctx.Value(executionContextKey{}).(*Execution); ok {
		x.SetTarget(caller, device, tool)
	}
}

func boundedMetadata(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func (x *Execution) snapshot() v2.ToolExecution {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.row
}

// FinishCall records an accepted UI task separately from the tools/call
// response. An accepted response is not evidence of the task's final outcome.
func (x *Execution) FinishCall(args json.RawMessage, result any, isError bool) {
	var input struct {
		Protocol int `json:"protocol_version"`
	}
	_ = json.Unmarshal(args, &input)
	x.mu.Lock()
	local := x.row.Tool[strings.LastIndex(x.row.Tool, "/")+1:]
	_, suffix, _ := strings.Cut(local, ".")
	isTask := !isError && input.Protocol == 1 && (suffix == "run" || suffix == "script")
	if !isTask {
		x.closed = true
		x.mu.Unlock()
		x.remove()
		return
	}
	meta, valid := taskMetadata(result, false)
	if !valid {
		x.closed = true
		x.mu.Unlock()
		x.remove()
		return
	}
	x.row.RunID, x.row.TaskStatus, x.row.UpdatedAt = meta.RunID, meta.Status, v2.Now()
	if taskTerminal(meta.Status) && !meta.OutcomeUnknown {
		x.closed = true
		x.mu.Unlock()
		x.remove()
		return
	}
	x.row.State = "async"
	if meta.OutcomeUnknown || x.poll == nil || !x.tracked {
		x.row.State, x.row.TaskStatus = "unknown", "unknown"
	}
	monitor := x.poll != nil && x.tracked
	x.mu.Unlock()
	if monitor {
		x.log.executionMu.Lock()
		if x.log.executionsClosed {
			x.log.executionMu.Unlock()
			x.markUnknown()
			return
		}
		x.log.executionWorkers.Add(1)
		x.log.executionMu.Unlock()
		go x.monitor()
	}
}

type taskMeta struct {
	Protocol       int    `json:"protocol_version"`
	RunID          string `json:"run_id"`
	Status         string `json:"status"`
	OutcomeUnknown bool   `json:"outcome_unknown"`
}

func taskMetadata(result any, requireProtocol bool) (taskMeta, bool) {
	var meta taskMeta
	b, err := json.Marshal(result)
	if err != nil || json.Unmarshal(b, &meta) != nil || len(meta.RunID) == 0 || len(meta.RunID) > 128 || requireProtocol && meta.Protocol != 1 {
		return meta, false
	}
	// UI MCP run identifiers are opaque, bounded printable identifiers. Do not
	// admit arbitrary tool output into the metadata view.
	for _, c := range meta.RunID {
		if c < '!' || c > '~' {
			return meta, false
		}
	}
	switch meta.Status {
	case "accepted", "running", "passed", "failed", "cancelled", "timed_out", "interrupted":
		return meta, true
	}
	return meta, false
}

func taskTerminal(status string) bool {
	return status != "accepted" && status != "running"
}

func (x *Execution) remove() {
	x.log.executionMu.Lock()
	delete(x.log.executions, x.row.ID)
	x.log.executionMu.Unlock()
}

func (x *Execution) markUnknown() {
	x.mu.Lock()
	x.row.State, x.row.TaskStatus, x.row.UpdatedAt = "unknown", "unknown", v2.Now()
	x.mu.Unlock()
}

// stopTracking retires a monitor whose caller can no longer read the result.
// Its audit event describes the tracking outcome while the task outcome stays
// unknown. Retiring the live entry makes its admission slot reusable.
func (x *Execution) stopTracking(reason string) {
	x.mu.Lock()
	x.closed = true
	x.row.State, x.row.TaskStatus, x.row.UpdatedAt = "unknown", "unknown", v2.Now()
	row := x.row
	x.mu.Unlock()
	_ = x.log.Write(taskRecord(row, "task_untracked", "error", reason))
	x.remove()
}

func taskRecord(row v2.ToolExecution, event, status string, code any) map[string]any {
	record := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "event": event, "caller": row.Caller, "device": row.Device, "tool": row.Tool, "status": status, "error": code, "duration_ms": max(int64(0), time.Since(row.StartedAt.Time).Milliseconds()), "execution_id": row.ID, "started_at": row.StartedAt, "layer": row.Layer, "run_id": row.RunID, "task_status": row.TaskStatus}
	if row.OAuthClient != "" {
		record["oauth_client"] = row.OAuthClient
	}
	return record
}

func (x *Execution) monitor() {
	defer x.log.executionWorkers.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-x.ctx.Done():
			x.markUnknown()
			return
		case <-x.log.stop:
			x.markUnknown()
			return
		case <-ticker.C:
		}
		if x.pollAllowed != nil && !x.pollAllowed() {
			x.stopTracking("authorization_lost")
			return
		}
		ctx, cancel := context.WithTimeout(x.ctx, 3*time.Second)
		// Logger shutdown cancels an in-flight poll as well as the next tick.
		stop := context.AfterFunc(x.log.executionCtx, cancel)
		result, err := x.poll(ctx, x.row.RunID)
		stop()
		cancel()
		meta, valid := taskMetadata(result, true)
		if err != nil || !valid || meta.RunID != x.row.RunID || meta.OutcomeUnknown {
			x.markUnknown()
			continue
		}
		x.mu.Lock()
		x.row.TaskStatus, x.row.State, x.row.UpdatedAt = meta.Status, "async", v2.Now()
		terminal := taskTerminal(meta.Status)
		if terminal {
			x.closed = true
		}
		row := x.row
		x.mu.Unlock()
		if terminal {
			status := "ok"
			var code any
			if meta.Status != "passed" {
				status, code = "error", meta.Status
			}
			if err := x.log.Write(taskRecord(row, "task_complete", status, code)); err != nil {
				x.markUnknown()
				continue
			}
			x.remove()
			return
		}
	}
}

// Executions returns a metadata snapshot and explicit admission limits.
func (l *Logger) Executions() ([]v2.ToolExecution, *v2.ExecutionLimits) {
	l.executionMu.Lock()
	items := make([]*Execution, 0, len(l.executions))
	for _, x := range l.executions {
		items = append(items, x)
	}
	limits := &v2.ExecutionLimits{MaxActive: maxExecutions, Active: len(items), RejectedTotal: l.executionRejected}
	l.executionMu.Unlock()
	out := make([]v2.ToolExecution, 0, len(items))
	for _, x := range items {
		out = append(out, x.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt.Time) })
	return out, limits
}

// PollResult unwraps only structured metadata from an MCP result.
func PollResult(result any, isError bool, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if isError {
		return nil, errors.New("task result unavailable")
	}
	return result, nil
}
