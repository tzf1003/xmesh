package winbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// ErrChangeJournalAccess marks an account that cannot read the NTFS change
// journal of the workspace volume. Reading the journal needs read access to the
// raw volume device, which Windows grants to Administrators and LocalSystem only;
// a handle to the volume-root directory answers FSCTL_QUERY_USN_JOURNAL but is
// refused FSCTL_READ_USN_JOURNAL even for an elevated account. L2 therefore
// cannot be offered to an ordinary account, and the refusal happens before any
// sandbox admission.
var ErrChangeJournalAccess = errors.New("the NTFS change journal of the workspace volume is not readable by this account: run the Agent as an Administrator or LocalSystem service to use L2 sandboxing")

// openChangeSourceHandle opens the raw volume device for the journal. Only an
// access-denied or privilege-not-held refusal is reported as
// ErrChangeJournalAccess; any other failure keeps its own cause.
func openChangeSourceHandle(device *uint16) (windows.Handle, error) {
	h, err := windows.CreateFile(device, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err == nil {
		return h, nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return 0, fmt.Errorf("%w (volume device open: %v)", ErrChangeJournalAccess, err)
	}
	return 0, fmt.Errorf("open AppContainer NTFS change source: %w", err)
}

// ProbeChangeJournalAccess reports whether this account can read the NTFS change
// journal of the volume holding dir, before any runner state is created. Only a
// refusal that is a property of the account is returned, wrapped as
// ErrChangeJournalAccess; a volume or API failure is left to the runner's own
// admission, which names it. Access to the raw volume device is decided by the
// account's token, not by the volume, so one volume stands for all of them.
func ProbeChangeJournalAccess(dir string) error {
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil
	}
	var root [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumePathName(name, &root[0], uint32(len(root))); err != nil {
		return nil
	}
	var volume [64]uint16
	if err := windows.GetVolumeNameForVolumeMountPoint(&root[0], &volume[0], uint32(len(volume))); err != nil {
		return nil
	}
	device, err := windows.UTF16PtrFromString(strings.TrimSuffix(windows.UTF16ToString(volume[:]), `\`))
	if err != nil {
		return nil
	}
	h, err := openChangeSourceHandle(device)
	if err != nil {
		if errors.Is(err, ErrChangeJournalAccess) {
			return err
		}
		return nil
	}
	return windows.CloseHandle(h)
}
