package winbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

// Recover reclaims what a previous host process left behind in the state
// directory: AppContainer profiles, workspace ACL grants and change journals of
// executions whose cleanup never completed. Call it once, before the first
// execution is admitted; it refuses to run next to live executions.
//
// A journal whose guard keeper survived the host is adopted and recovered under
// that keeper. When no keeper exists (a reboot or logoff killed every process,
// or the keeper itself was killed) the workspace is frozen by a fresh kernel
// writer fence instead, and recovery proceeds only if the journal's profile has
// no live processes. Journals that cannot be recovered stay intact and the error
// names each of them, so the caller can keep L2 failing closed rather than admit
// new executions on top of unreclaimed grants.
//
// The fresh fence detects, but cannot prevent, same-user DACL changes while the
// sweep runs; see NewWorkspaceCoordinator for the limits of the writer boundary.
func (r *Runner) Recover(ctx context.Context) error {
	r.mu.Lock()
	busy := r.closed || len(r.active) != 0 || len(r.pending) != 0
	r.mu.Unlock()
	if busy {
		return errors.New("AppContainer recovery requires an idle runner")
	}
	results, err := recoverJournalDirectoryWithKeepers(ctx, r.config.StateDir, maxRecoveryJournals, r.config.MaxObjects, fenceRecoveryPrepare(r.config.StateDir))
	for _, result := range results {
		if result.Err != nil || (result.State != "complete" && result.State != "recovered") {
			continue
		}
		// A completed journal has no recovery content left; the keeper release
		// above already read its completion.
		discardCompletedJournal(ctx, filepath.Join(r.config.StateDir, result.Name))
	}
	return err
}

// fenceRecoveryPrepare freezes the workspace of a journal without a surviving
// keeper by acquiring the same kernel writer fence an execution uses. The sweep
// itself verifies the recorded workspace identity and that the profile has no
// live processes before it edits anything.
func fenceRecoveryPrepare(stateDir string) journalRecoveryPrepare {
	return func(ctx context.Context, info journalRecoveryInfo) (*journalRecoveryLease, error) {
		if info.Workspace == nil {
			return nil, fmt.Errorf("AppContainer recovery without a keeper covers workspace journals only")
		}
		fence, err := acquireWorkspaceWriterFence(ctx, info.Workspace.Path, stateDir)
		if err != nil {
			return nil, fmt.Errorf("AppContainer recovery could not freeze the workspace: %w", err)
		}
		return &journalRecoveryLease{Guard: fence}, nil
	}
}
