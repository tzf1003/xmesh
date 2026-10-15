package winbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerIdentityHandleDirectoryRename(t *testing.T) {
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
	tree, err := openWorkspaceTree(context.Background(), root, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	var objects []treeObject
	for _, selected := range tree.objects[1:] {
		h, err := openIdentityObject(tree.ancestors[0], selected)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { windows.CloseHandle(h) })
		selected.handle = h
		objects = append(objects, selected)
	}
	// Keep identity/security handles open while dropping only the pathname
	// descendant opens. The root and ancestor namespace guards remain held.
	for index := 1; index < len(tree.objects); index++ {
		windows.CloseHandle(tree.objects[index].handle)
		tree.objects[index].handle = 0
	}
	moved := dir + ".moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal("populated directory rename with retained file-ID security handles", err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, expected := range objects {
		actual, err := inspectTreeObject(expected.handle, "")
		if err != nil || actual.id != expected.id || actual.directory != expected.directory {
			t.Fatal("file-ID security handle changed after rename/path replacement", err)
		}
		if _, err := readObjectSecurity(expected.handle); err != nil {
			t.Fatal("retained file-ID descriptor access", err)
		}
	}
	if err := os.Rename(root, root+".moved"); err == nil {
		t.Fatal("file-ID admission released the root binding")
	}
}

func Test_WindowsAppContainerIdentityHandlePreparation(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	root := filepath.Join(t.TempDir(), "workspace")
	dir := filepath.Join(root, "directory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
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
	tree, err := openWorkspaceTree(context.Background(), root, 3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Close)
	selected, err := p.grantSelectedObjects(context.Background(), tree)
	if selected != nil {
		t.Cleanup(func() {
			if err := selected.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	g := &workspaceGrant{tree: tree, selected: selected}
	identities, security, paths := []windows.Handle{}, []windows.Handle{}, []windows.Handle{}
	for index, grant := range selected.grants {
		identities = append(identities, grant.handle)
		security = append(security, grant.aclHandle)
		paths = append(paths, tree.objects[index].handle)
	}
	assertOriginal := func() {
		t.Helper()
		for index, grant := range selected.grants {
			if grant.handle != identities[index] || grant.aclHandle != security[index] || tree.objects[index].handle != paths[index] {
				t.Fatal("failed preparation changed owned handles", index)
			}
			for _, h := range []windows.Handle{grant.handle, grant.aclHandle, tree.objects[index].handle} {
				if _, err := readObjectSecurity(h); err != nil {
					t.Fatal("failed preparation closed an original handle", err)
				}
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.retainIdentityHandles(ctx); err == nil {
		t.Fatal("canceled identity preparation succeeded")
	}
	assertOriginal()
	// Fail after the first replacement has been prepared; all original handles
	// must remain available for exact rollback or another admission attempt.
	tree.objects[2].directory = !tree.objects[2].directory
	if err := g.retainIdentityHandles(context.Background()); err == nil {
		t.Fatal("mismatched replacement identity succeeded")
	}
	tree.objects[2].directory = !tree.objects[2].directory
	assertOriginal()
	if err := g.retainIdentityHandles(context.Background()); err != nil {
		t.Fatal(err)
	}
	for index, grant := range selected.grants[1:] {
		if tree.objects[index+1].handle != 0 {
			t.Fatal("pathname descendant handle retained")
		}
		for _, h := range []windows.Handle{grant.handle, grant.aclHandle} {
			var flags uint32
			getHandleInformation := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")
			result, _, err := getHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
			if result == 0 || flags&windows.HANDLE_FLAG_INHERIT != 0 {
				t.Fatal("file-ID grant handle must be valid and non-inheritable", err)
			}
		}
	}
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal("prepared grant handles constrain populated-directory rename", err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal("release prepared grants", err)
	}
}

func Test_WindowsAppContainerObjectIdentityReopen(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	guard, err := openWorkspaceTree(context.Background(), root, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	dir := filepath.Join(root, "original-directory")
	file := filepath.Join(dir, "original.txt")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("selected"), 0600); err != nil {
		t.Fatal(err)
	}
	selected := []treeObject{}
	for _, path := range []string{dir, file} {
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			t.Fatal(err)
		}
		object, err := inspectTreeObject(h, "")
		windows.CloseHandle(h)
		if err != nil {
			t.Fatal(err)
		}
		object.handle = 0
		selected = append(selected, object)
	}
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal("populated directory rename after releasing descendant handles", err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	for index, path := range []string{dir + ".moved", filepath.Join(dir+".moved", "original.txt")} {
		before := readObjectACL(t, path)
		replacement := dir
		if index == 1 {
			replacement = file
		}
		replacementACL := readObjectACL(t, replacement)
		h, err := openIdentityObject(guard.ancestors[0], selected[index])
		if err != nil {
			t.Fatal("reopen admitted identity after populated-directory rename", err)
		}
		object := selected[index]
		object.handle = h
		grant, err := p.grantSelectedObject(object)
		windows.CloseHandle(h)
		if err != nil {
			t.Fatal("grant recovered identity", err)
		}
		if bytes.Equal(before, readObjectACL(t, path)) || !bytes.Equal(replacementACL, readObjectACL(t, replacement)) {
			_ = grant.Close()
			t.Fatal("identity recovery changed the replacement")
		}
		if err := grant.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, readObjectACL(t, path)) {
			t.Fatal("recovered identity ACL not restored")
		}
	}
	for _, field := range []string{"volume", "id", "type"} {
		invalid := selected[0]
		switch field {
		case "volume":
			invalid.id.Volume ^= 1
		case "id":
			invalid.id.ID = [16]byte{}
		case "type":
			invalid.directory = !invalid.directory
		}
		if h, err := openIdentityObject(guard.ancestors[0], invalid); err == nil {
			windows.CloseHandle(h)
			t.Fatal("invalid recovery identity admitted", field)
		}
	}
	if err := os.Rename(root, root+".moved"); err == nil {
		t.Fatal("identity recovery lost the root binding")
	}
	guard.Close()
	if err := os.Rename(root, root+".moved"); err != nil {
		t.Fatal("identity recovery retained child handles", err)
	}
}
