package winbox

import (
	"context"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// admitImport records the complete source subtree BEFORE its namespace move.
// The trusted coordinator freezes all source namespace/reparse/ACL writers from
// before this call through the move; it also keeps the workspace writer guard
// required by monitoring. This primitive changes no descriptors or namespace.
// Its persisted census owns recovery even if the move is cancelled or the host
// exits immediately after admission. Unknown uncoordinated imports still fail.
func (g *workspaceGrant) admitImport(ctx context.Context, path string, maxEvents int) error {
	release, err := g.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	p := g.profile
	p.mu.Lock()
	defer p.mu.Unlock()
	j := p.journal
	if g.tree == nil || p.workspaceClosing || p.reclaiming || maxEvents < 1 || maxEvents > maxWorkspaceChangeEvents {
		return fmt.Errorf("AppContainer import requires an active bounded workspace")
	}
	if err := validateWorkspaceJournalOwnership(j, p.sid); err != nil {
		return err
	}
	if j.state.changeCursor == nil || j.state.workspaceReleased || j.state.complete {
		return fmt.Errorf("AppContainer import requires active change tracking")
	}
	source, err := openImportTree(ctx, path, g.maxObjects)
	if err != nil {
		return err
	}
	defer source.Close()
	if source.objects[0].id.Volume != j.state.root.Volume {
		return fmt.Errorf("AppContainer import requires the tracked volume")
	}
	if err := checkJournalBoundary(j, source); err != nil {
		return err
	}
	// Importing a workspace ancestor would expose namespace guards as mutable
	// descendants. Physical identities also catch aliases of those directories.
	protected := map[fileIdentity]bool{g.tree.objects[0].id: true}
	for _, h := range g.tree.ancestors {
		object, err := inspectTreeObject(h, "")
		if err != nil {
			return err
		}
		protected[object.id] = true
	}
	for _, object := range source.objects {
		if protected[object.id] {
			return fmt.Errorf("AppContainer import overlaps workspace namespace guards")
		}
	}
	paths, err := credentialDirectories()
	if err != nil {
		return err
	}
	credentials, err := pinCredentialBoundaries(ctx, source, paths)
	if err != nil {
		return err
	}
	defer func() { closeCredentialHandles(credentials) }()
	// Catch up existing tracked identities before the frozen source enters their
	// ownership set. A prior uncoordinated move is rejected by this checkpoint.
	if err := g.checkpointChanges(ctx, maxEvents); err != nil {
		return err
	}
	proof := journalImport{Cursor: *j.state.changeCursor}
	for _, object := range source.objects {
		proof.Objects = append(proof.Objects, journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: object.directory})
	}
	proof.Root = proof.Objects[0]
	known := make(map[journalObject]bool, len(j.state.objects)+len(j.state.created))
	for object := range j.state.objects {
		known[object] = true
	}
	for object := range j.state.created {
		known[object] = true
	}
	added, err := planWorkspaceImport(proof, j.state.root, known, j.state.deleted, g.maxObjects)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(added) == 0 {
		return nil
	}
	if _, err := j.append(journalRecord{Kind: "workspace-import", Import: &proof}); err != nil {
		return err
	}
	// These credential boundaries remain bound across the eventual move and all
	// later tracking/reclamation, including a cancelled move or cleanup retry.
	g.credentials = append(g.credentials, credentials...)
	credentials = nil
	return nil
}

// A file is a singleton import; directories use the same complete bounded walk
// as workspace admission. Parent namespace guards stay bound throughout census.
func openImportTree(ctx context.Context, path string, maxObjects int) (*workspaceTree, error) {
	if _, _, err := workspaceComponents(path); err != nil {
		return nil, err
	}
	parent, err := openNamespaceRoot(ctx, filepath.Dir(path), true)
	if err != nil {
		return nil, err
	}
	h, err := openRelativeObject(parent.objects[0].handle, filepath.Base(path), true, false)
	if err != nil {
		parent.Close()
		return nil, err
	}
	object, err := inspectTreeObject(h, "")
	if err != nil {
		windows.CloseHandle(h)
		parent.Close()
		return nil, err
	}
	if object.directory {
		windows.CloseHandle(h)
		parent.Close()
		return openWorkspaceTree(ctx, path, maxObjects)
	}
	ancestors := append(parent.ancestors, parent.objects[0].handle)
	parent.ancestors, parent.objects = nil, nil
	return &workspaceTree{ancestors: ancestors, objects: []treeObject{object}}, nil
}
