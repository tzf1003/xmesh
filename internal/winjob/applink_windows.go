package winjob

import (
	"golang.org/x/sys/windows"
)

// ioReparseTagAppExecLink is IO_REPARSE_TAG_APPEXECLINK, the reparse tag of a
// Microsoft Store application execution alias under WindowsApps.
const ioReparseTagAppExecLink = 0x8000001B

// IsAppExecLink reports whether path is a Store application execution alias.
// Launching one activates its package, which places the process tree in the
// package's own Job instead of the caller's.
func IsAppExecLink(path string) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	var data windows.Win32finddata
	handle, err := windows.FindFirstFile(name, &data)
	if err != nil {
		return false
	}
	_ = windows.FindClose(handle)
	return data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 && data.Reserved0 == ioReparseTagAppExecLink
}
