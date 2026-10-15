//go:build windows

package fs

import (
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FILE_RENAME_INFO uses pointer alignment for RootDirectory on each architecture.
type renameInfo struct {
	Flags         uint32
	RootDirectory windows.Handle
	NameLength    uint32
	Name          [1]uint16
}

func atomicReplace(source, target string) error {
	name, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	file, err := windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return &os.LinkError{Op: "replace", Old: source, New: target, Err: err}
	}
	defer windows.CloseHandle(file)
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	encoded, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	encoded = encoded[:len(encoded)-1]
	var header renameInfo
	offset := unsafe.Offsetof(header.Name)
	buffer := make([]byte, int(offset)+2*len(encoded))
	info := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.NameLength = uint32(2 * len(encoded))
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(encoded)), encoded)
	// POSIX replacement preserves existing readers' handles and makes new opens
	// see the complete replacement. Unsupported filesystems fail the operation.
	if err := windows.SetFileInformationByHandle(file, windows.FileRenameInfoEx, &buffer[0], uint32(len(buffer))); err != nil {
		return &os.LinkError{Op: "replace", Old: source, New: target, Err: err}
	}
	return nil
}
