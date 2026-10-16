package winbox

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
)

// NewWorkspaceCoordinator returns the production WorkspaceCoordinator. For each
// execution it launches an independent guard keeper that pins the workspace and
// journal-directory chains and survives the host, then acquires the in-host
// kernel writer fence. The pair is returned as one guard: it proves, through
// Verify, that the pinned namespace/reparse/ACL boundary is unchanged before
// every launch, and it is released only after the journal's durable completion.
//
// Use it with RunnerConfig.RequireWriterFence so the runner refuses any guard
// that cannot verify the boundary. keeperCommand is the keeper executable and
// leading arguments; nil runs this binary with "__sandbox-guard".
//
// The guard does not freeze writes inside the workspace tree, and the fence only
// detects (it cannot prevent) same-user WRITE_DAC changes; see the Windows L2
// conditions in docs/implementation-status.md before treating this as L2.
func NewWorkspaceCoordinator(keeperCommand []string) WorkspaceCoordinator {
	command := append([]string(nil), keeperCommand...)
	if len(command) == 0 {
		command = nil
	}
	return func(ctx context.Context, workspace, journal string) (io.Closer, error) {
		keeper, err := launchGuardKeeper(ctx, keeperLaunch{Workspace: workspace, Journal: journal, Command: command})
		if err != nil {
			return nil, err
		}
		guard := &productionGuard{keeper: keeper}
		fence, err := acquireWorkspaceWriterFence(ctx, workspace, filepath.Dir(journal))
		if err != nil {
			// The keeper is still owned; the lifecycle closes it with the error.
			return guard, err
		}
		guard.fence = fence
		return guard, nil
	}
}

// productionGuard is the keeper plus the kernel fence acting as one guard.
type productionGuard struct {
	mu     sync.Mutex
	keeper *guardKeeper
	fence  *workspaceWriterFence
}

var _ writerVerifier = (*productionGuard)(nil)

func (g *productionGuard) Verify(ctx context.Context) error {
	if g.fence == nil {
		return errors.New("AppContainer writer fence is unavailable")
	}
	return g.fence.Verify(ctx)
}

func (g *productionGuard) sealRoot(ctx context.Context) error {
	if g.fence == nil {
		return errors.New("AppContainer writer fence is unavailable")
	}
	return g.fence.sealRoot(ctx)
}

func (g *productionGuard) bindChains(workspace, journal *workspaceTree) error {
	if g.fence == nil {
		return errors.New("AppContainer writer fence is unavailable")
	}
	return g.fence.bindChains(workspace, journal)
}

func (g *productionGuard) bindJournal(ctx context.Context, j *grantJournal) error {
	return g.keeper.bindJournal(ctx, j)
}

// Close releases the keeper first: it agrees only after reading the journal's
// durable completion, so a refusal leaves both the keeper and the fence in
// place for a retry. The fence is released only once the keeper let go.
func (g *productionGuard) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.keeper != nil {
		if err := g.keeper.Close(); err != nil {
			return err
		}
	}
	if g.fence != nil {
		return g.fence.Close()
	}
	return nil
}
