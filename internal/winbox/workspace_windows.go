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
// sandbox process before Close. Retained child handles still constrain directory
// renames; execution integration must resolve that compatibility boundary.
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
	tree, err := openWorkspaceTree(ctx, path, maxObjects)
	if err != nil {
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
	if p.sid == nil || p.grants != 0 {
		p.mu.Unlock()
		tree.Close()
		closeCredentialHandles(credentials)
		return nil, fmt.Errorf("AppContainer workspace requires an available exclusive profile")
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
		return g, nil
	}
	if cleanupErr := g.Close(); cleanupErr != nil {
		return g, errors.Join(err, fmt.Errorf("rollback AppContainer workspace: %w", cleanupErr))
	}
	return nil, err
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
	for index := len(current.objects) - 1; index >= 0; index-- {
		if err := g.profile.removeInheritedGrant(current.objects[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("release inherited AppContainer object %d: %w", index, err))
		}
	}
	if result != nil {
		return result
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

func (p *Profile) removeInheritedGrant(object treeObject) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sid == nil {
		return fmt.Errorf("AppContainer profile unavailable")
	}
	objectACLMutex.Lock()
	defer objectACLMutex.Unlock()
	current, err := windows.GetSecurityInfo(object.handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, defaulted, err := current.DACL()
	if err != nil {
		return err
	}
	merged, changed, err := editInheritedProfileACL(dacl, p.sid)
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

// Only the known inherited allow ACE format is owned by this operation. Other
// profiles and opaque ACEs retain their bytes and order. An unexpected allow ACE
// for this exclusive profile keeps cleanup pending instead of guessing ownership.
func editInheritedProfileACL(acl *windows.ACL, sid *windows.SID) (*windows.ACL, bool, error) {
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
			const flags = windows.INHERITED_ACE | windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
			if ace[1]&windows.INHERITED_ACE == 0 || ace[1]&^byte(flags) != 0 || binary.LittleEndian.Uint32(ace[4:8]) != mask {
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
