package winbox

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The caller supplies a private, namespace-bound state directory on a local
// persistent-ACL volume, outside every admitted workspace. The file itself is
// single-link, protected and exclusively opened for the journal owner's lifetime.
// Creation is exclusive: an existing recovery record can never be overwritten.
func openGrantJournal(path string, create bool) (*grantJournal, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("AppContainer journal requires an absolute path")
	}
	if _, _, err := workspaceComponents(path); err != nil {
		return nil, fmt.Errorf("AppContainer journal requires an ordinary local drive path: %w", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if create {
		disposition = windows.CREATE_NEW
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, 0, &attributes,
		disposition, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, fmt.Errorf("open AppContainer grant journal: %w", err)
	}
	return loadPrivateGrantJournal(h, path, user.User.Sid)
}

// Take ownership of the newly opened handle, including validated prefixes that
// accompany a replay error. Validation failures close it before returning.
func loadPrivateGrantJournal(h windows.Handle, path string, user *windows.SID) (*grantJournal, error) {
	f := os.NewFile(uintptr(h), path)
	retained := false
	defer func() {
		if !retained {
			f.Close()
		}
	}()
	object, err := inspectTreeObject(h, "")
	if err != nil || object.directory {
		return nil, fmt.Errorf("AppContainer journal requires an ordinary single-link file: %v", err)
	}
	var flags uint32
	if err := windows.GetVolumeInformationByHandle(h, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return nil, err
	}
	if flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return nil, fmt.Errorf("AppContainer journal volume requires persistent ACLs")
	}
	security, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	if err := validateJournalSecurity(security, user); err != nil {
		return nil, err
	}
	j, err := loadGrantJournal(f)
	// A verified prefix accompanies a torn/corrupt tail for conservative cleanup.
	// Such journals retain the exclusive file but refuse every further append.
	retained = j != nil
	return j, err
}

func validateJournalSecurity(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID) error {
	return validateRecoverySecurity(sd, user, 0)
}

func validateRecoverySecurity(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID, flags byte) error {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, user) {
		return fmt.Errorf("AppContainer journal owner mismatch")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("AppContainer journal requires a protected DACL")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	_, aces, err := aclParts(acl)
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	userBytes := unsafe.Slice((*byte)(unsafe.Pointer(user)), user.Len())
	systemBytes := unsafe.Slice((*byte)(unsafe.Pointer(system)), system.Len())
	hasUser, hasSystem := false, false
	for _, ace := range aces {
		const fileAllAccess = 0x001f01ff
		if len(ace) < 8 || ace[0] != windows.ACCESS_ALLOWED_ACE_TYPE || ace[1] != flags || binary.LittleEndian.Uint32(ace[4:8]) != fileAllAccess {
			return fmt.Errorf("AppContainer journal has an unexpected access entry")
		}
		isUser, isSystem := bytes.Equal(ace[8:], userBytes), bytes.Equal(ace[8:], systemBytes)
		if !isUser && !isSystem {
			return fmt.Errorf("AppContainer journal grants another identity access")
		}
		hasUser, hasSystem = hasUser || isUser, hasSystem || isSystem
	}
	if !hasUser || !hasSystem {
		return fmt.Errorf("AppContainer journal lacks required owner/system access")
	}
	return nil
}
