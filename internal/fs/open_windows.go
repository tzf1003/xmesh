//go:build windows

package fs

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func openRead(path string) (*os.File, error) {
	native, err := nativePath(path)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(native)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// Readers permit atomic replacement while retaining their original data.
	// Go's os.Open omits FILE_SHARE_DELETE on Windows.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// Win32 calls need an extended absolute path beyond the legacy path limit.
func nativePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if len(absolute) < 248 || strings.HasPrefix(absolute, `\\?\`) || strings.HasPrefix(absolute, `\\.\`) {
		return absolute, nil
	}
	if strings.HasPrefix(absolute, `\\`) {
		return `\\?\UNC\` + absolute[2:], nil
	}
	return `\\?\` + absolute, nil
}
