package winbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerWorkspaceACEForms(t *testing.T) {
	sid, err := windows.StringToSid("S-1-15-2-123-456-789")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.KeepAlive(sid)
	sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())
	const mask = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | 0x0040
	for _, form := range []struct {
		name                      string
		flags                     byte
		directory, created, allow bool
		mask                      uint32
	}{
		{"inherited-file", 0x10, false, false, true, mask},
		{"inherited-directory", 0x13, true, false, true, mask},
		{"copied-file", 0, false, true, true, mask},
		{"copied-directory", 3, true, true, true, mask},
		{"existing-explicit-file", 0, false, false, false, mask},
		{"existing-explicit-directory", 3, true, false, false, mask},
		{"inherit-only", 0x1b, true, true, false, mask},
		{"unexpected-propagation", 0x11, true, true, false, mask},
		{"file-with-directory-flags", 0x13, false, true, false, mask},
		{"unexpected-rights", 0x13, true, true, false, 0x1f01ff},
	} {
		t.Run(form.name, func(t *testing.T) {
			ace := make([]byte, 8+len(sidBytes))
			ace[1] = form.flags
			binary.LittleEndian.PutUint16(ace[2:4], uint16(len(ace)))
			binary.LittleEndian.PutUint32(ace[4:8], form.mask)
			copy(ace[8:], sidBytes)
			// Opaque callback and unrelated allow entries must retain order and
			// bytes even when the current profile's copied ACE is removed.
			opaque := []byte{9, 0, 8, 0, 1, 2, 3, 4}
			other := bytes.Clone(ace)
			other[len(other)-1] ^= 1
			original := append([]byte{4, 0, 0, 0, 3, 0, 0, 0}, opaque...)
			original = append(original, ace...)
			original = append(original, other...)
			binary.LittleEndian.PutUint16(original[2:4], uint16(len(original)))
			acl := (*windows.ACL)(unsafe.Pointer(&original[0]))
			merged, changed, err := editInheritedProfileACL(acl, sid, form.directory, form.created)
			if !form.allow {
				if err == nil || changed {
					t.Fatal("unexpected workspace ACE accepted")
				}
				return
			}
			if err != nil || !changed {
				t.Fatal("known workspace ACE rejected", err)
			}
			_, aces, err := aclParts(merged)
			if err != nil || len(aces) != 2 || !bytes.Equal(aces[0], opaque) || !bytes.Equal(aces[1], other) {
				t.Fatal("unrelated workspace ACL entries changed", err)
			}
			if _, changed, err := editInheritedProfileACL(merged, sid, form.directory, form.created); err != nil || changed {
				t.Fatal("workspace ACE cleanup is not repeatable", err)
			}
		})
	}
}

type workspaceProof struct {
	AppContainer  bool
	ExistingRead  bool
	ExistingWrite bool
	NestedWrite   bool
	NestedRead    bool
	OutsideRead   bool
	OutsideWrite  bool
}

func Test_WindowsAppContainerWorkspaceGrant(t *testing.T) {
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
	root := filepath.Join(t.TempDir(), "workspace")
	child := filepath.Join(root, "existing-child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(child, "existing.txt")
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	for _, path := range []string{file, outside} {
		if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string][]byte{}
	labels := map[string]string{}
	kernelACLs := map[string][]byte{}
	kernelControls := map[string]windows.SECURITY_DESCRIPTOR_CONTROL{}
	traceState := func(stage string) {
		t.Helper()
		for _, path := range []string{root, child, file} {
			sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			control, _, err := sd.Control()
			if err != nil {
				t.Fatal(err)
			}
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			h, err := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
				nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := readObjectSecurity(h)
			windows.CloseHandle(h)
			if err != nil {
				t.Fatal(err)
			}
			rawControl, _, err := raw.Control()
			if err != nil {
				t.Fatal(err)
			}
			rawACL, _, err := raw.DACL()
			if err != nil {
				t.Fatal(err)
			}
			header, aces, err := aclParts(rawACL)
			if err != nil {
				t.Fatal(err)
			}
			body := bytes.Clone(header)
			for _, ace := range aces {
				body = append(body, ace...)
			}
			if stage == "before" {
				kernelACLs[path], kernelControls[path] = body, rawControl
			} else if stage == "released" && (!bytes.Equal(kernelACLs[path], body) || kernelControls[path] != rawControl) {
				t.Fatalf("kernel descriptor not restored %s: control before=%#x after=%#x ACL before=%x after=%x", path, kernelControls[path], rawControl, kernelACLs[path], body)
			}
			t.Logf("workspace descriptor %s %s: control=%#x kernel-control=%#x ACL=%x", stage, filepath.Base(path), control, rawControl, readObjectACL(t, path))
		}
	}
	for _, path := range []string{root, child, file} {
		setMediumLabel(t, path)
		before[path] = readObjectACL(t, path)
		labels[path] = objectLabel(t, path)
	}
	outsideACL := readObjectACL(t, outside)
	traceState("before")
	g, err := p.grantWorkspace(context.Background(), root, 64)
	if g != nil {
		t.Cleanup(func() {
			if err := g.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	traceState("admitted")
	if err := p.Close(); err == nil {
		t.Fatal("profile deleted before inherited grants were recovered")
	}
	if err := os.Rename(root, root+".moved"); err == nil {
		t.Fatal("workspace root binding lost")
	}
	helper := filepath.Join(p.Directory(), "workspace-helper.exe")
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
	run := func() workspaceProof {
		t.Helper()
		body := runAppContainerHelper(t, p, helper, "Test_WindowsAppContainerWorkspaceHelper", "XMESH_WINBOX_WORKSPACE_HELPER", root, outside)
		var proof workspaceProof
		if err := json.Unmarshal(body, &proof); err != nil {
			t.Fatal(err)
		}
		return proof
	}
	proof := run()
	if !proof.AppContainer || !proof.ExistingRead || !proof.ExistingWrite || !proof.NestedWrite || !proof.NestedRead || proof.OutsideRead || proof.OutsideWrite {
		t.Fatalf("workspace grant proof: %+v", proof)
	}
	created := filepath.Join(root, "new-tree", "level-1", "level-2", "created.txt")
	createdLabel := objectLabel(t, created)
	other, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
	})
	otherGrant, err := other.grantObject(created)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := otherGrant.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := g.Close(); err != nil {
		t.Fatal("recover workspace inheritance", err)
	}
	traceState("released")
	if err := g.Close(); err != nil {
		t.Fatal("repeat workspace release", err)
	}
	for path, acl := range before {
		actual := readObjectACL(t, path)
		actualLabel := objectLabel(t, path)
		if !bytes.Equal(acl, actual) || actualLabel != labels[path] {
			t.Fatalf("existing object ACL or label changed after release %s: ACL before=%x after=%x label before=%s after=%s", path, acl, actual, labels[path], actualLabel)
		}
	}
	if !bytes.Equal(outsideACL, readObjectACL(t, outside)) || objectLabel(t, created) != createdLabel {
		t.Fatal("outside ACL or created object label changed")
	}
	// Inspect every resulting object independently, including the newly created
	// directories, and require no allow ACE for the released profile.
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		acl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		_, aces, err := aclParts(acl)
		if err != nil {
			return err
		}
		sid := unsafe.Slice((*byte)(unsafe.Pointer(p.sid)), p.sid.Len())
		for _, ace := range aces {
			if len(ace) >= 8 && ace[0] == windows.ACCESS_ALLOWED_ACE_TYPE && bytes.Equal(ace[8:], sid) {
				t.Fatal("released profile ACE retained", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	acl, err := objectDACL(otherGrant.handle)
	if err != nil {
		t.Fatal(err)
	}
	_, aces, err := aclParts(acl)
	if err != nil {
		t.Fatal(err)
	}
	foundOther := false
	for _, ace := range aces {
		foundOther = foundOther || bytes.Equal(ace, otherGrant.ace)
	}
	if !foundOther {
		t.Fatal("another profile ACE removed from new file")
	}
	proof = run()
	if !proof.AppContainer || proof.ExistingRead || proof.ExistingWrite || proof.NestedRead || proof.NestedWrite || proof.OutsideRead || proof.OutsideWrite {
		t.Fatalf("released workspace proof: %+v", proof)
	}
	if err := otherGrant.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+".moved"); err != nil {
		t.Fatal("workspace guard retained after cleanup", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerWorkspaceDirectoryRename(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	root := filepath.Join(t.TempDir(), "workspace")
	dir := filepath.Join(root, "directory")
	file := filepath.Join(dir, "file.txt")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	type descriptorState struct {
		acl     []byte
		control windows.SECURITY_DESCRIPTOR_CONTROL
		label   string
	}
	readState := func(path string) descriptorState {
		t.Helper()
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(h)
		sd, err := readObjectSecurity(h)
		if err != nil {
			t.Fatal(err)
		}
		control, _, err := sd.Control()
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
		return descriptorState{body, control, objectLabel(t, path)}
	}
	before := map[string]descriptorState{}
	for _, path := range []string{root, dir, file} {
		setMediumLabel(t, path)
		before[path] = readState(path)
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
	g, err := p.grantWorkspace(context.Background(), root, 64)
	if g != nil {
		t.Cleanup(func() {
			if err := g.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(p.Directory(), "rename-helper.exe")
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
	// The actual AppContainer performs the rename, path replacement and writes
	// before any grant is released. Helper failures propagate through its exit.
	runAppContainerHelper(t, p, helper, "Test_WindowsAppContainerWorkspaceRenameHelper", "XMESH_WINBOX_RENAME_HELPER", root, "")
	if err := os.Rename(root, root+".moved"); err == nil {
		t.Fatal("directory rename released the workspace root guard")
	}
	if err := g.Close(); err != nil {
		t.Fatal("release renamed workspace grants", err)
	}
	for old, expected := range before {
		path := old
		if old == dir {
			path = dir + ".moved"
		} else if old == file {
			path = filepath.Join(dir+".moved", "file.txt")
		}
		actual := readState(path)
		if !bytes.Equal(actual.acl, expected.acl) || actual.control != expected.control || actual.label != expected.label {
			t.Fatal("renamed original descriptor not restored", path)
		}
	}
	// Both renamed originals and replacement/new identities must be recovered.
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		state := readState(path)
		_, aces, err := aclParts((*windows.ACL)(unsafe.Pointer(&state.acl[0])))
		if err != nil {
			return err
		}
		sid := unsafe.Slice((*byte)(unsafe.Pointer(p.sid)), p.sid.Len())
		for _, ace := range aces {
			if len(ace) >= 8 && ace[0] == windows.ACCESS_ALLOWED_ACE_TYPE && bytes.Equal(ace[8:], sid) {
				t.Fatal("released profile retained on renamed/replacement tree", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+".moved"); err != nil {
		t.Fatal("root guard retained after renamed workspace release", err)
	}
}

func Test_WindowsAppContainerWorkspaceRenameHelper(t *testing.T) {
	if os.Getenv("XMESH_WINBOX_RENAME_HELPER") != "1" {
		t.Skip("AppContainer workspace rename helper")
	}
	args := os.Args[len(os.Args)-3:]
	dir := filepath.Join(args[0], "directory")
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal("AppContainer populated-directory rename", err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(dir+".moved", "file.txt"): "edited original",
		filepath.Join(dir, "file.txt"):          "replacement",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(dir+".moved", "new", "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "created.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[2], []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerWorkspaceCleanupRetry(t *testing.T) {
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
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	before := readObjectACL(t, root)
	g, err := p.grantWorkspace(context.Background(), root, 1)
	if g != nil {
		t.Cleanup(func() {
			if err := g.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(root, "new-directory")
	if err := os.Mkdir(created, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(created) })
	if err := g.Close(); err == nil {
		t.Fatal("incomplete cleanup scan accepted")
	}
	if err := p.Close(); err == nil {
		t.Fatal("profile deleted after incomplete cleanup")
	}
	if err := os.Rename(root, root+".moved"); err == nil {
		t.Fatal("namespace guard released after incomplete cleanup")
	}
	if err := os.Remove(created); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal("retry inherited cleanup", err)
	}
	if !bytes.Equal(before, readObjectACL(t, root)) {
		t.Fatal("root ACL not restored after cleanup retry")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerWorkspaceHelper(t *testing.T) {
	if os.Getenv("XMESH_WINBOX_WORKSPACE_HELPER") != "1" {
		t.Skip("AppContainer workspace helper")
	}
	args := os.Args[len(os.Args)-3:]
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil {
		t.Fatal(err)
	}
	proof := workspaceProof{AppContainer: container != 0}
	existing := filepath.Join(args[0], "existing-child", "existing.txt")
	_, err := os.ReadFile(existing)
	proof.ExistingRead = err == nil
	proof.ExistingWrite = os.WriteFile(existing, []byte("updated"), 0600) == nil
	nested := filepath.Join(args[0], "new-tree", "level-1", "level-2")
	if err := os.MkdirAll(nested, 0700); err == nil {
		file := filepath.Join(nested, "created.txt")
		proof.NestedWrite = os.WriteFile(file, []byte("created"), 0600) == nil
		body, err := os.ReadFile(file)
		proof.NestedRead = err == nil && string(body) == "created"
	}
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
