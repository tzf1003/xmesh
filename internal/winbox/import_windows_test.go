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

func Test_WindowsAppContainerWorkspaceImport(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"moved-child", "cancelled-move", "file", "append-failure", "reopen", "limit"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			root, source := filepath.Join(base, "workspace"), filepath.Join(base, "source")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			setMediumLabel(t, root)
			if stage == "file" {
				if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Join(source, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{filepath.Join(source, "original.txt"), filepath.Join(source, "nested", "deep.txt")} {
					if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, control := kernelJournalState(t, root)
			label := objectLabel(t, root)
			state := filepath.Join(base, "state")
			privateJournalDirectory(t, state)
			path := filepath.Join(state, "import.journal")
			j, err := openBoundGrantJournal(ctx, path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			p, err := newProfile(j)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := p.Close(); err != nil {
					t.Error("import profile cleanup", err)
				}
			}()
			g, err := p.grantWorkspace(ctx, root, 64)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := g.Close(); err != nil {
					t.Error("import workspace cleanup", err)
				}
			}()
			if err := beginWorkspaceChangeTracking(ctx, j, p.sid, 64); err != nil {
				t.Fatal(err)
			}
			original, originalControl := kernelJournalState(t, source)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := g.admitImport(cancelled, source, maxWorkspaceChangeEvents); err == nil || len(j.state.created) != 0 {
				t.Fatal("cancelled source admission changed ownership", err)
			}
			if stage == "limit" {
				g.maxObjects = 3
			}
			if stage == "append-failure" {
				_ = j.file.Close()
				j.file, err = os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			if stage == "limit" || stage == "append-failure" {
				if err := g.admitImport(ctx, source, maxWorkspaceChangeEvents); err == nil || len(j.state.created) != 0 {
					t.Fatal("failed import lost source obligations", err)
				}
				if stage == "append-failure" {
					if j.blocked == nil {
						t.Fatal("import append fixture did not fail writing")
					}
					_ = j.Close()
					j, err = openBoundGrantJournal(ctx, path, false)
					if err != nil {
						t.Fatal(err)
					}
					p.journal = j
				}
				g.maxObjects = 64
			}
			if err := g.admitImport(ctx, source, maxWorkspaceChangeEvents); err != nil {
				t.Fatal(err)
			}
			expected := 4
			if stage == "file" {
				expected = 1
			}
			if len(j.state.created) != expected {
				t.Fatal("incomplete import census", len(j.state.created))
			}
			after, afterControl := kernelJournalState(t, source)
			if !bytes.Equal(original, after) || originalControl != afterControl {
				t.Fatal("source census changed descriptor")
			}
			manifest := recoveryJournalInfo(j)
			if len(manifest.Imports) != 1 || len(manifest.Imports[0].Objects) != expected || len(manifest.CreatedObjects) != expected {
				t.Fatal("incomplete detached import manifest")
			}
			manifest.Imports[0].Objects[0].ID[0] ^= 1
			manifest.Imports[0].Cursor.Next++
			manifest.Imports[0].Root.ID[0] ^= 1
			manifest.CreatedObjects[0].ID[0] ^= 1
			if err := validateWorkspaceJournalOwnership(j, p.sid); err != nil {
				t.Fatal("detached import manifest changed owner", err)
			}
			var proof *journalImport
			for _, record := range j.records {
				if record.Import != nil {
					proof = record.Import
				}
			}
			if proof == nil {
				t.Fatal("missing import record")
			}
			proof.Cursor.Next++
			err = validateWorkspaceJournalOwnership(j, p.sid)
			proof.Cursor.Next--
			if err == nil {
				t.Fatal("altered import cursor accepted")
			}
			if err := g.admitImport(ctx, source, maxWorkspaceChangeEvents); err != nil {
				t.Fatal("idempotent source admission", err)
			}
			if len(recoveryJournalInfo(j).Imports) != 1 {
				t.Fatal("repeated source created another import frame")
			}
			if stage == "reopen" {
				_ = j.Close()
				j, err = openBoundGrantJournal(ctx, path, false)
				if err != nil {
					t.Fatal(err)
				}
				p.journal = j
			}
			// Supply copied workspace-format ACEs to exercise precise recovery of
			// imported descriptors. The admission primitive itself changes no ACL.
			seedImportProfileACEs(t, g, source)
			active := source
			if stage != "cancelled-move" {
				active = filepath.Join(root, "imported")
				if err := os.Rename(source, active); err != nil {
					t.Fatal(err)
				}
			}
			moved := ""
			if stage == "moved-child" {
				moved = filepath.Join(base, "moved-child")
				if err := os.Rename(filepath.Join(active, "nested"), moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(moved, "deep.txt")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, "replacement.txt"), []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage != "file" {
				helper := filepath.Join(p.Directory(), "import-helper.exe")
				body, err := sandboxHelperImage()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(helper, body, 0700); err != nil {
					t.Fatal(err)
				}
				target := active
				if moved != "" {
					target = moved
				}
				if body := runAppContainerHelper(t, p, helper, "Test_WindowsAppContainerWorkspaceImportHelper", "XMESH_IMPORT_HELPER", target, ""); string(body) != "imported" {
					t.Fatal("sandbox imported source proof", string(body))
				}
				expected++
			}
			if err := checkpointWorkspaceChanges(ctx, j, p.sid, 64, maxWorkspaceChangeEvents); err != nil {
				t.Fatal("import history checkpoint", err)
			}
			if len(j.state.created) != expected {
				t.Fatal("import descendant history incomplete", len(j.state.created), expected)
			}
			if stage == "moved-child" && len(j.state.deleted) != 1 {
				t.Fatal("imported descendant deletion not recorded")
			}
			if err := g.Close(); err != nil {
				t.Fatal("import recovery", err)
			}
			after, afterControl = kernelJournalState(t, root)
			if !bytes.Equal(before, after) || control != afterControl || objectLabel(t, root) != label || p.grants != 0 {
				t.Fatal("import cleanup changed workspace security")
			}
			sid := unsafe.Slice((*byte)(unsafe.Pointer(p.sid)), p.sid.Len())
			for _, path := range []string{active, moved} {
				if path == "" {
					continue
				}
				if err := filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					acl, _ := kernelJournalState(t, path)
					if bytes.Contains(acl, sid) {
						t.Errorf("imported object retains profile ACE")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "file" {
				after, afterControl = kernelJournalState(t, active)
				if !bytes.Equal(original, after) || originalControl != afterControl {
					t.Fatal("imported file lost unrelated security")
				}
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			_ = j.Close()
			j, err = openBoundGrantJournal(ctx, path, false)
			if err != nil || !j.state.complete || len(j.state.created) != expected || len(recoveryJournalInfo(j).Imports) != 1 {
				t.Fatal("import completion replay", err)
			}
		})
	}
}

func seedImportProfileACEs(t *testing.T, g *workspaceGrant, path string) {
	t.Helper()
	tree, err := openImportTree(context.Background(), path, g.maxObjects)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	var template []byte
	for _, record := range g.profile.journal.records {
		if record.Grant != nil && record.Grant.Object == g.profile.journal.state.root {
			template = record.Grant.ACE
			break
		}
	}
	if len(template) == 0 {
		t.Fatal("missing workspace ACE fixture")
	}
	for _, object := range tree.objects {
		h, err := openIdentityObject(tree.ancestors[0], object)
		if err != nil {
			t.Fatal(err)
		}
		ace := bytes.Clone(template)
		ace[1] = 0
		if object.directory {
			ace[1] = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		}
		err = setObjectGrant(h, ace, true)
		windows.CloseHandle(h)
		if err != nil {
			t.Fatal(err)
		}
		setMediumLabel(t, filepath.Join(path, object.relative))
	}
}

func Test_WindowsAppContainerWorkspaceImportHelper(t *testing.T) {
	if os.Getenv("XMESH_IMPORT_HELPER") != "1" {
		t.Skip("AppContainer import helper")
	}
	args := os.Args[len(os.Args)-3:]
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil || container == 0 {
		t.Fatal("import helper token", err)
	}
	if err := os.WriteFile(filepath.Join(args[0], "late.txt"), []byte("imported"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[2], []byte("imported"), 0600); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerWorkspaceImportGuards(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(base, "state")
	privateJournalDirectory(t, state)
	j, err := openBoundGrantJournal(ctx, filepath.Join(state, "guards.journal"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	p, err := newProfile(j)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	g, err := p.grantWorkspace(ctx, root, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := g.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := beginWorkspaceChangeTracking(ctx, j, p.sid, 64); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, "file.txt")
	if err := os.WriteFile(file, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	link := filepath.Join(base, "hardlink.txt")
	for _, test := range []struct{ name, path string }{{"workspace", root}, {"journal", state}, {"ancestor", base}, {"reparse", alias}, {"hardlink", file}} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "reparse" {
				if err := os.Symlink(source, alias); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(alias)
			}
			if test.name == "hardlink" {
				if err := os.Link(file, link); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(link)
			}
			count := len(j.records)
			credentials := len(g.credentials)
			before, control := kernelJournalState(t, root)
			if err := g.admitImport(ctx, test.path, maxWorkspaceChangeEvents); err == nil {
				t.Fatal("unsafe import admitted")
			}
			after, afterControl := kernelJournalState(t, root)
			if len(j.records) != count || len(j.state.created) != 0 || len(g.credentials) != credentials || !bytes.Equal(before, after) || control != afterControl {
				t.Fatal("rejected import changed ownership or security")
			}
		})
	}
}
