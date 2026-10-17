package winbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func profileACEPresent(t *testing.T, sid []byte, path string) bool {
	t.Helper()
	found := false
	if err := filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		acl, _ := kernelJournalState(t, path)
		found = found || bytes.Contains(acl, sid)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return found
}

// An uncoordinated rename-in has no pre-move census. Tracking must classify it,
// durably own the identity, adopt the remaining present subtree during cleanup,
// and reclaim every workspace-format ACE it carries.
func Test_WindowsAppContainerWorkspaceUnknownImport(t *testing.T) {
	skipUnlessChangeJournal(t)
	for _, stage := range []string{"directory", "file", "replay", "deleted", "created-inside"} {
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
			path := filepath.Join(state, "unknown.journal")
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
					t.Error("unknown import profile cleanup", err)
				}
			}()
			g, err := p.grantWorkspace(ctx, root, 64)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := g.Close(); err != nil {
					t.Error("unknown import workspace cleanup", err)
				}
			}()
			if err := beginWorkspaceChangeTracking(ctx, j, p.sid, 64); err != nil {
				t.Fatal(err)
			}
			// Workspace-format ACEs make reclamation observable. No admission runs.
			seedImportProfileACEs(t, g, source)
			active := filepath.Join(root, "imported")
			if err := os.Rename(source, active); err != nil {
				t.Fatal(err)
			}
			// Event tracking sees only the rename-in; cleanup adopts the present
			// remainder of the subtree. A creation inside it is a proved lifetime.
			wantUnknown, wantCreated := 4, 4
			switch stage {
			case "file":
				wantUnknown, wantCreated = 1, 1
			case "created-inside":
				wantCreated = 5
			}
			if stage == "created-inside" {
				// A creation beneath the imported directory is a proved lifetime.
				if err := os.WriteFile(filepath.Join(active, "later.txt"), []byte("later"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "deleted" {
				if err := os.RemoveAll(active); err != nil {
					t.Fatal(err)
				}
			}
			if err := g.checkpointChanges(ctx, maxWorkspaceChangeEvents); !errors.Is(err, errWorkspaceUnknownImport) {
				t.Fatal("uncoordinated import was not classified", err)
			}
			if len(j.state.unknown) == 0 {
				t.Fatal("unknown import was not recorded")
			}
			if stage == "deleted" && len(j.state.deleted) == 0 {
				t.Fatal("imported deletion receipt missing")
			}
			if stage == "directory" || stage == "created-inside" || stage == "replay" {
				if len(j.state.unknown) != 1 {
					t.Fatal("event tracking over-claimed a subtree", len(j.state.unknown))
				}
			}
			if stage == "replay" {
				_ = j.Close()
				j, err = openBoundGrantJournal(ctx, path, false)
				if err != nil {
					t.Fatal(err)
				}
				p.journal = j
				// The mark is durable: a replayed checkpoint cannot declare calm.
				if err := g.checkpointChanges(ctx, maxWorkspaceChangeEvents); !errors.Is(err, errWorkspaceUnknownImport) {
					t.Fatal("reopened journal lost the unknown-import mark", err)
				}
			}
			// A coordinated import cannot proceed on top of an unknown one.
			other := filepath.Join(base, "other")
			if err := os.Mkdir(other, 0700); err != nil {
				t.Fatal(err)
			}
			created := len(j.state.created)
			if err := g.admitImport(ctx, other, maxWorkspaceChangeEvents); !errors.Is(err, errWorkspaceUnknownImport) || len(j.state.created) != created {
				t.Fatal("import admitted after an unknown import", err)
			}
			if err := g.Close(); err != nil {
				t.Fatal("unknown import recovery", err)
			}
			if stage != "deleted" && (len(j.state.unknown) != wantUnknown || len(j.state.created) != wantCreated) {
				t.Fatal("cleanup census did not adopt the imported subtree", len(j.state.unknown), len(j.state.created))
			}
			wantUnknown = len(j.state.unknown)
			after, afterControl := kernelJournalState(t, root)
			if !bytes.Equal(before, after) || control != afterControl || objectLabel(t, root) != label || p.grants != 0 {
				t.Fatal("unknown import cleanup changed workspace security")
			}
			sid := unsafe.Slice((*byte)(unsafe.Pointer(p.sid)), p.sid.Len())
			if _, err := os.Stat(active); err == nil && profileACEPresent(t, sid, active) {
				t.Fatal("imported object retains profile ACE")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			_ = j.Close()
			j, err = openBoundGrantJournal(ctx, path, false)
			if err != nil || !j.state.complete || len(j.state.unknown) != wantUnknown {
				t.Fatal("unknown import completion replay", err)
			}
		})
	}
}

// Through the lifecycle owner an uncoordinated import stops execution, cleanup
// still reaches durable completion, and the caller sees the typed condition.
func Test_WindowsAppContainerWorkspaceExecutionUnknownImport(t *testing.T) {
	skipUnlessChangeJournal(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	options := workspaceExecutionTestOptions(t)
	before, control := kernelJournalState(t, options.Workspace)
	label := objectLabel(t, options.Workspace)
	source := filepath.Join(filepath.Dir(options.Workspace), "source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "deep.txt"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	guardCloses := 0
	e, err := newWorkspaceExecution(ctx, options, func(context.Context, workspaceExecutionOptions) (io.Closer, error) {
		return recoveryTestGuard(func() error { guardCloses++; return nil }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.closeContext(context.Background(), 1) }()
	journal, monitor := e.journal, e.monitor
	sid, err := windows.StringToSid(journal.records[0].SID)
	if err != nil {
		t.Fatal(err)
	}
	seedImportProfileACEs(t, e.workspace, source)
	active := filepath.Join(options.Workspace, "imported")
	if err := os.Rename(source, active); err != nil {
		t.Fatal(err)
	}
	select {
	case <-monitor.done:
	case <-time.After(10 * time.Second):
		t.Fatal("monitor did not observe the uncoordinated import")
	}
	if !errors.Is(monitor.err, errWorkspaceUnknownImport) {
		t.Fatal("monitor fault is not the typed unknown import", monitor.err)
	}
	if _, err := e.start(ctx, []string{"cmd.exe"}, "", nil, nil, 0); err == nil {
		t.Fatal("execution admitted work after an unknown import")
	}
	err = e.closeContext(ctx, 3)
	if !errors.Is(err, errWorkspaceUnknownImport) || !e.closed || guardCloses != 1 || !journal.state.complete {
		t.Fatal("lifecycle close after unknown import", err, e.closed, guardCloses)
	}
	if len(journal.state.unknown) != 3 {
		t.Fatal("lifecycle cleanup did not own the imported subtree", len(journal.state.unknown))
	}
	after, afterControl := kernelJournalState(t, options.Workspace)
	if !bytes.Equal(before, after) || control != afterControl || label != objectLabel(t, options.Workspace) {
		t.Fatal("workspace descriptor/control/label changed after unknown import")
	}
	if profileACEPresent(t, unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len()), active) {
		t.Fatal("imported object retains profile ACE")
	}
	if err := e.closeContext(ctx, 3); err != nil || guardCloses != 1 {
		t.Fatal("close retry", err, guardCloses)
	}
}
