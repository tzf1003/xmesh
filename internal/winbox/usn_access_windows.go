package winbox

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// errChangeSourceAccess marks an account that cannot read the volume's NTFS
// change journal by any non-privileged route. It is returned before any sandbox
// admission so the caller can refuse L2 with a cause an operator can act on.
var errChangeSourceAccess = errors.New("the NTFS change journal of the workspace volume is not readable by this account")

// openChangeSourceHandle returns a handle that answers FSCTL_QUERY_USN_JOURNAL and
// FSCTL_READ_USN_JOURNAL for one volume. device is the volume device path and
// root the retained volume-root GUID path.
//
// The raw volume device needs GENERIC_READ, which an ordinary (non-elevated)
// account is normally denied. Only that denial falls back to the volume-root
// directory, opened with directory-listing access and no privilege, and the
// handle is accepted only after the journal answers a query through it. If both
// routes are refused the error names errChangeSourceAccess and both causes.
func openChangeSourceHandle(device *uint16, root string) (windows.Handle, error) {
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE
	h, deviceErr := windows.CreateFile(device, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, 0, 0)
	if deviceErr == nil {
		return h, nil
	}
	if !errors.Is(deviceErr, windows.ERROR_ACCESS_DENIED) && !errors.Is(deviceErr, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return 0, fmt.Errorf("open AppContainer NTFS change source: %w", deviceErr)
	}
	h, rootErr := openVolumeRootForJournal(root)
	if rootErr == nil {
		return h, nil
	}
	return 0, fmt.Errorf("%w: volume device: %v; volume root: %v; run the Agent as an Administrator or LocalSystem service for L2 sandboxing", errChangeSourceAccess, deviceErr, rootErr)
}

func openVolumeRootForJournal(root string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(name, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, err
	}
	probe := &workspaceChangeSource{handle: h}
	if _, err := probe.query(); err != nil {
		_ = windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}
