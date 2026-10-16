package winbox

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerWorkspaceChangeGuards(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"history-identity", "history-future", "event-limit", "identity-limit", "cancel", "append-failure", "source-binding"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(base, "state")
			directory, err := openJournalDirectory(ctx, state, true)
			if err != nil {
				t.Fatal(err)
			}
			directory.Close()
			path := filepath.Join(state, "changes.journal")
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
			defer func() {
				if j.blocked != nil {
					j.Close()
					j, err = openBoundGrantJournal(ctx, path, false)
					if err != nil {
						t.Error("reopen checkpoint journal", err)
						return
					}
					p.journal = j
				}
				if err := g.Close(); err != nil {
					t.Error("tracked workspace cleanup", err)
				}
				if err := p.Close(); err != nil {
					t.Error("tracked profile cleanup", err)
				}
				j.Close()
			}()
			count := len(j.records)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := beginWorkspaceChangeTracking(cancelled, j, p.sid, 64); !errors.Is(err, context.Canceled) || len(j.records) != count || j.state.changeCursor != nil {
				t.Fatal("cancelled tracking admission changed journal", err)
			}
			if err := beginWorkspaceChangeTracking(ctx, j, p.sid, 64); err != nil {
				t.Fatal(err)
			}
			start := *j.state.changeCursor
			if err := beginWorkspaceChangeTracking(ctx, j, p.sid, 64); err == nil {
				t.Fatal("tracking started twice")
			}
			child := filepath.Join(root, "new-dir")
			file := filepath.Join(child, "new.txt")
			if err := os.Mkdir(child, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			// Namespace moves occur before any checkpoint/scan can observe the new IDs.
			moved := root + ".moved"
			if err := os.Rename(child, moved); err != nil {
				t.Fatal(err)
			}
			file = filepath.Join(moved, "new.txt")
			count = len(j.records)
			var before [3][]byte
			var controls [3]windows.SECURITY_DESCRIPTOR_CONTROL
			paths := []string{root, moved, file}
			for i, path := range paths {
				before[i], controls[i] = kernelJournalState(t, path)
			}
			maxObjects, maxEvents := 64, maxWorkspaceChangeEvents
			callCtx := ctx
			var restoreCursor func()
			switch stage {
			case "history-identity", "history-future":
				altered := start
				if stage == "history-identity" {
					altered.JournalID ^= 1
				} else {
					altered.Next = math.MaxInt64
				}
				record := j.records[len(j.records)-1].ChangeCursor
				old := *record
				*record = altered
				*j.state.changeCursor = altered
				restoreCursor = func() { *record = old; *j.state.changeCursor = start }
			case "event-limit":
				maxEvents = 1
			case "identity-limit":
				maxObjects = 2
			case "cancel":
				callCtx = cancelled
			case "append-failure":
				if err := j.file.Close(); err != nil {
					t.Fatal(err)
				}
				j.file, err = os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
			case "source-binding":
				if source, err := openWorkspaceChangeSource(g.tree.ancestors[0], j.records[1].Workspace.Root.Volume+1); err == nil {
					source.Close()
					t.Fatal("foreign volume source accepted")
				}
				if source, err := openWorkspaceChangeSource(g.tree.objects[0].handle, j.records[1].Workspace.Root.Volume); err == nil {
					source.Close()
					t.Fatal("workspace directory accepted as raw volume root")
				}
				callCtx = cancelled
			}
			err = checkpointWorkspaceChanges(callCtx, j, p.sid, maxObjects, maxEvents)
			if restoreCursor != nil {
				restoreCursor()
			}
			if err == nil {
				t.Fatal("invalid change checkpoint accepted", stage)
			}
			if (stage == "history-identity" || stage == "history-future") && !errors.Is(err, errWorkspaceChangesLost) {
				t.Fatal("history continuity failure lost error", err)
			}
			if stage == "append-failure" && j.blocked == nil {
				t.Fatal("fixture did not reach checkpoint write", err)
			}
			if count != len(j.records) || *j.state.changeCursor != start || len(j.state.created) != 0 {
				t.Fatal("failed checkpoint changed ownership/cursor")
			}
			for i, path := range paths {
				acl, control := kernelJournalState(t, path)
				if !bytes.Equal(acl, before[i]) || control != controls[i] {
					t.Fatal("failed checkpoint changed descriptor", path)
				}
			}
		})
	}
}
