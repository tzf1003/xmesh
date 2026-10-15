package winbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func credentialDirectories() ([]string, error) {
	token := windows.GetCurrentProcessToken()
	home, err := token.GetUserProfileDirectory()
	if err != nil {
		return nil, fmt.Errorf("AppContainer credential profile directory: %w", err)
	}
	roaming, err := token.KnownFolderPath(windows.FOLDERID_RoamingAppData, 0)
	if err != nil {
		return nil, fmt.Errorf("AppContainer credential application directory: %w", err)
	}
	return []string{
		filepath.Join(home, ".ssh"), filepath.Join(home, ".config", "gcloud"),
		filepath.Join(home, ".azure"), filepath.Join(home, ".aws"),
		filepath.Join(roaming, "gcloud"),
	}, nil
}

// pinCredentialBoundaries compares actual volume/object identities, including
// ancestors and followed directory aliases. Existing prefixes remain pinned for
// the grant lifetime. The caller coordinates external namespace writers, including
// reparse-data changes and creation of missing credential-directory components.
func pinCredentialBoundaries(ctx context.Context, tree *workspaceTree, paths []string) (_ []windows.Handle, resultErr error) {
	var handles []windows.Handle
	defer func() {
		if resultErr != nil {
			closeCredentialHandles(handles)
		}
	}()
	workspace := map[fileIdentity]bool{}
	for _, object := range tree.objects {
		workspace[object.id] = true
	}
	for _, h := range tree.ancestors {
		object, err := inspectTreeObject(h, "")
		if err != nil {
			return nil, err
		}
		workspace[object.id] = true
	}
	root := tree.objects[0].id
	for _, path := range paths {
		volume, parts, err := workspaceComponents(path)
		if err != nil {
			return nil, fmt.Errorf("AppContainer credential directory: %w", err)
		}
		name, err := windows.UTF16PtrFromString(`\\?\` + volume + `\`)
		if err != nil {
			return nil, err
		}
		parent, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			return nil, fmt.Errorf("open AppContainer credential volume: %w", err)
		}
		handles = append(handles, parent)
		for index, part := range parts {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			ntName, err := windows.NewNTUnicodeString(part)
			if err != nil {
				return nil, err
			}
			attributes := windows.OBJECT_ATTRIBUTES{
				Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: parent,
				ObjectName: ntName, Attributes: windows.OBJ_CASE_INSENSITIVE,
			}
			var child windows.Handle
			var status windows.IO_STATUS_BLOCK
			err = windows.NtCreateFile(&child, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY,
				&attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
				windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_FOR_BACKUP_INTENT, 0, 0)
			if errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.STATUS_OBJECT_PATH_NOT_FOUND) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("open AppContainer credential component: %w", err)
			}
			handles = append(handles, child)
			object, err := inspectTreeObject(child, "")
			if err != nil || !object.directory {
				return nil, fmt.Errorf("inspect AppContainer credential directory: %w", errors.Join(err, requireDirectory(object)))
			}
			// The root must not contain a credential path (even a missing final
			// component). A complete credential root must not contain or alias
			// any workspace object or ancestor.
			if object.id == root || (index == len(parts)-1 && workspace[object.id]) {
				return nil, fmt.Errorf("AppContainer workspace overlaps a credential directory")
			}
			parent = child
		}
	}
	return handles, nil
}

func closeCredentialHandles(handles []windows.Handle) {
	for index := len(handles) - 1; index >= 0; index-- {
		windows.CloseHandle(handles[index])
	}
}
