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

var reopenObject = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// objectGrant retains one explicitly selected object until its ACE is removed.
// This is a building block for workspace admission, not a recursive workspace
// grant: callers must establish and protect the allowed namespace separately.
// Rename/delete sharing preserves editor semantics; release follows the same
// object even if another file later occupies its original path.
type objectGrant struct {
	profile *Profile
	handle  windows.Handle
	ace     []byte
}

func (p *Profile) grantObject(path string) (*objectGrant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sid == nil {
		return nil, fmt.Errorf("AppContainer profile unavailable")
	}
	if err := reopenObject.Find(); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Keep only metadata access between ACL edits: a retained DELETE access
	// would conflict with ordinary readers that omit FILE_SHARE_DELETE.
	// OPEN_REPARSE_POINT lets us reject the final object itself.
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			windows.CloseHandle(h)
		}
	}()
	var info windows.ByHandleFileInformation
	if kind, err := windows.GetFileType(h); err != nil || kind != windows.FILE_TYPE_DISK {
		return nil, fmt.Errorf("AppContainer grant requires a disk object: type=%d, error=%v", kind, err)
	}
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.NumberOfLinks > 1 {
		return nil, fmt.Errorf("AppContainer object grant requires an ordinary object with one link")
	}
	mask := windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE)
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
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
	if err := setObjectGrant(h, ace, true); err != nil {
		return nil, err
	}
	p.grants++
	retained = true
	return &objectGrant{profile: p, handle: h, ace: ace}, nil
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
	if err := setObjectGrant(g.handle, g.ace, false); err != nil {
		return err
	}
	windows.CloseHandle(g.handle)
	g.handle = 0
	g.profile.grants--
	return nil
}

// ReOpenFile retains object identity across rename/replacement. Acquire broad
// access only during the ACL edit, after sandbox processes have exited.
func setObjectGrant(object windows.Handle, ace []byte, add bool) error {
	h, _, callErr := reopenObject.Call(uintptr(object), windows.MAXIMUM_ALLOWED,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if windows.Handle(h) == windows.InvalidHandle {
		return fmt.Errorf("ReOpenFile for AppContainer ACL edit: %w", callErr)
	}
	handle := windows.Handle(h)
	defer windows.CloseHandle(handle)
	dacl, err := objectDACL(handle)
	if err != nil {
		return err
	}
	merged, err := editObjectACL(dacl, ace, add)
	if err != nil {
		return err
	}
	// MAXIMUM_ALLOWED suppresses propagation of existing inheritable ACEs to
	// children: https://learn.microsoft.com/windows/win32/api/aclapi/nf-aclapi-setsecurityinfo
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil)
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
