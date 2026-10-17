package app

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/sandbox"
)

// l2Runner is a managed isolation runner together with its shutdown.
type l2Runner interface {
	sandbox.Runner
	Close(context.Context) error
}

// errL2Unsupported means this platform has no managed L2 runner to install.
var errL2Unsupported = errors.New("no managed L2 runner on this platform")

// openL2Runner builds the platform's managed runner over stateDir and completes
// its startup recovery. The platform files provide it; tests replace it.
var openL2Runner = platformL2Runner

// runnerShutdownLimit bounds the final runner cleanup. It exceeds the runner's own
// 90 second per-execution cleanup limit, and the service stop wait (see
// cmd/xmesh) is sized from it, so a stop does not abandon an execution that is
// still releasing its sandbox.
const runnerShutdownLimit = 2 * time.Minute

// RunnerShutdownLimit is the longest Run may spend releasing the managed runner
// after the roles have stopped.
func RunnerShutdownLimit() time.Duration { return runnerShutdownLimit }

// startSandbox installs the managed L2 runner when the configuration opts in. It
// never fails the node: a runner that cannot start or recover leaves L2 failing
// closed (sandbox.unavailable) and reports why through warnings, which sys.info
// carries. unavailable is non-empty exactly then; it is the reason shown to
// callers and in the control status. A runner refused for a reason the user can
// fix (sandbox.UnavailableError) names it; any other cause stays in the log.
func startSandbox(ctx context.Context, c *config.Config, stateDir string) (runner l2Runner, warnings []string, unavailable string) {
	if !c.Agent.Policy.WindowsAppContainer {
		return nil, nil, ""
	}
	runner, err := openL2Runner(ctx, filepath.Join(stateDir, "sandbox"))
	var refused *sandbox.UnavailableError
	switch {
	case errors.Is(err, errL2Unsupported):
		return nil, []string{"agent.policy.windows_appcontainer 仅在 Windows 节点生效，已忽略"}, ""
	case errors.As(err, &refused):
		slog.Error("windows_l2_runner_unavailable", "error", err)
		return nil, []string{"Windows L2 不可用，L2 继续拒绝执行（sandbox.unavailable）：" + refused.Reason}, refused.Reason
	case err != nil:
		slog.Error("windows_l2_runner_unavailable", "error", err)
		return nil, []string{"Windows L2 运行器未能启动或完成启动恢复，L2 继续拒绝执行（sandbox.unavailable），详见节点日志"}, "L2 运行器未能启动或完成启动恢复，详见节点日志"
	}
	return runner, nil, ""
}

// closeSandbox releases the runner after the agent has drained its executions.
func closeSandbox(runner l2Runner) error {
	ctx, cancel := context.WithTimeout(context.Background(), runnerShutdownLimit)
	defer cancel()
	return runner.Close(ctx)
}
