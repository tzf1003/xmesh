package winbox

import (
	"errors"
	"fmt"
)

// errWriterFenceViolated marks proof that a namespace, reparse or ACL writer
// changed an object the fence pins. It is sticky: a fence never returns to a
// verified state after a violation or after losing the ability to observe one.
var errWriterFenceViolated = errors.New("AppContainer external writer fence violated")

type fenceRole uint8

const (
	// fenceAncestor covers every directory from the volume root through the
	// parent of the workspace root and of the private journal directory.
	fenceAncestor fenceRole = iota + 1
	// fenceWorkspaceRoot is also changed by this package's own grant and
	// cleanup, so security changes on it are validated by the grant owner.
	fenceWorkspaceRoot
	// fenceJournal is the private journal directory. Its descriptor is fixed at
	// creation and must not change while any journal is open.
	fenceJournal
)

const (
	usnReasonSecurity  = 0x00000800
	usnReasonRenameOld = 0x00001000
)

// inspectFenceChanges classifies a complete, ordered USN slice against the
// pinned identities. Pinned objects are held without delete sharing, so a
// rename or delete record can only come from a path that bypassed the kernel
// check; either is reported. Reparse and hard-link changes are never
// part of this package's own operations on pinned directories. Security changes
// are reported for ancestors and the journal directory; the workspace root's
// descriptor is changed by this package's own grants and is checked by them.
func inspectFenceChanges(changes []workspaceChange, pinned map[[16]byte]fenceRole) error {
	for _, change := range changes {
		role, ok := pinned[change.ID]
		if !ok {
			continue
		}
		forbidden := uint32(usnReasonDelete | usnReasonRenameOld | usnReasonRenameNew | usnReasonHardLink | usnReasonReparse)
		if role != fenceWorkspaceRoot {
			forbidden |= usnReasonSecurity
		}
		if change.Reason&forbidden != 0 {
			return fmt.Errorf("%w: reason %#x on pinned directory at USN %d", errWriterFenceViolated, change.Reason&forbidden, change.USN)
		}
		if change.Attributes&usnDirectory == 0 {
			return fmt.Errorf("%w: pinned directory identity became a non-directory at USN %d", errWriterFenceViolated, change.USN)
		}
	}
	return nil
}
