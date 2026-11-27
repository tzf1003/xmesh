package control

import (
	"context"
	"net/http"
	"regexp"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// DelegateAdmin is the agent's delegation view behind GET /v2/delegate (API 2.7) and task cancellation behind
// POST /v2/delegate/tasks/{task_id}/cancel (API 2.8); the Agent implements it. Nil on a node without the agent
// role, and then the capability is not declared.
type DelegateAdmin interface {
	DelegateState() v2.DelegateState
	CancelDelegateTask(ctx context.Context, taskID string) (v2.DelegateTaskCancelResponse, bool)
}

var delegateTaskIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (s *Server) getDelegate(w http.ResponseWriter, _ *http.Request, _ *call) {
	writeJSON(w, 200, s.o.Delegate.DelegateState())
}

func (s *Server) cancelDelegateTask(w http.ResponseWriter, r *http.Request, _ *call) {
	taskID := pathPart(r, 3)
	if !delegateTaskIDPattern.MatchString(taskID) {
		failWith(w, argError("task_id", "任务 ID 无效"))
		return
	}
	res, ok := s.o.Delegate.CancelDelegateTask(r.Context(), taskID)
	if !ok {
		failWith(w, newError(v2.ErrNotFound, "没有这个委托任务"))
		return
	}
	writeJSON(w, 200, res)
}
