package winbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// moveImport binds an existing tracked destination parent, persists the complete
// source census, moves the source by identity, then grants each imported object.
// The trusted caller freezes source and destination namespace/reparse/ACL writers
// before entry and holds the workspace's external writer guard throughout. The
// operation gate also excludes this owner's monitor and cleanup until completion.
//
// moved reports the namespace side effect even when subsequent admission fails.
// Such a failure seals execution and reaps its Jobs; the durable census retains
// all ACL cleanup obligations. Namespace moves are retained during reclamation.
func (g *workspaceGrant) moveImport(ctx context.Context, sourcePath, destinationPath string, maxEvents int) (moved bool, resultErr error) {
	release, err := g.acquire(ctx)
	if err != nil {
		return false, err
	}
	p := g.profile
	var reap *executionProcesses
	defer func() {
		release()
		if reap != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, reap.reap(cleanup, 1))
		}
	}()
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() {
		if moved && resultErr != nil {
			p.workspaceClosing = true
			reap = p.processes
		}
	}()
	if err := g.importReady(maxEvents); err != nil {
		return false, err
	}
	if err := g.checkpointChanges(ctx, maxEvents); err != nil {
		return false, err
	}
	parent, name, err := g.importDestination(ctx, destinationPath)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	source, err := openImportTree(ctx, sourcePath, g.maxObjects)
	if err != nil {
		return false, err
	}
	defer source.Close()
	for _, object := range source.objects {
		key := journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: object.directory}
		if object.id == parent.objects[0].id || p.journal.state.objects[key] {
			return false, fmt.Errorf("AppContainer import move overlaps destination or initial workspace identities")
		}
	}
	// Acquire every security handle and check every descriptor before changing
	// namespace or ACLs. All handles use file IDs so they permit directory moves.
	objects, err := g.prepareImportAccess(ctx, source)
	if err != nil {
		return false, err
	}
	defer func() {
		for _, object := range objects {
			if object.handle != 0 {
				windows.CloseHandle(object.handle)
			}
		}
	}()
	if err := g.admitImportTree(ctx, source, maxEvents); err != nil {
		return false, err
	}
	// Pathname descendant handles can block a populated-directory rename. The
	// complete persisted census now owns recovery, and parent guards remain bound.
	root := source.objects[0]
	for index := range source.objects {
		windows.CloseHandle(source.objects[index].handle)
		source.objects[index].handle = 0
	}
	handle, err := openImportMoveRoot(source.ancestors[len(source.ancestors)-1], filepath.Base(sourcePath), root)
	if err != nil {
		return false, err
	}
	defer func() {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
	}()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := renameImportObject(handle, parent.objects[0].handle, name); err != nil {
		return false, err
	}
	moved = true
	// Reopen the exact destination binding before granting access. The caller's
	// writer guard holds this binding until the transaction returns.
	target, err := openRelativeObject(parent.objects[0].handle, name, true, false)
	if err != nil {
		return true, err
	}
	actual, inspectErr := inspectTreeObject(target, "")
	windows.CloseHandle(target)
	if inspectErr != nil || actual.id != root.id || actual.directory != root.directory {
		return true, fmt.Errorf("AppContainer import destination identity mismatch: %v", inspectErr)
	}
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		if err := g.grantImportAccess(object); err != nil {
			return true, err
		}
	}
	// Release transaction-local identity handles so NTFS can emit final close
	// reasons before recording the completed transaction's checkpoint waterline.
	windows.CloseHandle(handle)
	handle = 0
	for index := range objects {
		windows.CloseHandle(objects[index].handle)
		objects[index].handle = 0
	}
	if err := g.checkpointChanges(ctx, maxEvents); err != nil {
		return true, err
	}
	return true, nil
}

// A rename requires a namespace-aware file object. Security-only recovery opens
// by ID can outlive names, but NTFS rejects renaming those file objects. Reopen
// relative to the still-bound original parent, then verify the entire identity
// and type before using DELETE access. External source writers remain frozen.
func openImportMoveRoot(parent windows.Handle, name string, expected treeObject) (windows.Handle, error) {
	if err := ordinaryComponent(name); err != nil {
		return 0, err
	}
	encoded, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: parent,
		ObjectName: encoded, Attributes: windows.OBJ_DONT_REPARSE | windows.OBJ_CASE_INSENSITIVE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.DELETE|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		&attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("open bound AppContainer import root for move: %w", err)
	}
	actual, err := inspectTreeObject(handle, "")
	if err != nil || actual.id != expected.id || actual.directory != expected.directory {
		windows.CloseHandle(handle)
		return 0, fmt.Errorf("AppContainer import move source identity mismatch: %v", err)
	}
	return handle, nil
}

// Lexical containment and the pinned physical workspace identity must agree.
// The complete destination chain remains protected from replacement; the final
// parent must already belong to the tracked live directory census.
func (g *workspaceGrant) importDestination(ctx context.Context, path string) (*workspaceTree, string, error) {
	if _, _, err := workspaceComponents(path); err != nil {
		return nil, "", err
	}
	relative, err := filepath.Rel(g.path, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, `..\`) || filepath.IsAbs(relative) {
		return nil, "", fmt.Errorf("AppContainer import destination must be inside the workspace")
	}
	parent, err := openWorkspaceRoot(ctx, filepath.Dir(path))
	if err != nil {
		return nil, "", err
	}
	valid := false
	defer func() {
		if !valid {
			parent.Close()
		}
	}()
	rootID := g.tree.objects[0].id
	bound := parent.objects[0].id == rootID
	for _, h := range parent.ancestors {
		ancestor, err := inspectTreeObject(h, "")
		if err != nil {
			return nil, "", err
		}
		bound = bound || ancestor.id == rootID
	}
	object := parent.objects[0]
	key := journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: true}
	j := g.profile.journal
	_, deleted := j.state.deleted[key]
	if !bound || !object.directory || deleted || (!j.state.objects[key] && !j.state.created[key]) {
		return nil, "", fmt.Errorf("AppContainer import destination requires a bound tracked directory")
	}
	name := filepath.Base(path)
	h, err := openRelativeObject(object.handle, name, true, true)
	if err == nil {
		windows.CloseHandle(h)
		return nil, "", fmt.Errorf("AppContainer import destination already exists")
	}
	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) {
		return nil, "", fmt.Errorf("inspect AppContainer import destination: %w", err)
	}
	valid = true
	return parent, name, nil
}

func (g *workspaceGrant) prepareImportAccess(ctx context.Context, source *workspaceTree) (_ []treeObject, resultErr error) {
	objects := make([]treeObject, 0, len(source.objects))
	defer func() {
		if resultErr != nil {
			for _, object := range objects {
				windows.CloseHandle(object.handle)
			}
		}
	}()
	for _, object := range source.objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := openIdentityObject(source.ancestors[0], object)
		if err != nil {
			return nil, err
		}
		object.handle = h
		objects = append(objects, object)
		if _, _, _, err := g.importAccessEdit(object); err != nil {
			return nil, err
		}
	}
	return objects, nil
}

func (g *workspaceGrant) grantImportAccess(object treeObject) error {
	lock, err := acquireObjectACL(object.id, 30000)
	if err != nil {
		return err
	}
	defer lock.Close()
	sd, acl, changed, err := g.importAccessEdit(object)
	if err != nil || !changed {
		return err
	}
	_, defaulted, err := sd.DACL()
	if err != nil {
		return err
	}
	return writeObjectDACL(object.handle, sd, acl, defaulted)
}

// Imported identities have a complete write-ahead census. Use the same precise
// workspace-format ACE that inherited-object reclamation already owns. Existing
// matching explicit or inherited access is idempotent; other SID entries fail.
func (g *workspaceGrant) importAccessEdit(object treeObject) (*windows.SECURITY_DESCRIPTOR, *windows.ACL, bool, error) {
	sd, err := readObjectSecurity(object.handle)
	if err != nil {
		return nil, nil, false, err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, nil, false, err
	}
	_, aces, err := aclParts(acl)
	if err != nil {
		return nil, nil, false, err
	}
	var template []byte
	for _, record := range g.profile.journal.records {
		if record.Grant != nil && record.Grant.Object == g.profile.journal.state.root {
			template = bytes.Clone(record.Grant.ACE)
			break
		}
	}
	if len(template) == 0 {
		return nil, nil, false, fmt.Errorf("AppContainer import requires workspace access ownership")
	}
	template[1] = 0
	if object.directory {
		template[1] = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	sid := unsafe.Slice((*byte)(unsafe.Pointer(g.profile.sid)), g.profile.sid.Len())
	matches := 0
	for _, ace := range aces {
		if !bytes.Contains(ace, sid) {
			continue
		}
		if len(ace) != len(template) || ace[0] != template[0] || (ace[1] != template[1] && ace[1] != template[1]|windows.INHERITED_ACE) || !bytes.Equal(ace[2:], template[2:]) {
			return nil, nil, false, fmt.Errorf("AppContainer import access ownership changed")
		}
		matches++
	}
	if matches > 1 {
		return nil, nil, false, fmt.Errorf("AppContainer import has duplicate owned access")
	}
	if matches == 1 {
		key := journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: object.directory}
		if !g.profile.journal.state.created[key] {
			return nil, nil, false, fmt.Errorf("AppContainer import has access preceding identity ownership")
		}
		return sd, acl, false, nil
	}
	merged, err := editObjectACL(acl, template, true)
	return sd, merged, err == nil, err
}

type importRenameInfo struct {
	Flags         uint32
	RootDirectory windows.Handle
	NameLength    uint32
	Name          [1]uint16
}

func renameImportObject(source, parent windows.Handle, name string) error {
	if err := ordinaryComponent(name); err != nil {
		return err
	}
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	var header importRenameInfo
	offset := unsafe.Offsetof(header.Name)
	storage := make([]uint64, (int(offset)+2*len(encoded)+7)/8)
	buffer := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), len(storage)*8)
	info := (*importRenameInfo)(unsafe.Pointer(&buffer[0]))
	info.RootDirectory = parent
	info.NameLength = uint32(2 * (len(encoded) - 1))
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(encoded)), encoded)
	// The native rename information accepts a bound target-directory handle.
	// The Win32 FileRenameInfoEx interface requires a null RootDirectory.
	// ReplaceIfExists remains zero: an existing destination is never replaced.
	var status windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(source, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation); err != nil {
		return fmt.Errorf("move AppContainer import by identity: %w", err)
	}
	return nil
}
