package winbox

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openIdentityObject reopens an admitted object through a retained handle on its
// volume. It does not consult a pathname. Callers keep the root namespace guard
// and coordinate external writers; this primitive does not provide a recovery
// journal or track the lifetime of objects created during execution.
func openIdentityObject(volume windows.Handle, expected treeObject) (windows.Handle, error) {
	var anchor fileIdentity
	if err := windows.GetFileInformationByHandleEx(volume, windows.FileIdInfo, (*byte)(unsafe.Pointer(&anchor)), uint32(unsafe.Sizeof(anchor))); err != nil {
		return 0, err
	}
	if anchor.Volume != expected.id.Volume || expected.id.ID == [16]byte{} {
		return 0, fmt.Errorf("AppContainer object recovery volume or identity mismatch")
	}
	// FILE_OPEN_BY_FILE_ID interprets the Unicode-string buffer as the raw
	// 128-bit file ID, rather than text; no case folding is appropriate.
	name := windows.NTUnicodeString{
		Length: 16, MaximumLength: 16, Buffer: (*uint16)(unsafe.Pointer(&expected.id.ID[0])),
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: volume,
		ObjectName: &name, Attributes: windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err := windows.NtCreateFile(&handle, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
		&attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_OPEN_BY_FILE_ID|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT, 0, 0)
	runtime.KeepAlive(expected)
	if err != nil {
		return 0, fmt.Errorf("open AppContainer object by identity: %w", err)
	}
	actual, err := inspectTreeObject(handle, "")
	if err != nil || actual.id != expected.id || actual.directory != expected.directory {
		windows.CloseHandle(handle)
		if errors.Is(err, errNonOrdinaryObject) {
			return 0, fmt.Errorf("AppContainer recovered object identity or type mismatch: %w", err)
		}
		return 0, fmt.Errorf("AppContainer recovered object identity or type mismatch: %v", err)
	}
	return handle, nil
}
