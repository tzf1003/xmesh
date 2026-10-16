package toolerr

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/tzf1003/xmesh/internal/sandbox"
)

// SandboxRunner reports a managed isolation runner that could not start or
// finish an execution. The call is refused under a named rule, so callers see
// the same denial as an unavailable platform instead of an unexplained internal
// error. The underlying cause stays attached for the node log.
func SandboxRunner(rule string, err error) *Error {
	// An oversized workspace is the caller's choice of directory, not a runner
	// fault, and the caller can fix it. Only a refused start says so; a failed
	// cleanup keeps its own rule even when growth during the run caused it.
	var large *sandbox.WorkspaceTooLargeError
	if rule == "sandbox.runner_failed" && errors.As(err, &large) {
		e := New("policy_denied", "L2 工作目录包含的文件和目录过多", "rule", "sandbox.workspace_too_large",
			"reason", fmt.Sprintf("工作目录的文件与目录总数超过 %d，请把 cwd 指向更小的独立项目目录，不要使用用户主目录或整个磁盘", large.Limit),
			"limit", large.Limit)
		e.Cause = err
		slog.Warn("sandbox_workspace_too_large", "limit", large.Limit)
		return e
	}
	e := New("policy_denied", "托管沙箱运行器拒绝了此次执行", "rule", rule, "reason", "L2 运行器未能完成请求，详见节点日志")
	e.Cause = err
	// The reason promises the node log, so the cause is written there.
	slog.Error("sandbox_runner_failed", "rule", rule, "error", err)
	return e
}
