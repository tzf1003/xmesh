//go:build windows

package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/tzf1003/xmesh/internal/sandbox"
	"github.com/tzf1003/xmesh/internal/winbox"
)

// changeJournalReason is what callers and the node's user are told when the
// account cannot run L2. It states the fix without weakening the requirement.
const changeJournalReason = "当前账户无法读取 NTFS 变更日志，Windows L2 需要以管理员（提权）或 LocalSystem 服务运行节点"

// platformL2Runner builds the AppContainer runner with the production writer
// coordinator (guard keeper plus kernel fence, required before every launch) and
// reclaims what a previous process left in stateDir. A runner whose recovery did
// not finish is closed and not returned: new executions must not be admitted on
// top of unreclaimed profiles or ACL grants.
func platformL2Runner(ctx context.Context, stateDir string) (l2Runner, error) {
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	// Refuse an account that cannot read the change journal before the runner
	// creates any state: otherwise every call would fail without a reason and
	// leave unfinished cleanup behind.
	if err := winbox.ProbeChangeJournalAccess(filepath.Dir(absolute)); err != nil {
		return nil, &sandbox.UnavailableError{Reason: changeJournalReason, Cause: err}
	}
	runner, err := winbox.NewRunner(ctx, winbox.RunnerConfig{
		StateDir:           absolute,
		Coordinator:        winbox.NewWorkspaceCoordinator(nil),
		RequireWriterFence: true,
	})
	if err != nil {
		return nil, err
	}
	if err := runner.Recover(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("AppContainer startup recovery: %w", err), runner.Close(ctx))
	}
	return runner, nil
}
