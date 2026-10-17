package winbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type importMoveFixture struct {
	root, source, journalPath string
	j                         *grantJournal
	p                         *Profile
	g                         *workspaceGrant
	security                  map[string][]byte
	control                   map[string]windows.SECURITY_DESCRIPTOR_CONTROL
	labels                    map[string]string
	workspaceSecurity         map[string][]byte
	workspaceControl          map[string]windows.SECURITY_DESCRIPTOR_CONTROL
	workspaceLabels           map[string]string
}

func newImportMoveFixture(t *testing.T, file bool) *importMoveFixture {
	t.Helper()
	base := t.TempDir()
	f := &importMoveFixture{root: filepath.Join(base, "workspace"), source: filepath.Join(base, "source"), security: map[string][]byte{}, control: map[string]windows.SECURITY_DESCRIPTOR_CONTROL{}, labels: map[string]string{}, workspaceSecurity: map[string][]byte{}, workspaceControl: map[string]windows.SECURITY_DESCRIPTOR_CONTROL{}, workspaceLabels: map[string]string{}}
	if err := os.MkdirAll(filepath.Join(f.root, "parent"), 0700); err != nil {
		t.Fatal(err)
	}
	if file {
		if err := os.WriteFile(f.source, []byte("source"), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.MkdirAll(filepath.Join(f.source, "nested"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{filepath.Join(f.source, "original.txt"), filepath.Join(f.source, "nested", "deep.txt")} {
			if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := filepath.WalkDir(f.root, func(path string, _ os.DirEntry, err error) error {
		if err == nil {
			setMediumLabel(t, path)
			f.workspaceSecurity[path], f.workspaceControl[path] = kernelJournalState(t, path)
			f.workspaceLabels[path] = objectLabel(t, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(f.source, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		setMediumLabel(t, path)
		relative, err := filepath.Rel(f.source, path)
		if err != nil {
			return err
		}
		f.security[relative], f.control[relative] = kernelJournalState(t, path)
		f.labels[relative] = objectLabel(t, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(base, "state")
	privateJournalDirectory(t, state)
	f.journalPath = filepath.Join(state, "move.journal")
	var err error
	f.j, err = openBoundGrantJournal(context.Background(), f.journalPath, true)
	if err != nil {
		t.Fatal(err)
	}
	f.p, err = newProfile(f.j)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f.g != nil {
			if err := f.g.Close(); err != nil {
				t.Error("move fixture workspace cleanup", err)
			}
		}
		if err := f.p.Close(); err != nil {
			t.Error("move fixture profile cleanup", err)
		}
		_ = f.j.Close()
	})
	f.g, err = f.p.grantWorkspace(context.Background(), f.root, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := beginWorkspaceChangeTracking(context.Background(), f.j, f.p.sid, 64); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *importMoveFixture) reopen(t *testing.T) {
	t.Helper()
	_ = f.j.Close()
	var err error
	f.j, err = openBoundGrantJournal(context.Background(), f.journalPath, false)
	if err != nil {
		t.Fatal(err)
	}
	f.p.journal = f.j
}

func (f *importMoveFixture) assertSecurity(t *testing.T, root string) {
	t.Helper()
	for relative, expected := range f.security {
		path := root
		if relative != "." {
			path = filepath.Join(root, relative)
		}
		actual, control := kernelJournalState(t, path)
		if !bytes.Equal(actual, expected) || control != f.control[relative] || objectLabel(t, path) != f.labels[relative] {
			t.Errorf("import move changed unrelated security: %s", relative)
		}
	}
}

func Test_WindowsAppContainerWorkspaceMoveImport(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"directory", "file", "nested-parent", "reimport", "reopen"} {
		t.Run(stage, func(t *testing.T) {
			f := newImportMoveFixture(t, stage == "file")
			destination := filepath.Join(f.root, "imported 日本 😀")
			if stage == "nested-parent" {
				destination = filepath.Join(f.root, "parent", "imported 日本 😀")
			}
			moved, err := f.g.moveImport(context.Background(), f.source, destination, maxWorkspaceChangeEvents)
			if err != nil || !moved {
				t.Fatal("move import", moved, err)
			}
			if _, err := os.Stat(f.source); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("import source still present", err)
			}
			if len(recoveryJournalInfo(f.j).Imports) != 1 || len(f.j.state.created) != len(f.security) {
				t.Fatal("move lost complete import proof")
			}
			if stage == "reimport" {
				if err := os.Rename(destination, f.source); err != nil {
					t.Fatal(err)
				}
				if moved, err := f.g.moveImport(context.Background(), f.source, destination, maxWorkspaceChangeEvents); err != nil || !moved || len(recoveryJournalInfo(f.j).Imports) != 1 {
					t.Fatal("repeat import ownership", moved, err)
				}
			}
			if stage == "reopen" {
				f.reopen(t)
			}
			helper := filepath.Join(f.p.Directory(), "move-import-helper.exe")
			body, err := sandboxHelperImage()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(helper, body, 0700); err != nil {
				t.Fatal(err)
			}
			mode := "directory"
			if stage == "file" {
				mode = "file"
			}
			if proof := runAppContainerHelper(t, f.p, helper, "Test_WindowsAppContainerWorkspaceMoveImportHelper", "XMESH_MOVE_IMPORT_HELPER", destination, mode); string(proof) != "imported" {
				t.Fatal("actual imported access", string(proof))
			}
			if err := f.g.Close(); err != nil {
				t.Fatal("move import reclaim", err)
			}
			f.assertSecurity(t, destination)
			for path, expected := range f.workspaceSecurity {
				actual, control := kernelJournalState(t, path)
				if !bytes.Equal(actual, expected) || control != f.workspaceControl[path] || objectLabel(t, path) != f.workspaceLabels[path] {
					t.Error("workspace security not restored", path)
				}
			}
			if f.p.grants != 0 {
				t.Fatal("workspace references not reclaimed")
			}
			sid := unsafe.Slice((*byte)(unsafe.Pointer(f.p.sid)), f.p.sid.Len())
			if stage != "file" {
				acl, _ := kernelJournalState(t, filepath.Join(destination, "late.txt"))
				if bytes.Contains(acl, sid) {
					t.Fatal("late imported child retains profile access")
				}
			}
			if err := f.p.Close(); err != nil {
				t.Fatal(err)
			}
			f.reopen(t)
			if !f.j.state.complete || !f.j.state.workspaceReleased || len(recoveryJournalInfo(f.j).Imports) != 1 {
				t.Fatal("completed move import replay")
			}
		})
	}
}

func Test_WindowsAppContainerWorkspaceMoveImportHelper(t *testing.T) {
	if os.Getenv("XMESH_MOVE_IMPORT_HELPER") != "1" {
		t.Skip("AppContainer move import helper")
	}
	args := os.Args[len(os.Args)-3:]
	target := args[0]
	if args[1] == "directory" {
		target = filepath.Join(target, "late.txt")
	}
	if err := os.WriteFile(target, []byte("imported"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[2], []byte("imported"), 0600); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerWorkspaceMoveImportSourceBinding(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	f := newImportMoveFixture(t, false)
	tree, err := openImportTree(context.Background(), f.source, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	expected := tree.objects[0]
	for index := range tree.objects {
		windows.CloseHandle(tree.objects[index].handle)
		tree.objects[index].handle = 0
	}
	moved := f.source + ".original"
	if err := os.Rename(f.source, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.source, 0700); err != nil {
		t.Fatal(err)
	}
	if h, err := openImportMoveRoot(tree.ancestors[len(tree.ancestors)-1], filepath.Base(f.source), expected); err == nil || h != 0 {
		if h != 0 {
			windows.CloseHandle(h)
		}
		t.Fatal("substituted source acquired original move authority", err)
	}
	f.assertSecurity(t, moved)
}

func Test_WindowsAppContainerWorkspaceMoveImportGuards(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"collision", "outside", "reparse", "hardlink", "preowned-acl", "append-failure", "cancel", "rename-blocked", "limit"} {
		t.Run(stage, func(t *testing.T) {
			f := newImportMoveFixture(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source, destination := f.source, filepath.Join(f.root, "imported")
			switch stage {
			case "collision":
				if err := os.WriteFile(destination, []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			case "outside":
				destination = filepath.Join(filepath.Dir(f.root), "outside")
			case "reparse":
				source = f.source + ".alias"
				if err := os.Symlink(f.source, source); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(source) })
			case "hardlink":
				link := f.source + ".link"
				if err := os.Link(filepath.Join(f.source, "original.txt"), link); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(link)
			case "preowned-acl":
				seedImportProfileACEs(t, f.g, source)
			case "append-failure":
				_ = f.j.file.Close()
				var err error
				f.j.file, err = os.Open(f.journalPath)
				if err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			case "rename-blocked":
				guard, err := openImportTree(ctx, source, 64)
				if err != nil {
					t.Fatal(err)
				}
				defer guard.Close()
			case "limit":
				f.g.maxObjects = 3
			}
			moved, err := f.g.moveImport(ctx, source, destination, maxWorkspaceChangeEvents)
			if err == nil || moved || f.p.workspaceClosing {
				t.Fatal("unsafe import moved or sealed unrelated execution", moved, err)
			}
			if _, err := os.Stat(f.source); err != nil {
				t.Fatal("failed import lost source", err)
			}
			if stage == "collision" {
				body, err := os.ReadFile(destination)
				if err != nil || string(body) != "occupied" {
					t.Fatal("collision replaced destination", string(body), err)
				}
			} else if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed import created destination", err)
			}
			if stage == "preowned-acl" {
				tree, err := openImportTree(context.Background(), source, 64)
				if err != nil {
					t.Fatal(err)
				}
				for _, object := range tree.objects {
					if err := f.p.removeInheritedGrant(object, true); err != nil {
						t.Fatal(err)
					}
				}
				tree.Close()
			}
			f.assertSecurity(t, f.source)
			if stage == "append-failure" {
				if f.j.blocked == nil {
					t.Fatal("append fault did not reach durable write")
				}
				f.reopen(t)
			}
			f.g.maxObjects = 64
		})
	}
}

func Test_WindowsAppContainerWorkspaceMoveImportFault(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"cancel-after-move", "journal-after-move"} {
		t.Run(stage, func(t *testing.T) {
			f := newImportMoveFixture(t, false)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			owner, err := newExecutionProcesses(f.p, 1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.reap(context.Background(), 1); _ = owner.Close() })
			helper := filepath.Join(f.p.Directory(), "move-fault-helper.exe")
			body, err := sandboxHelperImage()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(helper, body, 0700); err != nil {
				t.Fatal(err)
			}
			process, err := owner.start(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", "member", "unused"}, f.p.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_PROCESS_OWNER_HELPER=1"})
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process.Process)
			defer windows.CloseHandle(process.Thread)
			tree, err := openImportTree(ctx, f.source, 64)
			if err != nil {
				t.Fatal(err)
			}
			id := tree.objects[0].id
			tree.Close()
			lock, err := acquireObjectACL(id, 5000)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			type outcome struct {
				moved bool
				err   error
			}
			done := make(chan outcome, 1)
			destination := filepath.Join(f.root, "imported")
			go func() {
				moved, err := f.g.moveImport(ctx, f.source, destination, maxWorkspaceChangeEvents)
				done <- outcome{moved, err}
			}()
			for {
				if _, err := os.Stat(destination); err == nil {
					break
				}
				select {
				case result := <-done:
					t.Fatal("move ended before ACL fault boundary", result.moved, result.err)
				case <-ctx.Done():
					t.Fatal("namespace move did not reach ACL boundary", ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			// The retained ACL mutex stops the worker after its namespace move.
			// os.File.Close is safe concurrently with the worker's later write.
			if stage == "cancel-after-move" {
				cancel()
			} else if err := f.j.file.Close(); err != nil {
				t.Fatal(err)
			}
			lock.Close()
			result := <-done
			if !result.moved || result.err == nil || !f.p.workspaceClosing || !owner.sealed {
				t.Fatal("post-move failure lost side effect or admission seal", result.moved, result.err)
			}
			if status, err := windows.WaitForSingleObject(process.Process, 5000); err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatal("failed import left sandbox process live", status, err)
			}
			if len(recoveryJournalInfo(f.j).Imports) != 1 || len(f.j.state.created) != len(f.security) {
				t.Fatal("post-move failure lost identity census")
			}
			if stage == "journal-after-move" {
				if f.p.processesConfirmed || f.j.blocked == nil {
					t.Fatal("failed journal confirmed process exit")
				}
				f.reopen(t)
			}
			if err := owner.reap(context.Background(), 1); err != nil {
				t.Fatal("post-move process confirmation retry", err)
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.g.Close(); err != nil {
				t.Fatal("post-move reclaim", err)
			}
			f.assertSecurity(t, destination)
		})
	}
}
