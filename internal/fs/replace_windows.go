//go:build windows

package fs

import (
	"os"
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
	return atomicRename(source, target, windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS)
}

func atomicCreate(source, target string) error {
	return atomicRename(source, target, windows.FILE_RENAME_POSIX_SEMANTICS)
}

func atomicRename(source, target string, flags uint32) error {
	nativeSource, err := nativePath(source)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(nativeSource)
	if err != nil {
		return err
	}
	file, err := windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return &os.LinkError{Op: "replace", Old: source, New: target, Err: err}
	}
	defer windows.CloseHandle(file)
	absolute, err := nativePath(target)
	if err != nil {
		return err
	}
	encoded, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	var header renameInfo
	offset := unsafe.Offsetof(header.Name)
	buffer := make([]byte, int(offset)+2*len(encoded))
	info := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	info.Flags = flags
	// Keep the UTF-16 terminator in the backing buffer. The Win32 rename path
	// consumes a terminated name even though FileNameLength excludes that NUL.
	// Otherwise adjacent heap data can become part of the destination filename.
	info.NameLength = uint32(2 * (len(encoded) - 1))
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(encoded)), encoded)
	// POSIX replacement preserves existing readers' handles and makes new opens
	// see the complete replacement. Unsupported filesystems fail the operation.
	if err := windows.SetFileInformationByHandle(file, windows.FileRenameInfoEx, &buffer[0], uint32(len(buffer))); err != nil {
		return &os.LinkError{Op: "replace", Old: source, New: target, Err: err}
	}
	return nil
}
