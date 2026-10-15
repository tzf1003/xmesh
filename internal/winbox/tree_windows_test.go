package winbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerWorkspaceTree(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	parent := filepath.Join(t.TempDir(), "ancestor")
	root := filepath.Join(parent, "workspace")
	sub := filepath.Join(root, "子目录")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	// More than one directory-information buffer, including Unicode names.
	for index := range 700 {
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("file-%04d-资料.txt", index)), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	firstFile := filepath.Join(sub, "file-0000-资料.txt")
	before := readObjectACL(t, firstFile)
	tree, err := openWorkspaceTree(context.Background(), root, 702)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Close)
	if len(tree.objects) != 702 {
		t.Fatal("workspace object count", len(tree.objects))
	}
	objects := map[string]treeObject{}
	for _, object := range tree.objects {
		objects[object.relative] = object
	}
	if !objects[""].directory || !objects["子目录"].directory || objects[filepath.Join("子目录", "file-0000-资料.txt")].directory {
		t.Fatal("workspace object types")
	}
	if !bytes.Equal(before, readObjectACL(t, firstFile)) {
		t.Fatal("snapshot changed ACL")
	}
	if err := os.Rename(root, root+".moved"); err == nil {
		t.Fatal("workspace root binding moved")
	}
	if err := os.Rename(parent, parent+".moved"); err == nil {
		t.Fatal("workspace ancestor binding moved")
	}
	movedSub := sub + ".moved"
	if err := os.Rename(sub, movedSub); err != nil {
		t.Fatal("descendant directory rename", err)
	}
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstFile, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	visited := 0
	err = enumerateDirectory(context.Background(), objects["子目录"].handle, func(entry directoryEntry) error {
		visited++
		original, ok := objects[filepath.Join("子目录", entry.name)]
		if !ok || original.id.ID != entry.id {
			return fmt.Errorf("enumeration changed to the replacement directory")
		}
		handle, err := openRelativeObject(objects["子目录"].handle, entry.name, true, false)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(handle)
		child, err := inspectTreeObject(handle, entry.name)
		if err != nil {
			return err
		}
		return verifyDirectoryEntry(original.id.Volume, entry, child)
	})
	if err != nil || visited != 700 {
		t.Fatal("enumeration after directory rename", visited, err)
	}
	if err := os.Rename(filepath.Join(movedSub, "file-0000-资料.txt"), filepath.Join(movedSub, "renamed.txt")); err != nil {
		t.Fatal("descendant file rename", err)
	}
	if err := os.Remove(filepath.Join(movedSub, "file-0001-资料.txt")); err != nil {
		t.Fatal("descendant file deletion", err)
	}
	tree.Close()
	tree.Close()
	if err := os.Rename(root, root+".moved"); err != nil {
		t.Fatal("workspace root binding not released", err)
	}
	t.Log("702 objects, multiple enumeration buffers, Unicode, stable root/ancestors, descendant editing and handle-relative identity after rename passed")
}

func Test_WindowsAppContainerWorkspaceTreeAdmission(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, kind := range []string{"hardlink", "symlink", "junction", "ancestor-symlink", "limit", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			secret := filepath.Join(outside, "credential-sentinel")
			if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("workspace"), 0600); err != nil {
				t.Fatal(err)
			}
			before := readObjectACL(t, secret)
			control, err := openWorkspaceTree(context.Background(), root, 100)
			if err != nil {
				t.Fatal("ordinary workspace control", err)
			}
			if len(control.objects) != 2 {
				control.Close()
				t.Fatal("ordinary workspace control count")
			}
			control.Close()
			candidate := root
			limit := 100
			ctx := context.Background()
			switch kind {
			case "hardlink":
				if err := os.Link(secret, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			case "junction":
				if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", filepath.Join(root, "link"), outside).CombinedOutput(); err != nil {
					t.Fatal("junction fixture", string(output), err)
				}
			case "ancestor-symlink":
				alias := filepath.Join(base, "alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				candidate = filepath.Join(alias, "child")
				if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
					t.Fatal(err)
				}
			case "limit":
				limit = 1
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if tree, err := openWorkspaceTree(ctx, candidate, limit); err == nil {
				tree.Close()
				t.Fatal("unsafe or incomplete workspace admitted")
			} else if kind == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation result", err)
			}
			if !bytes.Equal(before, readObjectACL(t, secret)) {
				t.Fatal("outside target ACL changed")
			}
			if body, err := os.ReadFile(secret); err != nil || string(body) != "private" {
				t.Fatal("outside target content changed", err)
			}
			if err := os.Rename(root, root+".moved"); err != nil {
				t.Fatal("failed admission retained root binding", err)
			}
		})
	}
}

func Test_WindowsAppContainerWorkspaceReplacement(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "original.txt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	tree, err := openWorkspaceTree(context.Background(), root, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	matched := false
	stop := errors.New("replacement checked")
	err = enumerateDirectory(context.Background(), tree.objects[0].handle, func(entry directoryEntry) error {
		if entry.name != "original.txt" {
			return fmt.Errorf("unexpected fixture entry")
		}
		if err := os.Rename(path, path+".moved"); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
			return err
		}
		handle, err := openRelativeObject(tree.objects[0].handle, entry.name, true, false)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(handle)
		object, err := inspectTreeObject(handle, entry.name)
		if err != nil {
			return err
		}
		if verifyDirectoryEntry(tree.objects[0].id.Volume, entry, object) == nil {
			return fmt.Errorf("replacement identity accepted")
		}
		matched = true
		return stop
	})
	if !errors.Is(err, stop) || !matched {
		t.Fatal("replacement between enumeration and open", matched, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = enumerateDirectory(ctx, tree.objects[0].handle, func(entry directoryEntry) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("directory enumeration cancellation", err)
	}
}

func Test_WindowsAppContainerWorkspaceComponents(t *testing.T) {
	for _, path := range []string{`C:\work\project`, `c:/work/project`} {
		_, parts, err := workspaceComponents(path)
		if err != nil || len(parts) != 2 || parts[0] != "work" || parts[1] != "project" {
			t.Fatal("valid workspace", path, parts, err)
		}
	}
	for _, path := range []string{`C:\`, `C:work`, `\work`, `\\server\share\work`, `\\?\C:\work`, `\\.\pipe\work`, `C:\work:stream`, `C:\work\..\other`, `C:\work\.\other`, `C:\work\\other`, `C:\work\`, "C:\\work\x00secret", `C:\work\*.txt`} {
		if _, _, err := workspaceComponents(path); err == nil {
			t.Fatal("ambiguous workspace admitted", path)
		}
	}
}

func Test_WindowsAppContainerDirectoryEntryParser(t *testing.T) {
	entry := func(name string, id byte, next bool) []byte {
		units := utf16.Encode([]rune(name))
		size := 88 + len(units)*2
		if next {
			size = (size + 7) &^ 7
		}
		data := make([]byte, size)
		if next {
			binary.LittleEndian.PutUint32(data, uint32(size))
		}
		binary.LittleEndian.PutUint32(data[60:], uint32(len(units)*2))
		data[72] = id
		for index, unit := range units {
			binary.LittleEndian.PutUint16(data[88+index*2:], unit)
		}
		return data
	}
	valid := append(entry(".", 0, true), entry("..", 0, true)...)
	valid = append(valid, entry("资料🌿.txt", 1, true)...)
	valid = append(valid, entry("last.txt", 2, false)...)
	names := []string{}
	if err := parseDirectoryEntries(valid, func(entry directoryEntry) error {
		names = append(names, entry.name)
		return nil
	}); err != nil || len(names) != 2 || names[0] != "资料🌿.txt" || names[1] != "last.txt" {
		t.Fatal("directory entry parser", names, err)
	}
	for _, name := range []string{"", "a/b", `a\b`, "a:stream", "a\x00b"} {
		if err := parseDirectoryEntries(entry(name, 1, false), func(directoryEntry) error { return nil }); err == nil {
			t.Fatal("unsafe entry name accepted", name)
		}
	}
	for _, mutation := range []func([]byte){
		func(data []byte) { binary.LittleEndian.PutUint32(data, 8) },
		func(data []byte) { binary.LittleEndian.PutUint32(data, uint32(len(data)+88)) },
		func(data []byte) { binary.LittleEndian.PutUint32(data[60:], 3) },
		func(data []byte) { binary.LittleEndian.PutUint32(data[60:], uint32(len(data))) },
		func(data []byte) { data[72] = 0 },
		func(data []byte) { binary.LittleEndian.PutUint16(data[88:], 0xd800) },
	} {
		data := entry("file.txt", 1, false)
		mutation(data)
		if err := parseDirectoryEntries(data, func(directoryEntry) error { return nil }); err == nil {
			t.Fatal("malformed directory entry accepted")
		}
	}
	if err := parseDirectoryEntries(make([]byte, 87), func(directoryEntry) error { return nil }); err == nil {
		t.Fatal("truncated directory entry accepted")
	}
	sentinel := errors.New("stop")
	if err := parseDirectoryEntries(entry("file.txt", 1, false), func(directoryEntry) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal("visitor error changed", err)
	}
}
