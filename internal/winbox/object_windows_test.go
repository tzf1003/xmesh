package winbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/tzf1003/xmesh/internal/winjob"
	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerObjectGrant(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(workspace, "existing.txt")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(workspace, "ungranted")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(child, "secret.txt")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(workspace), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, path := range []string{workspace, file} {
		setMediumLabel(t, path)
		labels[path] = objectLabel(t, path)
	}
	before := readObjectACL(t, secret)
	directoryGrant, err := p.grantObject(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := directoryGrant.Close(); err != nil {
			t.Error(err)
		}
	})
	fileGrant, err := p.grantObject(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fileGrant.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := p.Close(); err == nil {
		t.Fatal("profile deleted with active grants")
	}
	if duplicate, err := p.grantObject(file); err == nil {
		duplicate.Close()
		t.Fatal("duplicate object grant accepted")
	}
	if !bytes.Equal(before, readObjectACL(t, secret)) {
		t.Fatal("grant propagated to existing child")
	}
	// A second profile's ACE must survive the first profile's release.
	other, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
	})
	otherGrant, err := other.grantObject(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := otherGrant.Close(); err != nil {
			t.Error(err)
		}
	})
	helper := filepath.Join(p.Directory(), "object-helper.exe")
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, body, 0700); err != nil {
		t.Fatal(err)
	}
	proof := runObjectHelper(t, p, helper, workspace, outside)
	if !proof.AppContainer || !proof.Read || !proof.Write || !proof.Create || proof.ChildRead || proof.ChildWrite || proof.OutsideRead || proof.OutsideWrite {
		t.Fatalf("object grant proof: %+v", proof)
	}
	for path, before := range labels {
		if got := objectLabel(t, path); got != before {
			t.Fatalf("object integrity label changed: %s -> %s", before, got)
		}
	}
	if err := fileGrant.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fileGrant.Close(); err != nil {
		t.Fatal("repeated grant release", err)
	}
	acl, err := objectDACL(otherGrant.handle)
	if err != nil {
		t.Fatal(err)
	}
	_, aces, err := aclParts(acl)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ace := range aces {
		found = found || bytes.Equal(ace, otherGrant.ace)
	}
	if !found {
		t.Fatal("concurrent profile grant removed")
	}
	proof = runObjectHelper(t, p, helper, workspace, outside)
	if proof.Read || proof.Write || !proof.Create {
		t.Fatalf("released file grant proof: %+v", proof)
	}
	if err := directoryGrant.Close(); err != nil {
		t.Fatal(err)
	}
	proof = runObjectHelper(t, p, helper, workspace, outside)
	if proof.Read || proof.Write || proof.Create || proof.ChildRead || proof.ChildWrite || proof.OutsideRead || proof.OutsideWrite {
		t.Fatalf("released directory grant proof: %+v", proof)
	}
	for _, path := range []string{secret, outside} {
		if body, err := os.ReadFile(path); err != nil || string(body) != "private" {
			t.Fatal("ungranted file changed", err)
		}
	}
	if !bytes.Equal(before, readObjectACL(t, secret)) {
		t.Fatal("release changed existing child ACL")
	}
	t.Log("Medium integrity directory/file writes, exact ACE release, concurrent profile preservation, child/outside denial, and unchanged integrity labels passed")
}

type objectProof struct {
	AppContainer bool
	Read         bool
	Write        bool
	Create       bool
	ChildRead    bool
	ChildWrite   bool
	OutsideRead  bool
	OutsideWrite bool
}

func Test_WindowsAppContainerObjectHelper(t *testing.T) {
	if os.Getenv("XMESH_WINBOX_OBJECT_HELPER") != "1" {
		t.Skip("AppContainer object helper")
	}
	args := os.Args[len(os.Args)-3:]
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil {
		t.Fatal(err)
	}
	proof := objectProof{AppContainer: container != 0}
	file := filepath.Join(args[0], "existing.txt")
	_, err := os.ReadFile(file)
	proof.Read = err == nil
	proof.Write = os.WriteFile(file, []byte("changed"), 0600) == nil
	created := filepath.Join(args[0], "created.txt")
	proof.Create = os.WriteFile(created, []byte("created"), 0600) == nil
	// Remove it so the next launch tests directory creation rights again.
	if proof.Create {
		if err := os.Remove(created); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(args[0], "ungranted", "secret.txt")
	_, err = os.ReadFile(secret)
	proof.ChildRead = err == nil
	proof.ChildWrite = os.WriteFile(secret, []byte("changed"), 0600) == nil
	_, err = os.ReadFile(args[1])
	proof.OutsideRead = err == nil
	proof.OutsideWrite = os.WriteFile(args[1], []byte("changed"), 0600) == nil
	body, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[2], body, 0600); err != nil {
		t.Fatal(err)
	}
}

func runObjectHelper(t *testing.T, p *Profile, helper, workspace, outside string) objectProof {
	t.Helper()
	body := runAppContainerHelper(t, p, helper, "Test_WindowsAppContainerObjectHelper", "XMESH_WINBOX_OBJECT_HELPER", workspace, outside)
	var proof objectProof
	if err := json.Unmarshal(body, &proof); err != nil {
		t.Fatal(err)
	}
	return proof
}

func runAppContainerHelper(t *testing.T, p *Profile, helper, testName, marker, workspace, outside string) []byte {
	t.Helper()
	output := filepath.Join(p.Directory(), "object-proof.json")
	_ = os.Remove(output)
	job, err := winjob.New()
	if err != nil {
		t.Fatal(err)
	}
	var process windows.ProcessInformation
	defer func() {
		if process.Process == 0 {
			_ = windows.CloseHandle(job)
			return
		}
		defer windows.CloseHandle(process.Process)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		defer windows.CloseHandle(job)
		// No further process admission occurs in this fixture. Confirm every
		// member has exited before callers reclaim workspace grants/profile.
		if err := winjob.TerminateAndWait(ctx, job, 1, process.Process); err != nil {
			t.Fatal("AppContainer helper process tree cleanup", err)
		}
	}()
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	if err := winjob.Apply(attrs, job); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(attrs); err != nil {
		t.Fatal(err)
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attrs.List()}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{helper, "-test.run=^" + testName + "$", "--", workspace, outside, output}))
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := windows.UTF16PtrFromString(p.Directory())
	env, err := Environment([]string{"SystemRoot=" + os.Getenv("SystemRoot"), marker + "=1"})
	if err != nil {
		t.Fatal(err)
	}
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(nil, command, nil, nil, false, flags, &block[0], dir, &startup.StartupInfo, &process); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Thread)
	defer func() {
		windows.TerminateProcess(process.Process, 1)
		windows.WaitForSingleObject(process.Process, 10000)
	}()
	if result, err := windows.WaitForSingleObject(process.Process, 30000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("object helper wait", result, err)
	}
	var exit uint32
	if err := windows.GetExitCodeProcess(process.Process, &exit); err != nil || exit != 0 {
		t.Fatal("object helper exit", exit, err)
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func setMediumLabel(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("S:(ML;;NW;;;ME)")
	if err != nil {
		t.Fatal(err)
	}
	sacl, _, err := sd.SACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, sacl); err != nil {
		t.Fatal(err)
	}
	actual, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	actualSACL, _, err := actual.SACL()
	if err != nil {
		t.Fatal(err)
	}
	_, expectedACEs, err := aclParts(sacl)
	if err != nil || len(expectedACEs) != 1 {
		t.Fatal("Medium integrity fixture expected ACE", err)
	}
	_, actualACEs, err := aclParts(actualSACL)
	if err != nil || len(actualACEs) != 1 || !bytes.Equal(expectedACEs[0], actualACEs[0]) {
		t.Fatal("Medium integrity fixture label", actual.String(), err)
	}
}

func objectLabel(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

func readObjectACL(t *testing.T, path string) []byte {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	header, aces, err := aclParts(acl)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Clone(header)
	for _, ace := range aces {
		body = append(body, ace...)
	}
	return body
}

func Test_WindowsAppContainerRejectLinkedObject(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	before := readObjectACL(t, file)
	link := file + ".hardlink"
	if err := os.Link(file, link); err != nil {
		t.Fatal(err)
	}
	if grant, err := p.grantObject(file); err == nil {
		grant.Close()
		t.Fatal("hard-linked object accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if grant, err := p.grantObject(link); err == nil {
		grant.Close()
		t.Fatal("reparse object accepted")
	}
	if !bytes.Equal(before, readObjectACL(t, file)) {
		t.Fatal("rejected grant changed target ACL")
	}
}

func Test_WindowsAppContainerGrantReleaseIdentity(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) { testGrantReleaseIdentity(t, kind == "directory") })
	}
}

func Test_WindowsAppContainerGrantReleaseDeletedObject(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			p, err := New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := p.Close(); err != nil {
					t.Error(err)
				}
			})
			path := filepath.Join(t.TempDir(), "selected")
			if kind == "directory" {
				err = os.Mkdir(path, 0700)
			} else {
				err = os.WriteFile(path, []byte("content"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := readObjectACL(t, path)
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			// Keep an independent observer of the object after deletion. Its
			// DELETE right also lets cleanup cancel the pending disposition.
			observer, err := windows.CreateFile(name, windows.DELETE|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
				windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				clear := [4]byte{}
				windows.SetFileInformationByHandle(observer, windows.FileDispositionInfo, &clear[0], uint32(len(clear)))
				windows.CloseHandle(observer)
			})
			grant, err := p.grantObject(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := grant.Close(); err != nil {
					t.Error(err)
				}
			})
			remove := [4]byte{1}
			if err := windows.SetFileInformationByHandle(observer, windows.FileDispositionInfo, &remove[0], uint32(len(remove))); err != nil {
				t.Fatal("mark selected object delete-pending", err)
			}
			var standard struct {
				allocationSize int64
				endOfFile      int64
				links          uint32
				deletePending  byte
				directory      byte
			}
			if err := windows.GetFileInformationByHandleEx(observer, windows.FileStandardInfo, (*byte)(unsafe.Pointer(&standard)), uint32(unsafe.Sizeof(standard))); err != nil {
				t.Fatal(err)
			}
			if standard.deletePending == 0 {
				t.Fatal("selected object is not delete-pending")
			}
			// The name is unavailable while the observer retains the pending
			// object. Windows may still permit reopening an existing handle.
			if named, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
				windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0); err == nil {
				windows.CloseHandle(named)
				t.Fatal("delete-pending object still opens by name")
			}
			if err := p.Close(); err == nil {
				t.Fatal("profile deleted with delete-pending grant")
			}
			if err := grant.Close(); err != nil {
				t.Fatal("release delete-pending object", err)
			}
			if err := grant.Close(); err != nil {
				t.Fatal("repeat delete-pending release", err)
			}
			acl, err := objectDACL(observer)
			if err != nil {
				t.Fatal("observe released delete-pending ACL", err)
			}
			header, aces, err := aclParts(acl)
			if err != nil {
				t.Fatal(err)
			}
			after := bytes.Clone(header)
			for _, ace := range aces {
				after = append(after, ace...)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("delete-pending object ACL not restored")
			}
			if err := p.Close(); err != nil {
				t.Fatal("profile retained after delete-pending release", err)
			}
		})
	}
}

func testGrantReleaseIdentity(t *testing.T, directory bool) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(t.TempDir(), "original")
	create := func(path string) error {
		if directory {
			return os.Mkdir(path, 0700)
		}
		return os.WriteFile(path, []byte("content"), 0600)
	}
	if err := create(path); err != nil {
		t.Fatal(err)
	}
	before := readObjectACL(t, path)
	grant, err := p.grantObject(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := grant.Close(); err != nil {
			t.Error(err)
		}
	})
	moved := path + ".moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal("rename during object grant", err)
	}
	if err := create(path); err != nil {
		t.Fatal(err)
	}
	replacementACL := readObjectACL(t, path)
	if err := grant.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, readObjectACL(t, moved)) {
		t.Fatal("moved object ACL not restored")
	}
	if !bytes.Equal(replacementACL, readObjectACL(t, path)) {
		t.Fatal("replacement object ACL changed")
	}
}

func Test_WindowsAppContainerRejectNullDACL(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(t.TempDir(), "null-dacl.txt")
	if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := before.DACL()
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := before.Control()
	if err != nil {
		t.Fatal(err)
	}
	restoreInfo := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if control&windows.SE_DACL_PROTECTED != 0 {
		restoreInfo = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	t.Cleanup(func() {
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, restoreInfo, nil, nil, dacl, nil); err != nil {
			t.Error(err)
		}
	})
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if grant, err := p.grantObject(path); err == nil {
		grant.Close()
		t.Fatal("null DACL accepted")
	}
}

func Test_WindowsObjectACLEdit(t *testing.T) {
	// Include an opaque callback ACE; it must retain its exact bytes and order.
	deny := []byte{1, 0, 4, 0}
	opaque := []byte{9, 0, 8, 0, 1, 2, 3, 4}
	inherited := []byte{0, windows.INHERITED_ACE, 4, 0}
	target := []byte{0, 0, 8, 0, 5, 6, 7, 8}
	header := []byte{4, 0, 0, 0, 3, 0, 0, 0}
	original := append(bytes.Clone(header), deny...)
	original = append(original, opaque...)
	original = append(original, inherited...)
	binary.LittleEndian.PutUint16(original[2:4], uint16(len(original)))
	acl := (*windows.ACL)(unsafe.Pointer(&original[0]))
	merged, err := editObjectACL(acl, target, true)
	if err != nil {
		t.Fatal(err)
	}
	_, aces, err := aclParts(merged)
	if err != nil || len(aces) != 4 || !bytes.Equal(aces[0], deny) || !bytes.Equal(aces[1], opaque) || !bytes.Equal(aces[2], target) || !bytes.Equal(aces[3], inherited) {
		t.Fatal("ACL merge ordering/preservation", aces, err)
	}
	restored, err := editObjectACL(merged, target, false)
	if err != nil {
		t.Fatal(err)
	}
	got := unsafe.Slice((*byte)(unsafe.Pointer(restored)), len(original))
	if !bytes.Equal(got, original) {
		t.Fatal("ACL release changed unrelated entries")
	}
	if _, err := editObjectACL(merged, target, true); err == nil {
		t.Fatal("duplicate grant accepted")
	}
	if _, err := editObjectACL(restored, target, false); err == nil {
		t.Fatal("missing grant release accepted")
	}
	if _, _, err := aclParts(nil); err == nil {
		t.Fatal("null ACL accepted")
	}
}
