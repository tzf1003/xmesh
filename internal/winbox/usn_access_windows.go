package winbox

import (
	"errors"
	"fmt"

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
