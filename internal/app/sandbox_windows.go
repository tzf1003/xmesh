//go:build windows

package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/tzf1003/xmesh/internal/winbox"
)

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
