package winbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerWorkspaceDeletionRecovery(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"catchup", "checkpointed", "receipt-only", "initial", "pending-close", "write-failure"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			initial := filepath.Join(root, "initial.txt")
			if err := os.WriteFile(initial, []byte("initial"), 0600); err != nil {
				t.Fatal(err)
			}
			before, control := kernelJournalState(t, root)
			state := filepath.Join(base, "state")
			directory, err := openJournalDirectory(ctx, state, true)
			if err != nil {
				t.Fatal(err)
			}
			directory.Close()
			path := filepath.Join(state, "deletion.journal")
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^Test_WindowsAppContainerWorkspaceDeletionCrashHelper$")
			cmd.Env = append(os.Environ(), "XMESH_DELETION_JOURNAL="+path, "XMESH_DELETION_ROOT="+root, "XMESH_DELETION_STAGE="+stage)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			ready := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				var lines []string
				for scanner.Scan() {
					if scanner.Text() == "ready" {
						ready <- "ready\n"
						return
					}
					lines = append(lines, scanner.Text())
				}
				ready <- strings.Join(lines, "\n")
			}()
			select {
			case line := <-ready:
				if line != "ready\n" && line != "ready\r\n" {
					t.Fatal("deletion helper readiness", line)
				}
			case <-ctx.Done():
				t.Fatal("deletion helper deadline")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			j, err := openBoundGrantJournal(ctx, path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if stage == "receipt-only" && len(j.state.deleted) != 2 {
				t.Fatal("durable deletion prefix lost receipts")
			}
			sid, _, err := journalRecoveryIdentity(j)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.FreeSid(sid)
			// The sandbox has exited; killing its host closes pinned initial-ID
			// handles. External filesystem writers are quiescent in this fixture.
			if err := recoverWorkspaceJournal(j, 32); err != nil {
				t.Fatal("deletion recovery", err)
			}
			expected := 2
			if stage == "initial" || stage == "pending-close" {
				expected++
			}
			if len(j.state.deleted) != expected || !j.state.complete || len(j.state.pending) != 0 {
				t.Fatal("deletion completion", len(j.state.deleted), j.state.complete, j.state.pending)
			}
			manifest := recoveryJournalInfo(j)
			if len(manifest.DeletedObjects) != expected {
				t.Fatal("deletion coordinator manifest")
			}
			manifest.DeletedObjects[0].USN++
			manifest.DeletedObjects[0].Object.ID[0] ^= 1
			if err := validateWorkspaceJournalOwnership(j, sid); err != nil {
				t.Fatal("coordinator changed journal deletion ownership", err)
			}
			if err := recoverWorkspaceJournal(j, 32); err != nil {
				t.Fatal("repeat deletion recovery", err)
			}
			after, afterControl := kernelJournalState(t, root)
			if !bytes.Equal(before, after) || control != afterControl {
				t.Fatal("deletion recovery changed root descriptor")
			}
			sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())
			if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				acl, _ := kernelJournalState(t, path)
				if bytes.Contains(acl, sidBytes) {
					return fmt.Errorf("deleted workspace profile ACE remains")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = openBoundGrantJournal(ctx, path, false)
			if err != nil || !j.state.complete || len(j.state.deleted) != expected {
				t.Fatal("completed deletion replay", err)
			}
			defer j.Close()
		})
	}
}

func Test_WindowsAppContainerWorkspaceDeletionCrashHelper(t *testing.T) {
	path := os.Getenv("XMESH_DELETION_JOURNAL")
	if path == "" {
		t.Skip("deletion crash helper")
	}
	ctx := context.Background()
	j, err := openBoundGrantJournal(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newProfile(j)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("XMESH_DELETION_ROOT")
	g, err := p.grantWorkspace(ctx, root, 32)
	if err != nil {
		t.Fatal(err)
	}
	if err := beginWorkspaceChangeTracking(ctx, j, p.sid, 32); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(p.Directory(), "deletion-helper.exe")
	body, err := sandboxHelperImage()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, body, 0700); err != nil {
		t.Fatal(err)
	}
	stage := os.Getenv("XMESH_DELETION_STAGE")
	body = runAppContainerHelper(t, p, helper, "Test_WindowsAppContainerWorkspaceDeletionSandboxHelper", "XMESH_DELETION_SANDBOX", root, stage)
	if string(body) != "deleted" {
		t.Fatal("sandbox deletion proof", string(body))
	}
	// Reuse the deleted pathname with a distinct live object identity.
	if err := os.WriteFile(filepath.Join(root, "new-dir"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := confirmWorkspaceDeletion(g.tree.ancestors[0], j.records[1].Workspace.Root); err == nil || !strings.Contains(err.Error(), "still has a live identity") {
		t.Fatal("live root reference probe", err)
	}
	if stage == "pending-close" {
		name, _ := windows.UTF16PtrFromString(filepath.Join(root, "pending.txt"))
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.CREATE_NEW, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		// Explicit classic disposition retains the named delete-pending object
		// until this handle closes. Go's Remove may instead use POSIX unlink.
		remove := uint32(1)
		if err := windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&remove)), 4); err != nil {
			windows.CloseHandle(h)
			t.Fatal(err)
		}
		var standard [3]uint64
		if err := windows.GetFileInformationByHandleEx(h, windows.FileStandardInfo, (*byte)(unsafe.Pointer(&standard[0])), 24); err != nil || unsafe.Slice((*byte)(unsafe.Pointer(&standard[0])), 24)[20] == 0 {
			windows.CloseHandle(h)
			t.Fatal("classic delete-pending fixture", err)
		}
		count, cursor := len(j.records), *j.state.changeCursor
		if err := checkpointWorkspaceChanges(ctx, j, p.sid, 32, maxWorkspaceChangeEvents); err == nil || len(j.records) != count || *j.state.changeCursor != cursor {
			windows.CloseHandle(h)
			t.Fatal("pending-close deletion advanced ownership", err)
		}
		windows.CloseHandle(h)
	}
	if stage == "write-failure" {
		j.file.Close()
		j.file, err = os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		count, cursor := len(j.records), *j.state.changeCursor
		if err := checkpointWorkspaceChanges(ctx, j, p.sid, 32, maxWorkspaceChangeEvents); err == nil || j.blocked == nil || len(j.records) != count || len(j.state.deleted) != 0 || *j.state.changeCursor != cursor {
			t.Fatal("failed deletion checkpoint changed ownership", err)
		}
		j.Close()
		j, err = openBoundGrantJournal(ctx, path, false)
		if err != nil {
			t.Fatal(err)
		}
		p.journal = j
	}
	if stage != "catchup" && stage != "initial" {
		if err := checkpointWorkspaceChanges(ctx, j, p.sid, 32, maxWorkspaceChangeEvents); err != nil {
			t.Fatal(err)
		}
	}
	if stage == "receipt-only" {
		last := j.records[len(j.records)-1]
		if last.Kind != "workspace-tracked" || len(j.state.deleted) != 2 {
			t.Fatal("receipt prefix fixture")
		}
		// Retain the complete durable prefix ending with workspace-deleted,
		// simulating host loss before the following cursor frame was written.
		frame, err := json.Marshal(last)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.file.Truncate(j.bytes - int64(len(frame)+8)); err != nil {
			t.Fatal(err)
		}
		if err := j.file.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("ready")
	time.Sleep(30 * time.Second)
	t.Fatal("deletion helper was not terminated")
}

func Test_WindowsAppContainerWorkspaceDeletionSandboxHelper(t *testing.T) {
	if os.Getenv("XMESH_DELETION_SANDBOX") != "1" {
		t.Skip("AppContainer deletion helper")
	}
	args := os.Args[len(os.Args)-3:]
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil || container == 0 {
		t.Fatal("deletion helper token", err)
	}
	directory := filepath.Join(args[0], "new-dir")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "new.txt")
	if err := os.WriteFile(file, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if args[1] == "initial-classic" {
		name, _ := windows.UTF16PtrFromString(filepath.Join(args[0], "initial.txt"))
		h, err := windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		remove := uint32(1)
		err = windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&remove)), 4)
		windows.CloseHandle(h)
		if err != nil {
			t.Fatal(err)
		}
	}
	if args[1] == "initial" {
		if err := os.Remove(filepath.Join(args[0], "initial.txt")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(args[2], []byte("deleted"), 0600); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerWorkspaceLiveDeletionClose(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"normal", "external-handle", "append-failure"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			initial := filepath.Join(root, "initial.txt")
			keep := filepath.Join(root, "keep.txt")
			for _, path := range []string{initial, keep} {
				if err := os.WriteFile(path, []byte("initial"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			setMediumLabel(t, root)
			setMediumLabel(t, keep)
			rootBefore, rootControl := kernelJournalState(t, root)
			keepBefore, keepControl := kernelJournalState(t, keep)
			rootLabel, keepLabel := objectLabel(t, root), objectLabel(t, keep)
			state := filepath.Join(base, "state")
			privateJournalDirectory(t, state)
			path := filepath.Join(state, "live-deletion.journal")
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
					t.Error("live deletion profile cleanup", err)
				}
			}()
			g, err := p.grantWorkspace(ctx, root, 32)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if j.blocked != nil {
					_ = j.Close()
					j, err = openBoundGrantJournal(ctx, path, false)
					if err != nil {
						t.Error("live deletion journal reopen", err)
						return
					}
					p.journal = j
				}
				if err := g.Close(); err != nil {
					t.Error("live deletion grant cleanup", err)
				}
			}()
			if err := beginWorkspaceChangeTracking(ctx, j, p.sid, 32); err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(p.Directory(), "live-deletion-helper.exe")
			body, err := sandboxHelperImage()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(helper, body, 0700); err != nil {
				t.Fatal(err)
			}
			var external windows.Handle
			if stage == "external-handle" {
				name, _ := windows.UTF16PtrFromString(initial)
				external, err = windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if external != 0 {
						windows.CloseHandle(external)
					}
				}()
			}
			body = runAppContainerHelper(t, p, helper, "Test_WindowsAppContainerWorkspaceDeletionSandboxHelper", "XMESH_DELETION_SANDBOX", root, "initial-classic")
			if string(body) != "deleted" {
				t.Fatal("live deletion sandbox proof", string(body))
			}
			// Confirm that our retained initial handle is the final-close blocker.
			var pending *objectGrant
			for _, grant := range g.selected.grants {
				var info [3]uint64
				if err := windows.GetFileInformationByHandleEx(grant.handle, windows.FileStandardInfo, (*byte)(unsafe.Pointer(&info[0])), 24); err != nil {
					t.Fatal(err)
				}
				if unsafe.Slice((*byte)(unsafe.Pointer(&info[0])), 24)[20] != 0 {
					pending = grant
				}
			}
			if pending == nil {
				t.Fatal("initial object did not remain delete-pending")
			}
			// A fresh unconfirmed owner, even with no admitted Jobs, keeps every
			// identity handle and durable grant until its exit receipt is written.
			owner, err := newExecutionProcesses(p, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if !owner.closed {
					if err := owner.reap(ctx, 0); err != nil {
						t.Error("live deletion owner reaping", err)
						return
					}
					if err := owner.Close(); err != nil {
						t.Error("live deletion owner cleanup", err)
					}
				}
			}()
			count, references := len(j.records), p.grants
			identity, security := pending.handle, pending.aclHandle
			if err := g.Close(); !errors.Is(err, errJournalProcessesPending) || len(j.records) != count || pending.handle != identity || pending.aclHandle != security || p.grants != references || p.workspaceClosing {
				t.Fatal("unconfirmed owner released initial handles", err)
			}
			if err := owner.reap(ctx, 0); err != nil {
				t.Fatal(err)
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			admitted, admittedControl := kernelJournalState(t, root)
			if stage == "append-failure" {
				if err := j.file.Close(); err != nil {
					t.Fatal(err)
				}
				j.file, err = os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			if stage != "normal" {
				if err := g.Close(); err == nil || j.state.workspaceReleased || g.tree == nil || p.grants != references || pending.handle != 0 || pending.aclHandle != 0 {
					t.Fatal("failed live close lost durable ownership", err)
				}
				if stage == "append-failure" && j.blocked == nil {
					t.Fatal("live close did not reach append fault")
				}
				after, control := kernelJournalState(t, root)
				if !bytes.Equal(admitted, after) || control != admittedControl || p.Close() == nil {
					t.Fatal("failed close changed root security or profile ownership")
				}
				if err := os.Rename(root, root+".moved"); err == nil {
					t.Fatal("failed close lost root namespace binding")
				}
				count = len(j.records)
				if next, err := newExecutionProcesses(p, 1); err == nil {
					_ = next.reap(ctx, 0)
					_ = next.Close()
					t.Fatal("failed close reopened process admission")
				}
				if next, err := p.grantObject(keep); err == nil {
					_ = next.Close()
					t.Fatal("failed close reopened grant admission")
				}
				if !p.workspaceClosing || len(j.records) != count || p.grants != references {
					t.Fatal("sealed admission changed journal or profile references")
				}
				if external != 0 {
					windows.CloseHandle(external)
					external = 0
				}
				if j.blocked != nil {
					_ = j.Close()
					j, err = openBoundGrantJournal(ctx, path, false)
					if err != nil {
						t.Fatal(err)
					}
					p.journal = j
				}
			}
			if err := g.Close(); err != nil {
				t.Fatal("live deletion close", err)
			}
			if !j.state.workspaceReleased || len(j.state.pending) != 0 || len(j.state.deleted) != 3 || p.grants != 0 || g.tree != nil || len(g.selected.grants) != 0 {
				t.Fatal("live deletion close retained ownership", len(j.state.deleted), p.grants)
			}
			count = len(j.records)
			if err := g.Close(); err != nil || len(j.records) != count || p.grants != 0 {
				t.Fatal("repeat live close changed references or journal", err)
			}
			rootAfter, rootAfterControl := kernelJournalState(t, root)
			keepAfter, keepAfterControl := kernelJournalState(t, keep)
			if !bytes.Equal(rootBefore, rootAfter) || rootControl != rootAfterControl || !bytes.Equal(keepBefore, keepAfter) || keepControl != keepAfterControl || rootLabel != objectLabel(t, root) || keepLabel != objectLabel(t, keep) {
				t.Fatal("live deletion close changed retained descriptors or labels")
			}
			if _, err := os.Stat(initial); !os.IsNotExist(err) {
				t.Fatal("classic deleted initial object still exists", err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = openBoundGrantJournal(ctx, path, false)
			if err != nil || !j.state.complete || len(j.state.deleted) != 3 || len(j.state.pending) != 0 {
				t.Fatal("live deletion durable replay", err)
			}
		})
	}
}
