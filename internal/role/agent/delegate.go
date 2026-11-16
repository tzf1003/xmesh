package agent

import (
	"time"

	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/delegate"
)

// delegateSwitched ends every delegate task when a reload turns agent.delegate.enabled off (CTR-DLG-06). It runs on
// the goroutine that adopted the configuration, so it only signals the tasks and does not wait for them.
func (a *Agent) delegateSwitched(next *config.Config) {
	if !next.Agent.Delegate.Enabled {
		a.Delegate.CancelAll()
	}
}

// delegateEnded writes the audit record of a finished delegate task (CTR-DLG-06). It names the runner, the resolved
// cwd, the mode, the task and its end state. It never carries the prompt, the output or the reply.
func (a *Agent) delegateEnded(end delegate.End) {
	log := a.auditLog.Load()
	if log == nil {
		return
	}
	taskStatus := audit.DelegateTaskStatus(end.State)
	status, code := "ok", any(nil)
	if taskStatus != "passed" {
		status, code = "error", taskStatus
	}
	_ = log.Write(map[string]any{
		"time":        time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"event":       "task_complete",
		"caller":      end.Caller,
		"device":      a.store.Get().Node.Name,
		"tool":        "delegate.run",
		"args_digest": map[string]any{"runner": end.Runner, "resolved_path": end.ResolvedPath, "mode": end.Mode},
		"run_id":      end.TaskID,
		"task_status": taskStatus,
		"status":      status,
		"error":       code,
		"duration_ms": max(int64(0), end.Duration.Milliseconds()),
	})
}
