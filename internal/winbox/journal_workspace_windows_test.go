package winbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerWorkspaceJournalCrashRecovery(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, stage := range []string{"admitted", "explicit-released", "workspace-released"} {
		t.Run(stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "workspace")
			child := filepath.Join(root, "existing-child")
			file := filepath.Join(child, "existing.txt")
			if err := os.MkdirAll(child, 0700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(filepath.Dir(root), "outside.txt")
			for _, path := range []string{file, outside} {
				if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			type state struct {
				acl     []byte
				control windows.SECURITY_DESCRIPTOR_CONTROL
				label   string
			}
			before := map[string]state{}
			for _, path := range []string{root, child, file, outside} {
				setMediumLabel(t, path)
				acl, control := kernelJournalState(t, path)
				before[path] = state{acl, control, objectLabel(t, path)}
			}
			journalPath := filepath.Join(filepath.Dir(root), "workspace.journal")
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^Test_WindowsAppContainerWorkspaceJournalCrashHelper$")
			cmd.Env = append(os.Environ(), "XMESH_WORKSPACE_JOURNAL="+journalPath, "XMESH_WORKSPACE_PATH="+root, "XMESH_WORKSPACE_STAGE="+stage)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			ready := make(chan string, 1)
			go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
			select {
			case line := <-ready:
				if line != "ready\n" && line != "ready\r\n" {
					t.Fatal("journal workspace helper readiness", line)
				}
			case <-ctx.Done():
				t.Fatal("journal workspace helper deadline")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			waited = true
			j, err := openGrantJournal(journalPath, false)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			// The helper reaps its sandbox before readiness. Its coordinated
			// namespace moves only initial IDs outside the root; all new objects
			// remain within the recovery census.
			defer func() {
				if err := recoverWorkspaceJournal(j, 64); err != nil {
					t.Error("workspace recovery cleanup", err)
				}
			}()
			created := filepath.Join(root, "new-tree", "level-1", "level-2", "created.txt")
			createdLabel := objectLabel(t, created)
			if stage == "admitted" {
				admitted, control := kernelJournalState(t, root)
				count := len(j.records)
				for _, change := range []string{"limit", "root", "volume", "ace"} {
					w := j.records[1].Workspace
					rootID, first := w.Root, w.Objects[0]
					grant := j.records[2].Grant
					object, ace := grant.Object, bytes.Clone(grant.ACE)
					limit := 64
					switch change {
					case "limit":
						limit = 2
					case "root":
						w.Root.ID[0] ^= 1
						w.Objects[0] = w.Root
						grant.Object = w.Root
					case "volume":
						grant.Object.Volume++
					case "ace":
						grant.ACE[4] ^= 1
					}
					err := recoverWorkspaceJournal(j, limit)
					w.Root, w.Objects[0] = rootID, first
					grant.Object, grant.ACE = object, ace
					if err == nil {
						t.Fatal("invalid workspace recovery accepted", change)
					}
					after, afterControl := kernelJournalState(t, root)
					if !bytes.Equal(admitted, after) || control != afterControl || len(j.records) != count {
						t.Fatal("failed workspace preflight changed descriptors or journal", change)
					}
				}
				if err := os.Rename(root, root+".moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				if err := recoverWorkspaceJournal(j, 64); err == nil {
					t.Fatal("replaced workspace root accepted")
				}
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(root+".moved", root); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "unexpected-link.txt")
				if err := os.Link(filepath.Join(root+".moved-child", "existing.txt"), link); err != nil {
					t.Fatal(err)
				}
				if err := recoverWorkspaceJournal(j, 64); err == nil {
					t.Fatal("incomplete hard-link census accepted")
				}
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				after, afterControl := kernelJournalState(t, root)
				if len(j.records) != count || !bytes.Equal(admitted, after) || control != afterControl {
					t.Fatal("rejected namespace census changed root descriptor or journal")
				}
			}
			// The root-replacement preflight above needs a movable namespace.
			// This profile retains ordinary pathname handles on its selected file.
			other, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			otherGrant, err := other.grantObject(created)
			if err != nil {
				t.Fatal(err)
			}
			defer otherGrant.Close()
			if err := recoverWorkspaceJournal(j, 64); err != nil {
				t.Fatal(err)
			}
			if err := recoverWorkspaceJournal(j, 64); err != nil {
				t.Fatal("repeat workspace recovery", err)
			}
			for path, original := range before {
				actual := path
				if path == child {
					actual = root + ".moved-child"
				} else if path == file {
					actual = filepath.Join(root+".moved-child", "existing.txt")
				}
				acl, control := kernelJournalState(t, actual)
				if !bytes.Equal(acl, original.acl) || control != original.control || objectLabel(t, actual) != original.label {
					t.Fatal("workspace recovery did not restore original descriptor/label", filepath.Base(actual))
				}
			}
			sid, err := windows.StringToSid(j.records[0].SID)
			if err != nil {
				t.Fatal(err)
			}
			sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())
			if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				acl, _ := kernelJournalState(t, path)
				if bytes.Contains(acl, sidBytes) {
					return fmt.Errorf("profile ACE remains on %s", path)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			acl, _ := kernelJournalState(t, created)
			if !bytes.Contains(acl, otherGrant.ace) || objectLabel(t, created) != createdLabel {
				t.Fatal("workspace recovery changed another profile or the created-object label")
			}
			if !j.state.workspaceReleased || !j.state.complete || len(j.state.pending) != 0 {
				t.Fatal("workspace recovery completion was not durably recorded")
			}
		})
	}
}

func Test_WindowsAppContainerWorkspaceJournalCrashHelper(t *testing.T) {
	journalPath := os.Getenv("XMESH_WORKSPACE_JOURNAL")
	if journalPath == "" {
		t.Skip("journal workspace forced-exit helper")
	}
	j, err := openGrantJournal(journalPath, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newProfile(j)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("XMESH_WORKSPACE_PATH")
	g, err := p.grantWorkspace(context.Background(), root, 64)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(p.Directory(), "journal-workspace-helper.exe")
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
	body = runAppContainerHelper(t, p, helper, "Test_WindowsAppContainerWorkspaceHelper", "XMESH_WINBOX_WORKSPACE_HELPER", root, filepath.Join(filepath.Dir(root), "outside.txt"))
	var proof workspaceProof
	if err := json.Unmarshal(body, &proof); err != nil {
		t.Fatal(err)
	}
	if !proof.AppContainer || !proof.ExistingRead || !proof.ExistingWrite || !proof.NestedRead || !proof.NestedWrite || proof.OutsideRead || proof.OutsideWrite {
		t.Fatalf("journal workspace sandbox proof: %+v", proof)
	}
	if err := os.Rename(filepath.Join(root, "existing-child"), root+".moved-child"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "existing-child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing-child", "replacement.txt"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if p.Close() == nil {
		t.Fatal("profile closed before workspace cleanup")
	}
	switch os.Getenv("XMESH_WORKSPACE_STAGE") {
	case "explicit-released":
		if err := g.selected.Close(); err != nil {
			t.Fatal(err)
		}
	case "workspace-released":
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
		if !j.state.workspaceReleased || p.grants != 0 {
			t.Fatal("live workspace cleanup retained references or lost acknowledgement")
		}
	}
	fmt.Println("ready")
	time.Sleep(30 * time.Second)
	t.Fatal("journal workspace helper was not terminated")
}
