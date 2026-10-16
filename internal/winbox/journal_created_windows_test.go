package winbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerWorkspaceCreatedPreflight(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"union-limit", "unavailable-observed-id", "failed-append", "execution-pending"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(base, "state", "created.journal")
			directory, err := openJournalDirectory(ctx, filepath.Dir(path), true)
			if err != nil {
				t.Fatal(err)
			}
			if err := directory.Close(); err != nil {
				t.Fatal(err)
			}
			j, err := openBoundGrantJournal(ctx, path, true)
			if err != nil {
				t.Fatal(err)
			}
			p, err := newProfile(j)
			if err != nil {
				j.Close()
				t.Fatal(err)
			}
			g, err := p.grantWorkspace(ctx, root, 64)
			if err != nil {
				p.Close()
				j.Close()
				t.Fatal(err)
			}
			var exclusive windows.Handle
			var execution uint64
			defer func() {
				if exclusive != 0 {
					windows.CloseHandle(exclusive)
				}
				if j.blocked != nil {
					_ = j.Close()
					j, err = openBoundGrantJournal(ctx, path, false)
					if err != nil {
						t.Error("reopen failed-write journal", err)
						return
					}
					p.journal = j
				}
				if execution != 0 && j.state.executions[execution] {
					// This intent fixture never admitted a process.
					if _, err := j.append(journalRecord{Kind: "execution-reaped", ExecutionSequence: execution}); err != nil {
						t.Error(err)
					}
				}
				if err := g.Close(); err != nil {
					t.Error("workspace cleanup", err)
				}
				if err := p.Close(); err != nil {
					t.Error("profile cleanup", err)
				}
				if err := j.Close(); err != nil {
					t.Error(err)
				}
			}()
			dir := filepath.Join(root, "created")
			file := filepath.Join(dir, "item.txt")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("created"), 0600); err != nil {
				t.Fatal(err)
			}
			limit := 64
			if stage == "execution-pending" {
				execution, err = j.append(journalExecution())
				if err != nil {
					t.Fatal(err)
				}
			}
			if stage != "failed-append" {
				if err := checkpointJournalWorkspace(j, p.sid, 64); err != nil {
					t.Fatal(err)
				}
				if len(j.state.created) != 2 {
					t.Fatal("checkpoint lost created identities")
				}
			}
			if stage == "union-limit" || stage == "unavailable-observed-id" {
				moved := filepath.Join(base, "moved-created")
				if err := os.Rename(dir, moved); err != nil {
					t.Fatal(err)
				}
				dir, file = moved, filepath.Join(moved, "item.txt")
				if stage == "union-limit" {
					if err := os.Mkdir(filepath.Join(root, "created"), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "created", "replacement.txt"), []byte("replacement"), 0600); err != nil {
						t.Fatal(err)
					}
					// Current tree and recorded identities each fit; their union does not.
					limit = 4
				} else {
					name, err := windows.UTF16PtrFromString(file)
					if err != nil {
						t.Fatal(err)
					}
					exclusive, err = windows.CreateFile(name, windows.READ_CONTROL, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			type descriptor struct {
				acl     []byte
				control windows.SECURITY_DESCRIPTOR_CONTROL
				label   string
			}
			before := make(map[string]descriptor)
			// The exclusive file is covered by its parent and retained journal ID.
			paths := []string{root, dir}
			if exclusive == 0 {
				paths = append(paths, file)
			}
			for _, objectPath := range paths {
				acl, control := kernelJournalState(t, objectPath)
				before[objectPath] = descriptor{acl, control, objectLabel(t, objectPath)}
			}
			count, created := len(j.records), len(j.state.created)
			if stage == "failed-append" {
				if err := j.file.Close(); err != nil {
					t.Fatal(err)
				}
				j.file, err = os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := releaseJournalWorkspace(j, p.sid, limit); err == nil {
				t.Fatal("incomplete reclamation accepted", stage)
			}
			if len(j.records) != count || len(j.state.created) != created || j.state.workspaceReleased {
				t.Fatal("failed reclamation advanced durable obligations")
			}
			if stage == "failed-append" && j.blocked == nil {
				t.Fatal("fixture did not reach the created-object write")
			}
			for objectPath, want := range before {
				acl, control := kernelJournalState(t, objectPath)
				if !bytes.Equal(acl, want.acl) || control != want.control || objectLabel(t, objectPath) != want.label {
					t.Fatal("rejected reclamation changed descriptor", objectPath)
				}
			}
		})
	}
}
