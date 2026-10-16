//go:build windows

package privdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const fileGenericRead = 0x120089

// principals are the only security principals a private directory grants
// access to: the account running the process, SYSTEM and Administrators. Under
// `install --system` the running account is SYSTEM, so the set collapses to
// SYSTEM and Administrators.
type principals []*windows.SID

func currentPrincipals() (principals, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	var out principals
	for _, sid := range []*windows.SID{user.User.Sid, system, admins} {
		if !out.has(sid) {
			out = append(out, sid)
		}
	}
	return out, nil
}

func (p principals) has(sid *windows.SID) bool {
	for _, s := range p {
		if s.Equals(sid) {
			return true
		}
	}
	return false
}

// sddl is a protected DACL with full control for every principal. Directories
// carry object and container inheritance so files and folders created later
// receive the same access.
func (p principals) sddl(directory bool) string {
	flags := ""
	if directory {
		flags = "OICI"
	}
	var b strings.Builder
	b.WriteString("D:P")
	for _, sid := range p {
		fmt.Fprintf(&b, "(A;%s;FA;;;%s)", flags, sid.String())
	}
	return b.String()
}

func (p principals) dacl(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.ACL, error) {
	sd, err := windows.SecurityDescriptorFromString(p.sddl(directory))
	if err != nil {
		return nil, nil, err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, nil, err
	}
	return sd, acl, nil
}

// ownerProblem reports an owner who could rewrite the DACL later. Only the
// running account, SYSTEM and Administrators are acceptable owners; a directory
// owned by anyone else cannot be made private by changing its DACL.
func (p principals) ownerProblem(sd *windows.SECURITY_DESCRIPTOR) string {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return "owner unreadable"
	}
	if !p.has(owner) {
		return "owned by " + owner.String()
	}
	return ""
}

// daclProblem returns why a descriptor is not private, or "" when it is.
// requireExact additionally demands the protected canonical shape: inheritance
// disabled and every principal present.
func (p principals) daclProblem(sd *windows.SECURITY_DESCRIPTOR, requireExact bool) string {
	acl, _, err := sd.DACL()
	if err != nil {
		return "DACL unreadable"
	}
	if acl == nil {
		return "null DACL grants everyone access"
	}
	if requireExact {
		control, _, err := sd.Control()
		if err != nil {
			return "control bits unreadable"
		}
		if control&windows.SE_DACL_PROTECTED == 0 {
			return "inherits access from its parent"
		}
	}
	granted := make([]bool, len(p))
	for i := 0; i < int(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			return "ACE unreadable"
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if !p.has(sid) {
				return "grants access to " + sid.String()
			}
			for j, s := range p {
				if s.Equals(sid) {
					granted[j] = true
				}
			}
		default:
			return fmt.Sprintf("has unsupported ACE type %d", ace.Header.AceType)
		}
	}
	if requireExact {
		for j, s := range p {
			if !granted[j] {
				return "lacks access for " + s.String()
			}
		}
	}
	return ""
}

func openDirectory(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	// Without DELETE sharing, the directory cannot be renamed or replaced while
	// it is inspected and tightened.
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		windows.CloseHandle(h)
		return 0, errors.New("not a directory")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return 0, errors.New("is a symbolic link or junction")
	}
	return h, nil
}

// Ensure makes path a private directory.
//
// A new directory is created with the protected DACL already attached, so there
// is no window in which it carries the parent's access. An existing directory is
// inspected through a handle that blocks rename and delete: if it is owned by
// another account, a reparse point, or not a directory, Ensure fails. If its
// DACL is not already the protected private one, Ensure replaces it and
// propagates the change to existing children, then sweeps the tree. The sweep
// fails on an entry owned by another account (that owner can still rewrite its
// ACL), on a link, and resets any child that explicitly grants other accounts.
func Ensure(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	p, err := currentPrincipals()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return err
	}
	sd, acl, err := p.dacl(true)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(name, &sa); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("create private directory %s: %w", abs, err)
	}
	h, err := openDirectory(abs)
	if err != nil {
		return fmt.Errorf("private directory %s: %w", abs, err)
	}
	defer windows.CloseHandle(h)
	current, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read security of %s: %w", abs, err)
	}
	if problem := p.ownerProblem(current); problem != "" {
		return fmt.Errorf("private directory %s is %s; remove it or choose another --state-dir", abs, problem)
	}
	if p.daclProblem(current, true) == "" {
		return nil
	}
	if err := windows.SetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("restrict access to %s: %w", abs, err)
	}
	current, err = windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read security of %s: %w", abs, err)
	}
	if problem := p.daclProblem(current, true); problem != "" {
		return fmt.Errorf("private directory %s still %s after restricting it", abs, problem)
	}
	return p.sweep(abs)
}

func (p principals) sweep(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if attrs, ok := info.Sys().(*syscall.Win32FileAttributeData); ok && attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("%s is a symbolic link or junction inside a private directory", path)
		}
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return fmt.Errorf("read security of %s: %w", path, err)
		}
		if problem := p.ownerProblem(sd); problem != "" {
			return fmt.Errorf("%s inside a private directory is %s; remove it", path, problem)
		}
		if p.daclProblem(sd, false) == "" {
			return nil
		}
		_, acl, err := p.dacl(d.IsDir())
		if err != nil {
			return err
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			return fmt.Errorf("restrict access to %s: %w", path, err)
		}
		return nil
	})
}

// GrantRead adds a read-only allow entry for each SID (string form, for example
// the installer of a SYSTEM service) to one file inside a private directory. The
// directory keeps denying listing and every other file stays private; the named
// SIDs can open this one file by its exact path because Windows lets every
// account traverse directories it cannot list. Use it for the small manifest a
// desktop UI must read, never for key material.
//
// Ensure resets such entries when it has to tighten the directory again.
func GrantRead(path string, sids ...string) error {
	if len(sids) == 0 {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, s := range sids {
		sid, err := windows.StringToSid(s)
		if err != nil {
			return fmt.Errorf("invalid SID %q: %w", s, err)
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: fileGenericRead,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	current, _, err := sd.DACL()
	if err != nil {
		return err
	}
	merged, err := windows.ACLFromEntries(entries, current)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil)
}

// Verify reports whether path is a private directory as Ensure leaves it:
// a real directory owned by an acceptable account, with a protected DACL that
// grants only the running account, SYSTEM and Administrators. It changes nothing.
func Verify(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	p, err := currentPrincipals()
	if err != nil {
		return err
	}
	h, err := openDirectory(abs)
	if err != nil {
		return fmt.Errorf("private directory %s: %w", abs, err)
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if problem := p.ownerProblem(sd); problem != "" {
		return fmt.Errorf("directory %s is %s", abs, problem)
	}
	if problem := p.daclProblem(sd, true); problem != "" {
		return fmt.Errorf("directory %s %s", abs, problem)
	}
	return nil
}
