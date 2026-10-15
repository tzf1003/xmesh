package winbox

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type journalDirectory struct{ tree *workspaceTree }

func (d *journalDirectory) Close() error {
	if d.tree != nil {
		d.tree.Close()
		d.tree = nil
	}
	return nil
}

// Create uses an already bound parent and supplies the protected descriptor at
// FILE_CREATE, so the private directory never has an intermediate inherited ACL.
// Open verifies the existing owner and exact owner/SYSTEM inheritable allow
// entries. All components reject reparse points and retain directory data access
// without delete sharing. Callers still coordinate in-place reparse/ACL writers.
func openJournalDirectory(ctx context.Context, path string, create bool) (_ *journalDirectory, resultErr error) {
	if _, _, err := workspaceComponents(path); err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	var tree *workspaceTree
	defer func() {
		if resultErr != nil && tree != nil {
			tree.Close()
		}
	}()
	if create {
		tree, err = openNamespaceRoot(ctx, filepath.Dir(path), true)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, err := windows.NewNTUnicodeString(filepath.Base(path))
		if err != nil {
			return nil, err
		}
		sid := user.User.Sid.String()
		sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + sid + ")")
		if err != nil {
			return nil, err
		}
		parent := tree.objects[0]
		attributes := windows.OBJECT_ATTRIBUTES{
			Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: parent.handle,
			ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE, SecurityDescriptor: sd,
		}
		var h windows.Handle
		var status windows.IO_STATUS_BLOCK
		err = windows.NtCreateFile(&h, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY,
			&attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT, 0, 0)
		runtime.KeepAlive(sd)
		if err != nil {
			return nil, fmt.Errorf("create private AppContainer journal directory: %w", err)
		}
		object, err := inspectTreeObject(h, "")
		if err != nil || !object.directory || object.id.Volume != parent.id.Volume {
			windows.CloseHandle(h)
			return nil, fmt.Errorf("AppContainer journal directory identity mismatch: %v", err)
		}
		tree.ancestors = append(tree.ancestors, parent.handle)
		tree.objects = []treeObject{object}
	} else {
		tree, err = openWorkspaceRoot(ctx, path)
		if err != nil {
			return nil, err
		}
	}
	security, err := windows.GetSecurityInfo(tree.objects[0].handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	const inheritance = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	if err := validateRecoverySecurity(security, user.User.Sid, inheritance); err != nil {
		return nil, fmt.Errorf("AppContainer private journal directory: %w", err)
	}
	return &journalDirectory{tree: tree}, nil
}

// A complete or damaged journal owns its namespace guard until Close. The
// directory is inspected without enumerating journal files, so independent
// owners can hold separate exclusive journals in the same private directory.
func openBoundGrantJournal(ctx context.Context, path string, create bool) (*grantJournal, error) {
	if _, _, err := workspaceComponents(path); err != nil {
		return nil, err
	}
	directory, err := openJournalDirectory(ctx, filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	root := directory.tree.objects[0]
	boundary := &journalBoundary{Root: journalObject{Volume: root.id.Volume, ID: root.id.ID, Directory: true}}
	for _, h := range directory.tree.ancestors {
		object, inspectErr := inspectTreeObject(h, "")
		if inspectErr != nil {
			directory.Close()
			return nil, inspectErr
		}
		boundary.Ancestors = append(boundary.Ancestors, journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: true})
	}
	h, err := openRelativeJournal(ctx, root.handle, filepath.Base(path), create)
	if err != nil {
		directory.Close()
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		windows.CloseHandle(h)
		directory.Close()
		return nil, err
	}
	j, err := loadPrivateGrantJournal(h, path, user.User.Sid)
	if j == nil {
		directory.Close()
		return nil, err
	}
	j.namespace, j.boundary = directory, boundary
	return j, err
}

func openRelativeJournal(ctx context.Context, directory windows.Handle, name string, create bool) (windows.Handle, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := ordinaryComponent(name); err != nil {
		return 0, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return 0, err
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;SY)(A;;FA;;;" + sid + ")")
	if err != nil {
		return 0, err
	}
	ntName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: directory, ObjectName: ntName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE, SecurityDescriptor: sd,
	}
	disposition := uint32(windows.FILE_OPEN)
	if create {
		disposition = windows.FILE_CREATE
	}
	var h windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.SYNCHRONIZE,
		&attributes, &status, nil, 0, 0, disposition,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_WRITE_THROUGH|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		return 0, fmt.Errorf("open bound AppContainer grant journal: %w", err)
	}
	return h, nil
}

// Compare physical identities in both directions. Shared volume/ancestor
// directories are allowed; neither selected root may contain the other root.
func checkJournalBoundary(j *grantJournal, tree *workspaceTree) error {
	if j == nil || j.boundary == nil {
		return nil
	}
	b := j.boundary
	state := fileIdentity{Volume: b.Root.Volume, ID: b.Root.ID}
	for _, object := range tree.objects {
		if object.id == state {
			return fmt.Errorf("AppContainer workspace contains its journal directory")
		}
	}
	for _, h := range tree.ancestors {
		object, err := inspectTreeObject(h, "")
		if err != nil {
			return err
		}
		if object.id == state {
			return fmt.Errorf("AppContainer workspace is inside its journal directory")
		}
	}
	root := tree.objects[0].id
	for _, ancestor := range b.Ancestors {
		if root == (fileIdentity{Volume: ancestor.Volume, ID: ancestor.ID}) {
			return fmt.Errorf("AppContainer workspace contains its journal directory ancestor")
		}
	}
	return nil
}

// Standalone selected grants also exclude state children and directory
// ancestors. Verify the DOS path's actual target identity before using its
// parent chain; a changed drive mapping or alias must stop admission.
func checkJournalSelectedBoundary(j *grantJournal, object treeObject) error {
	if j == nil || j.boundary == nil || j.state.workspace {
		return nil
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(object.handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return err
	}
	if n == 0 || n >= uint32(len(buffer)) {
		return fmt.Errorf("AppContainer selected journal boundary path is unavailable")
	}
	name := windows.UTF16ToString(buffer[:n])
	if !strings.HasPrefix(name, `\\?\`) {
		return fmt.Errorf("AppContainer selected journal boundary requires a local drive path")
	}
	name = strings.TrimPrefix(name, `\\?\`)
	if _, _, err := workspaceComponents(name); err != nil {
		return err
	}
	path := name
	if !object.directory {
		path = filepath.Dir(name)
	}
	tree, err := openNamespaceRoot(context.Background(), path, true)
	if err != nil {
		return err
	}
	defer tree.Close()
	actual := tree.objects[0]
	if !object.directory {
		h, err := openRelativeObject(actual.handle, filepath.Base(name), true, true)
		if err != nil {
			return err
		}
		actual, err = inspectTreeObject(h, "")
		windows.CloseHandle(h)
		if err != nil {
			return err
		}
	}
	if actual.id != object.id || actual.directory != object.directory {
		return fmt.Errorf("AppContainer selected journal boundary identity changed")
	}
	state := fileIdentity{Volume: j.boundary.Root.Volume, ID: j.boundary.Root.ID}
	if tree.objects[0].id == state {
		return fmt.Errorf("AppContainer selected object is inside its journal directory")
	}
	for _, h := range tree.ancestors {
		ancestor, err := inspectTreeObject(h, "")
		if err != nil {
			return err
		}
		if ancestor.id == state {
			return fmt.Errorf("AppContainer selected object is inside its journal directory")
		}
	}
	if object.directory {
		for _, ancestor := range j.boundary.Ancestors {
			if object.id == (fileIdentity{Volume: ancestor.Volume, ID: ancestor.ID}) {
				return fmt.Errorf("AppContainer selected directory contains its journal directory")
			}
		}
	}
	return nil
}
