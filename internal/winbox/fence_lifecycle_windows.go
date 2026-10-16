package winbox

import "context"

func (e *workspaceExecution) verifyFence(ctx context.Context) error {
	if e.fence == nil {
		return nil
	}
	return e.fence.Verify(ctx)
}

// sealFence binds the lifecycle's own directory chains to the pinned ones and
// starts verifying the workspace root descriptor. It runs after the initial
// grants and before any monitor or process admission.
func (e *workspaceExecution) sealFence(ctx context.Context) error {
	if e.fence == nil {
		return nil
	}
	var journal *workspaceTree
	if directory, ok := e.journal.namespace.(*journalDirectory); ok {
		journal = directory.tree
	}
	if err := e.fence.bindChains(e.workspace.tree, journal); err != nil {
		return err
	}
	if err := e.fence.sealRoot(ctx); err != nil {
		return err
	}
	e.workspace.writerCheck = e.fence.Verify
	return nil
}
