package winbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// workspaceGrant owns existing grants, inherited grants and the root namespace
// binding. The caller coordinates external namespace/ACL writers and reaps every
// sandbox process before Close. Descendant security handles use volume/file-ID
// opens so populated directories can be renamed while grants remain bound.
type workspaceGrant struct {
	mu          sync.Mutex
	profile     *Profile
	tree        *workspaceTree
	selected    *selectedObjectGrants
	path        string
	maxObjects  int
	credentials []windows.Handle
}

// grantWorkspace authorizes selected existing objects and inheritance from each
// directory. A result accompanying an error owns incomplete cleanup and must be
// retained for Close retry. No sandbox may run until admission succeeds.
func (p *Profile) grantWorkspace(ctx context.Context, path string, maxObjects int) (*workspaceGrant, error) {
	if p.journal != nil && maxObjects > 10000 {
		return nil, fmt.Errorf("AppContainer journaled workspace object limit exceeds 10000")
	}
	tree, err := openWorkspaceTree(ctx, path, maxObjects)
	if err != nil {
		return nil, err
	}
	if err := checkJournalBoundary(p.journal, tree); err != nil {
		tree.Close()
		return nil, err
	}
	paths, err := credentialDirectories()
	if err != nil {
		tree.Close()
		return nil, err
	}
	credentials, err := pinCredentialBoundaries(ctx, tree, paths)
	if err != nil {
		tree.Close()
		return nil, err
	}
	p.mu.Lock()
	if p.sid == nil || p.deleted || p.grants != 0 {
		p.mu.Unlock()
		tree.Close()
		closeCredentialHandles(credentials)
		return nil, fmt.Errorf("AppContainer workspace requires an available exclusive profile")
	}
	if p.journal != nil {
		snapshot := &journalWorkspace{Path: path}
		for _, object := range tree.objects {
			snapshot.Objects = append(snapshot.Objects, journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: object.directory})
		}
		snapshot.Root = snapshot.Objects[0]
		_, err = p.journal.append(journalRecord{Kind: "workspace", Workspace: snapshot})
		if err != nil {
			p.mu.Unlock()
			tree.Close()
			closeCredentialHandles(credentials)
			return nil, err
		}
	}
	// This reference also covers future inherited ACEs, independently of the
	// selected grants. It is released only after complete tree cleanup.
	p.grants++
	p.mu.Unlock()
	g := &workspaceGrant{profile: p, tree: tree, selected: &selectedObjectGrants{}, path: path, maxObjects: maxObjects, credentials: credentials}
	for _, object := range tree.objects {
		if err = ctx.Err(); err != nil {
			break
		}
		var grant *objectGrant
		grant, err = p.grantSelectedObjectMode(object, object.directory)
		if err != nil {
			break
		}
		g.selected.grants = append(g.selected.grants, grant)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = g.retainIdentityHandles(ctx)
	}
	if err == nil {
		return g, nil
	}
	if cleanupErr := g.Close(); cleanupErr != nil {
		return g, errors.Join(err, fmt.Errorf("rollback AppContainer workspace: %w", cleanupErr))
	}
	return nil, err
}

// Prepare every replacement before releasing any pathname descendant handle.
// A failed prepare leaves the original grants and snapshot available for rollback.
// Admission is private until this completes; external namespace/ACL writers are
// coordinated by the caller throughout preparation and execution.
func (g *workspaceGrant) retainIdentityHandles(ctx context.Context) error {
	type retainedHandles struct{ identity, security windows.Handle }
	retained := make([]retainedHandles, len(g.tree.objects))
	defer func() {
		for _, pair := range retained {
			if pair.identity != 0 {
				windows.CloseHandle(pair.identity)
			}
			if pair.security != 0 {
				windows.CloseHandle(pair.security)
			}
		}
	}()
	for index, object := range g.tree.objects[1:] {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := openIdentityObject(g.tree.ancestors[0], object)
		if err != nil {
			return err
		}
		pair := &retained[index+1]
		pair.security = h
		process := windows.CurrentProcess()
		if err := windows.DuplicateHandle(process, h, process, &pair.identity,
			windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, false, 0); err != nil {
			return fmt.Errorf("retain AppContainer file-ID identity handle: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for index := 1; index < len(retained); index++ {
		grant := g.selected.grants[index]
		windows.CloseHandle(grant.handle)
		windows.CloseHandle(grant.aclHandle)
		grant.handle, grant.aclHandle = retained[index].identity, retained[index].security
		retained[index] = retainedHandles{}
		windows.CloseHandle(g.tree.objects[index].handle)
		g.tree.objects[index].handle = 0
	}
	return nil
}

// Close first removes explicit inheritance sources, then scans the still-bound
// root and removes this profile's inherited ACEs by object identity. Any failed
// or incomplete scan retains the profile reference and namespace guard for retry.
func (g *workspaceGrant) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tree == nil {
		return nil
	}
	if g.profile.journal != nil {
		g.profile.mu.Lock()
		// Recovery owns the complete snapshot, including a write-ahead intent
		// whose kernel edit failed before an objectGrant could be returned.
		if err := releaseJournalWorkspace(g.profile.journal, g.profile.sid, g.maxObjects); err != nil {
			g.profile.mu.Unlock()
			return err
		}
		// Descriptors and durable acknowledgements are already committed. Close
		// each live reference without issuing a second release record.
		for _, grant := range g.selected.grants {
			if grant.handle == 0 {
				continue
			}
			windows.CloseHandle(grant.aclHandle)
			windows.CloseHandle(grant.handle)
			grant.aclHandle, grant.handle = 0, 0
			g.profile.grants--
		}
		g.profile.mu.Unlock()
	} else if err := g.closeUnjournaled(); err != nil {
		return err
	}
	g.tree.Close()
	g.tree = nil
	closeCredentialHandles(g.credentials)
	g.credentials = nil
	g.profile.mu.Lock()
	g.profile.grants--
	g.profile.mu.Unlock()
	return nil
}

func (g *workspaceGrant) closeUnjournaled() error {
	if err := g.selected.Close(); err != nil {
		return err
	}
	current, err := openWorkspaceTree(context.Background(), g.path, g.maxObjects)
	if err != nil {
		return fmt.Errorf("inspect inherited AppContainer grants: %w", err)
	}
	defer current.Close()
	if current.objects[0].id != g.tree.objects[0].id {
		return fmt.Errorf("AppContainer workspace root identity changed during cleanup")
	}
	var result error
	existing := make(map[fileIdentity]bool, len(g.tree.objects))
	for _, object := range g.tree.objects {
		existing[object.id] = true
	}
	for index := len(current.objects) - 1; index >= 0; index-- {
		object := current.objects[index]
		if err := g.profile.removeInheritedGrant(object, !existing[object.id]); err != nil {
			result = errors.Join(result, fmt.Errorf("release inherited AppContainer object %d: %w", index, err))
		}
	}
	if result != nil {
		return result
	}
	return nil
}

func (p *Profile) removeInheritedGrant(object treeObject, created bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sid == nil {
		return fmt.Errorf("AppContainer profile unavailable")
	}
	lock, err := acquireObjectACL(object.id, 30000)
	if err != nil {
		return err
	}
	defer lock.Close()
	current, err := readObjectSecurity(object.handle)
	if err != nil {
		return err
	}
	dacl, defaulted, err := current.DACL()
	if err != nil {
		return err
	}
	merged, changed, err := editInheritedProfileACL(dacl, p.sid, object.directory, created)
	if err != nil || !changed {
		return err
	}
	handle, err := reopenForACL(object.handle)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return writeObjectDACL(handle, current, merged, defaulted)
}

// Only the known workspace allow ACE format is owned by this operation. NTFS
// can copy inheritance into newly created objects without marking INHERITED.
// That form is accepted only for identities outside the admission snapshot.
// Other profiles and opaque ACEs retain their bytes and order. Unexpected allow
// ACEs for this exclusive profile keep cleanup pending for investigation.
func editInheritedProfileACL(acl *windows.ACL, sid *windows.SID, directory, created bool) (*windows.ACL, bool, error) {
	header, aces, err := aclParts(acl)
	if err != nil {
		return nil, false, err
	}
	sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())
	result := bytes.Clone(header)
	count, removed := 0, false
	for _, ace := range aces {
		owned := len(ace) == 8+len(sidBytes) && ace[0] == windows.ACCESS_ALLOWED_ACE_TYPE && bytes.Equal(ace[8:], sidBytes)
		if owned {
			const mask = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | 0x0040
			flags := byte(0)
			if directory {
				flags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
			}
			validFlags := ace[1] == flags|windows.INHERITED_ACE || (created && ace[1] == flags)
			if !validFlags || binary.LittleEndian.Uint32(ace[4:8]) != mask {
				return nil, false, fmt.Errorf("AppContainer inherited ACE ownership changed: flags=%#x mask=%#x", ace[1], binary.LittleEndian.Uint32(ace[4:8]))
			}
			removed = true
			continue
		}
		result = append(result, ace...)
		count++
	}
	if !removed {
		return nil, false, nil
	}
	binary.LittleEndian.PutUint16(result[2:4], uint16(len(result)))
	binary.LittleEndian.PutUint16(result[4:6], uint16(count))
	return (*windows.ACL)(unsafe.Pointer(&result[0])), true, nil
}
