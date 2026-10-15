package winbox

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileIdentity struct {
	Volume uint64
	ID     [16]byte
}

type treeObject struct {
	handle    windows.Handle
	id        fileIdentity
	relative  string
	directory bool
}

// workspaceTree retains the actual objects selected before ACL admission.
// Ancestors through the workspace root omit delete sharing to keep that binding
// stable. Descendants share delete access; retained child handles can still
// prevent a directory rename on Windows. Execution admission must account for
// that lifetime. ACL operations must use the retained object identities rather
// than resolve the recorded names again.
// No filesystem ACL changes occur while constructing or closing this snapshot.
type workspaceTree struct {
	ancestors []windows.Handle
	objects   []treeObject
}

func openWorkspaceTree(ctx context.Context, path string, maxObjects int) (_ *workspaceTree, resultErr error) {
	if maxObjects < 1 {
		return nil, fmt.Errorf("AppContainer workspace object limit must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	volume, components, err := workspaceComponents(path)
	if err != nil {
		return nil, err
	}
	tree := &workspaceTree{}
	defer func() {
		if resultErr != nil {
			tree.Close()
		}
	}()
	rootName, err := windows.UTF16PtrFromString(`\\?\` + volume + `\`)
	if err != nil {
		return nil, err
	}
	anchor, err := windows.CreateFile(rootName, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	tree.ancestors = append(tree.ancestors, anchor)
	anchorInfo, err := inspectTreeObject(anchor, "")
	if err != nil || !anchorInfo.directory {
		return nil, fmt.Errorf("AppContainer volume anchor: %w", errors.Join(err, requireDirectory(anchorInfo)))
	}
	var flags, serial, componentLength uint32
	var volumeName, filesystemName [261]uint16
	if err := windows.GetVolumeInformationByHandle(anchor, &volumeName[0], uint32(len(volumeName)), &serial, &componentLength, &flags, &filesystemName[0], uint32(len(filesystemName))); err != nil {
		return nil, fmt.Errorf("query AppContainer workspace volume: %w", err)
	}
	if flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return nil, fmt.Errorf("AppContainer workspace volume requires persistent ACLs")
	}
	parent := anchor
	var root treeObject
	for _, name := range components {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := openRelativeObject(parent, name, false, true)
		if err != nil {
			return nil, err
		}
		tree.ancestors = append(tree.ancestors, h)
		root, err = inspectTreeObject(h, "")
		if err != nil {
			return nil, err
		}
		if !root.directory || root.id.Volume != anchorInfo.id.Volume {
			return nil, fmt.Errorf("AppContainer workspace ancestor changed type or volume")
		}
		parent = h
	}
	// The last ancestor is also objects[0]; keep one owner of its handle.
	tree.ancestors = tree.ancestors[:len(tree.ancestors)-1]
	tree.objects = append(tree.objects, root)
	seen := map[fileIdentity]bool{root.id: true}
	for index := 0; index < len(tree.objects); index++ {
		current := tree.objects[index]
		if !current.directory {
			continue
		}
		err := enumerateDirectory(ctx, current.handle, func(entry directoryEntry) error {
			if len(tree.objects) >= maxObjects {
				return fmt.Errorf("AppContainer workspace exceeds %d objects", maxObjects)
			}
			if entry.attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				return fmt.Errorf("AppContainer workspace contains a reparse point")
			}
			h, err := openRelativeObject(current.handle, entry.name, true, false)
			if err != nil {
				return err
			}
			child, err := inspectTreeObject(h, filepath.Join(current.relative, entry.name))
			if err == nil {
				err = verifyDirectoryEntry(current.id.Volume, entry, child)
			}
			if err != nil {
				windows.CloseHandle(h)
				return err
			}
			if seen[child.id] {
				windows.CloseHandle(h)
				return fmt.Errorf("AppContainer workspace contains a repeated object identity")
			}
			seen[child.id] = true
			tree.objects = append(tree.objects, child)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("enumerate AppContainer workspace directory %q: %w", current.relative, err)
		}
	}
	return tree, nil
}

func (t *workspaceTree) Close() {
	for index := len(t.objects) - 1; index >= 0; index-- {
		if t.objects[index].handle != 0 {
			windows.CloseHandle(t.objects[index].handle)
		}
	}
	t.objects = nil
	for index := len(t.ancestors) - 1; index >= 0; index-- {
		windows.CloseHandle(t.ancestors[index])
	}
	t.ancestors = nil
}

func workspaceComponents(path string) (string, []string, error) {
	path = filepath.FromSlash(path)
	// L2 has no network capability. Bind a local drive namespace explicitly;
	// callers must resolve aliases before admission rather than pass a device,
	// UNC, drive-relative, root-relative, or alternate-stream spelling here.
	if len(path) < 4 || !((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) || path[1:3] != `:\` {
		return "", nil, fmt.Errorf("AppContainer workspace requires an absolute local drive path below its root")
	}
	parts := strings.Split(path[3:], `\`)
	for _, part := range parts {
		if err := ordinaryComponent(part); err != nil {
			return "", nil, err
		}
	}
	return path[:2], parts, nil
}

func ordinaryComponent(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "\\/:\x00*?\"") {
		return fmt.Errorf("AppContainer workspace requires ordinary path components")
	}
	return nil
}

func openRelativeObject(parent windows.Handle, name string, shareDelete, caseInsensitive bool) (windows.Handle, error) {
	if err := ordinaryComponent(name); err != nil {
		return 0, err
	}
	ntName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    ntName,
		Attributes:    windows.OBJ_DONT_REPARSE,
	}
	if caseInsensitive {
		attributes.Attributes |= windows.OBJ_CASE_INSENSITIVE
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES)
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_OPEN_FOR_BACKUP_INTENT)
	if shareDelete {
		share |= windows.FILE_SHARE_DELETE
	} else {
		// Metadata-only opens do not participate in NTFS share-access checks.
		// Directory data access makes the omitted delete-sharing bit protect
		// the binding even when no descendant handles remain open.
		access |= windows.FILE_LIST_DIRECTORY
		options |= windows.FILE_DIRECTORY_FILE
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access,
		&attributes, &status, nil, 0, share, windows.FILE_OPEN,
		options, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("open AppContainer workspace component: %w", err)
	}
	return handle, nil
}

func inspectTreeObject(handle windows.Handle, relative string) (treeObject, error) {
	var info windows.ByHandleFileInformation
	if kind, err := windows.GetFileType(handle); err != nil || kind != windows.FILE_TYPE_DISK {
		return treeObject{}, fmt.Errorf("AppContainer workspace requires disk objects: type=%d error=%v", kind, err)
	}
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return treeObject{}, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.NumberOfLinks > 1 {
		return treeObject{}, fmt.Errorf("AppContainer workspace requires ordinary single-link objects")
	}
	var id fileIdentity
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))); err != nil {
		return treeObject{}, err
	}
	if id.ID == [16]byte{} {
		return treeObject{}, fmt.Errorf("AppContainer workspace object identity unavailable")
	}
	return treeObject{handle: handle, id: id, relative: relative, directory: info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0}, nil
}

func requireDirectory(object treeObject) error {
	if !object.directory {
		return fmt.Errorf("AppContainer workspace anchor is not a directory")
	}
	return nil
}

type directoryEntry struct {
	name       string
	id         [16]byte
	attributes uint32
}

func verifyDirectoryEntry(volume uint64, entry directoryEntry, object treeObject) error {
	if object.id.Volume != volume || object.id.ID != entry.id || object.directory != (entry.attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) {
		return fmt.Errorf("AppContainer workspace object changed during enumeration")
	}
	return nil
}

func enumerateDirectory(ctx context.Context, object windows.Handle, visit func(directoryEntry) error) error {
	name, err := windows.NewNTUnicodeString("")
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: object, ObjectName: name,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		&attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return fmt.Errorf("open directory enumeration handle: %w", err)
	}
	defer windows.CloseHandle(handle)
	// FILE_ID_EXTD_DIR_INFO entries are 8-byte aligned and carry a 128-bit ID.
	// Each child is opened relative to this exact parent and compared with it.
	// Directory-information buffers require 8-byte alignment. A byte array can
	// be stack allocated at byte alignment, so give the backing store uint64
	// alignment explicitly on both supported Windows architectures.
	storage := make([]uint64, 64*1024/8)
	buffer := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), len(storage)*8)
	class := uint32(windows.FileIdExtdDirectoryRestartInfo)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := windows.GetFileInformationByHandleEx(handle, class, &buffer[0], uint32(len(buffer)))
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("query directory information class %d: %w", class, err)
		}
		if err := parseDirectoryEntries(buffer, func(entry directoryEntry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return visit(entry)
		}); err != nil {
			return err
		}
		class = windows.FileIdExtdDirectoryInfo
	}
}

func parseDirectoryEntries(buffer []byte, visit func(directoryEntry) error) error {
	const headerSize = 88
	for {
		if len(buffer) < headerSize {
			return fmt.Errorf("truncated AppContainer directory entry")
		}
		next := int(binary.LittleEndian.Uint32(buffer[:4]))
		nameBytes := int(binary.LittleEndian.Uint32(buffer[60:64]))
		if nameBytes == 0 || nameBytes%2 != 0 || nameBytes > len(buffer)-headerSize {
			return fmt.Errorf("invalid AppContainer directory name length")
		}
		if next != 0 && (next%8 != 0 || next < headerSize+nameBytes || next > len(buffer)-headerSize) {
			return fmt.Errorf("invalid AppContainer directory entry offset")
		}
		units := make([]uint16, nameBytes/2)
		for index := range units {
			units[index] = binary.LittleEndian.Uint16(buffer[headerSize+index*2:])
		}
		name := string(utf16.Decode(units))
		// A lossy UTF-16 conversion must not select a different sibling.
		encoded := utf16.Encode([]rune(name))
		if len(encoded) != len(units) {
			return fmt.Errorf("invalid AppContainer directory name encoding")
		}
		for index := range units {
			if units[index] != encoded[index] {
				return fmt.Errorf("invalid AppContainer directory name encoding")
			}
		}
		if name != "." && name != ".." {
			if err := ordinaryComponent(name); err != nil {
				return err
			}
			entry := directoryEntry{name: name, attributes: binary.LittleEndian.Uint32(buffer[56:60])}
			copy(entry.id[:], buffer[72:88])
			if entry.id == [16]byte{} {
				return fmt.Errorf("AppContainer directory entry identity unavailable")
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
		if next == 0 {
			return nil
		}
		buffer = buffer[next:]
	}
}
