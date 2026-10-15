package winbox

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Serialize ACL read/merge/write operations performed by this package. Each
// release reads the current ACL so changes made between acquire and release
// survive. External ACL writers must coordinate with the workspace owner.
var objectACLMutex sync.Mutex

var (
	reopenObject      = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")
	setObjectSecurity = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtSetSecurityObject")
)

// objectGrant retains one explicitly selected object until its ACE is removed.
// This is a building block for workspace admission, not a recursive workspace
// grant: callers must establish and protect the allowed namespace separately.
// Rename/delete sharing preserves editor semantics; release follows the same
// object even if another file later occupies its original path.
type objectGrant struct {
	profile   *Profile
	handle    windows.Handle
	aclHandle windows.Handle
	ace       []byte
}

func (p *Profile) grantObject(path string) (*objectGrant, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Retain metadata and security access: DELETE access would conflict with
	// ordinary readers that omit FILE_SHARE_DELETE.
	// OPEN_REPARSE_POINT lets us reject the final object itself.
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	object, err := inspectTreeObject(h, "")
	if err != nil {
		return nil, err
	}
	return p.grantSelectedObject(object)
}

// grantSelectedObject grants the object selected by workspace inspection.
// DuplicateHandle retains that file object and its sharing mode without another
// path lookup. The grant owns the duplicate independently of the snapshot.
// Callers serialize snapshot closure with this operation and protect the allowed
// namespace for the complete execution lifetime.
func (p *Profile) grantSelectedObject(object treeObject) (*objectGrant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sid == nil {
		return nil, fmt.Errorf("AppContainer profile unavailable")
	}
	if err := reopenObject.Find(); err != nil {
		return nil, err
	}
	var h windows.Handle
	process := windows.CurrentProcess()
	if err := windows.DuplicateHandle(process, object.handle, process, &h, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, fmt.Errorf("retain selected AppContainer object: %w", err)
	}
	retained := false
	defer func() {
		if !retained {
			windows.CloseHandle(h)
		}
	}()
	actual, err := inspectTreeObject(h, object.relative)
	if err != nil {
		return nil, err
	}
	if actual.id != object.id || actual.directory != object.directory {
		return nil, fmt.Errorf("AppContainer selected object identity changed")
	}
	// Acquire security access before admission and retain it through release.
	// A delete-pending object cannot be reopened, but its existing security
	// handle can still remove the grant after the sandbox has been reaped.
	aclHandle, err := reopenForACL(h)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !retained {
			windows.CloseHandle(aclHandle)
		}
	}()
	mask := windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE)
	if actual.directory {
		const fileDeleteChild = 0x0040
		mask |= fileDeleteChild
	}
	var pin runtime.Pinner
	pin.Pin(p.sid)
	defer pin.Unpin()
	entry, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(p.sid),
		},
	}}, nil)
	if err != nil {
		return nil, err
	}
	_, aces, err := aclParts(entry)
	if err != nil || len(aces) != 1 {
		return nil, fmt.Errorf("AppContainer grant ACE: %v", err)
	}
	ace := bytes.Clone(aces[0])
	objectACLMutex.Lock()
	defer objectACLMutex.Unlock()
	if err := setObjectGrant(aclHandle, ace, true); err != nil {
		return nil, err
	}
	p.grants++
	retained = true
	return &objectGrant{profile: p, handle: h, aclHandle: aclHandle, ace: ace}, nil
}

// Close removes exactly the acquired ACE from the current ACL. Failed releases
// retain the handle and profile reference for retry. Reap sandbox processes first.
func (g *objectGrant) Close() error {
	g.profile.mu.Lock()
	defer g.profile.mu.Unlock()
	if g.handle == 0 {
		return nil
	}
	objectACLMutex.Lock()
	defer objectACLMutex.Unlock()
	if err := setObjectGrant(g.aclHandle, g.ace, false); err != nil {
		return err
	}
	windows.CloseHandle(g.aclHandle)
	g.aclHandle = 0
	windows.CloseHandle(g.handle)
	g.handle = 0
	g.profile.grants--
	return nil
}

// Edit through an already admitted READ_CONTROL|WRITE_DAC handle, including
// objects that have since entered the delete-pending state.
func setObjectGrant(handle windows.Handle, ace []byte, add bool) error {
	if err := setObjectSecurity.Find(); err != nil {
		return err
	}
	current, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, defaulted, err := current.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return fmt.Errorf("AppContainer object grant requires a present, non-null DACL")
	}
	merged, err := editObjectACL(dacl, ace, add)
	if err != nil {
		return err
	}
	absolute, err := current.ToAbsolute()
	if err != nil {
		return err
	}
	if err := absolute.SetDACL(merged, true, defaulted); err != nil {
		return err
	}
	control, _, err := current.Control()
	if err != nil {
		return err
	}
	// NtSetSecurityObject consumes AUTO_INHERIT_REQ to preserve an existing
	// AUTO_INHERITED state. Supplying AUTO_INHERITED alone clears it on NTFS.
	// This direct kernel update does not walk existing children.
	if control&windows.SE_DACL_AUTO_INHERITED != 0 {
		if err := absolute.SetControl(windows.SE_DACL_AUTO_INHERIT_REQ, windows.SE_DACL_AUTO_INHERIT_REQ); err != nil {
			return err
		}
	}
	relative, err := absolute.ToSelfRelative()
	if err != nil {
		return err
	}
	information := uint32(windows.DACL_SECURITY_INFORMATION)
	if control&windows.SE_DACL_PROTECTED != 0 {
		information |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		information |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	// Set the selected kernel object's descriptor directly. SetSecurityInfo's
	// recursive inheritance propagation would touch unselected child objects.
	// Preserve the current descriptor's control bits and update only its DACL.
	// Explicitly carry the current DACL protection choice into the update.
	status, _, _ := setObjectSecurity.Call(uintptr(handle), uintptr(information), uintptr(unsafe.Pointer(relative)))
	runtime.KeepAlive(relative)
	if status != 0 {
		return fmt.Errorf("set AppContainer object DACL: %w", windows.NTStatus(status))
	}
	return nil
}

func reopenForACL(object windows.Handle) (windows.Handle, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(object, &info); err != nil {
		return 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		h, _, err := reopenObject.Call(uintptr(object), windows.READ_CONTROL|windows.WRITE_DAC,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT)
		if windows.Handle(h) == windows.InvalidHandle {
			return 0, fmt.Errorf("ReOpenFile for AppContainer ACL edit: %w", err)
		}
		return windows.Handle(h), nil
	}
	// An empty relative NT name reopens the directory handle itself. This
	// follows object identity even after rename and accepts directory objects.
	name, err := windows.NewNTUnicodeString("")
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: object,
		ObjectName:    name,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.READ_CONTROL|windows.WRITE_DAC, &attributes, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("reopen AppContainer directory for ACL edit: %w", err)
	}
	return handle, nil
}

func objectDACL(handle windows.Handle) (*windows.ACL, error) {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	if dacl == nil {
		return nil, fmt.Errorf("AppContainer object grant requires a present, non-null DACL")
	}
	return dacl, nil
}

// Preserve every unrelated ACE byte-for-byte, including object/callback ACEs.
// Explicit grants precede inherited ACEs. No inheritance flags are introduced.
func editObjectACL(acl *windows.ACL, target []byte, add bool) (*windows.ACL, error) {
	header, aces, err := aclParts(acl)
	if err != nil {
		return nil, err
	}
	matches := 0
	for _, ace := range aces {
		if bytes.Equal(ace, target) {
			matches++
		}
	}
	if (add && matches != 0) || (!add && matches != 1) {
		return nil, fmt.Errorf("AppContainer grant ACE ownership changed: matches=%d", matches)
	}
	result := bytes.Clone(header)
	count := 0
	inserted := false
	for _, ace := range aces {
		if add && !inserted && ace[1]&windows.INHERITED_ACE != 0 {
			result = append(result, target...)
			count++
			inserted = true
		}
		if !add && bytes.Equal(ace, target) {
			continue
		}
		result = append(result, ace...)
		count++
	}
	if add && !inserted {
		result = append(result, target...)
		count++
	}
	if len(result) > 65535 || count > 65535 {
		return nil, fmt.Errorf("AppContainer object ACL too large")
	}
	binary.LittleEndian.PutUint16(result[2:4], uint16(len(result)))
	binary.LittleEndian.PutUint16(result[4:6], uint16(count))
	return (*windows.ACL)(unsafe.Pointer(&result[0])), nil
}

func aclParts(acl *windows.ACL) ([]byte, [][]byte, error) {
	if acl == nil {
		return nil, nil, fmt.Errorf("null object ACL")
	}
	header := unsafe.Slice((*byte)(unsafe.Pointer(acl)), 8)
	size := int(binary.LittleEndian.Uint16(header[2:4]))
	if (header[0] != 2 && header[0] != 4) || size < 8 {
		return nil, nil, fmt.Errorf("invalid object ACL header")
	}
	body := unsafe.Slice((*byte)(unsafe.Pointer(acl)), size)
	aces := make([][]byte, 0, acl.AceCount)
	pos := 8
	for range acl.AceCount {
		if len(body)-pos < 4 {
			return nil, nil, fmt.Errorf("truncated object ACE header")
		}
		length := int(binary.LittleEndian.Uint16(body[pos+2 : pos+4]))
		if length < 4 || length%4 != 0 || length > len(body)-pos {
			return nil, nil, fmt.Errorf("invalid object ACE length")
		}
		aces = append(aces, body[pos:pos+length])
		pos += length
	}
	return header, aces, nil
}
