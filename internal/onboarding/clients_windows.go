//go:build windows

package onboarding

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func clientSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)")
}

func openClientFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Status readers keep their current snapshot while the writer replaces it.
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func createPrivateClientFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sd, err := clientSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func verifyPrivateClientFile(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errors.New("inspect client configuration permissions")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("client configuration must have private owner permissions")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return errors.New("client configuration must have private owner permissions")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return errors.New("inspect client configuration owner")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("inspect client configuration permissions")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(user.User.Sid) && !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			return errors.New("client configuration must have private owner permissions")
		}
	}
	return nil
}

// The POSIX replacement flag keeps a live target available to new readers while
// existing handles retain the old bytes. MoveFileEx can leave it delete-pending.
type clientRenameInfo struct {
	Flags         uint32
	RootDirectory windows.Handle
	NameLength    uint32
	Name          [1]uint16
}

func renameClientFile(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(from, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	to, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}
	var header clientRenameInfo
	offset := unsafe.Offsetof(header.Name)
	buffer := make([]byte, int(offset)+2*len(to))
	info := (*clientRenameInfo)(unsafe.Pointer(&buffer[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.NameLength = uint32(2 * (len(to) - 1))
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(to)), to)
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, &buffer[0], uint32(len(buffer)))
}

// Contents are flushed before publishing the replacement; directories cannot
// be flushed through Go's os.File on Windows.
func syncClientDir(string) error { return nil }
