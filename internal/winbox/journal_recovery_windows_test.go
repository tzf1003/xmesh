package winbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func kernelJournalState(t *testing.T, path string) ([]byte, windows.SECURITY_DESCRIPTOR_CONTROL) {
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
	return body, control
}

func Test_WindowsAppContainerSelectedJournalCrashRecovery(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, kind := range []string{"file", "directory"} {
		for _, stage := range []string{"applied", "removed", "released"} {
			t.Run(kind+"-"+stage, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "objects")
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, "selected")
				if kind == "directory" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				setMediumLabel(t, path)
				before, control := kernelJournalState(t, path)
				label := objectLabel(t, path)
				state := filepath.Join(filepath.Dir(root), "recovery-state")
				privateJournalDirectory(t, state)
				journalPath := filepath.Join(state, "recovery.journal")
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^Test_WindowsAppContainerSelectedJournalCrashHelper$")
				cmd.Env = append(os.Environ(), "XMESH_SELECTED_JOURNAL="+journalPath, "XMESH_SELECTED_PATH="+path, "XMESH_SELECTED_STAGE="+stage)
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
						t.Fatal("journaled grant helper readiness", line)
					}
				case <-ctx.Done():
					t.Fatal("journaled grant helper deadline")
				}
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
				waited = true
				j, err := openBoundGrantJournal(context.Background(), journalPath, false)
				if err != nil {
					t.Fatal(err)
				}
				defer j.Close()
				guard, err := openWorkspaceTree(context.Background(), root, 16)
				if err != nil {
					t.Fatal(err)
				}
				defer guard.Close()
				// The child launched no sandbox processes. After Kill/Wait the
				// recovery caller has satisfied the process-reaping precondition.
				defer func() {
					if j == nil || j.file == nil {
						var err error
						j, err = openBoundGrantJournal(context.Background(), journalPath, false)
						if err != nil {
							t.Error("reopen recovery cleanup", err)
							return
						}
					}
					if err := recoverSelectedJournal(j, guard.ancestors[0]); err != nil {
						t.Error("recovery cleanup", err)
					}
					j.Close()
				}()
				replacement, replacementControl := kernelJournalState(t, path)
				if stage == "applied" {
					admitted, admittedControl := kernelJournalState(t, path+".moved")
					for _, change := range []string{"profile", "volume", "ace"} {
						profileSID := j.records[0].SID
						volume := j.records[1].Grant.Object.Volume
						ace := bytes.Clone(j.records[1].Grant.ACE)
						switch change {
						case "profile":
							j.records[0].SID = "S-1-15-2-1-2-3-4-5-6-7"
						case "volume":
							j.records[1].Grant.Object.Volume++
						case "ace":
							j.records[1].Grant.ACE[4] ^= 1
						}
						err := recoverSelectedJournal(j, guard.ancestors[0])
						j.records[0].SID = profileSID
						j.records[1].Grant.Object.Volume = volume
						j.records[1].Grant.ACE = ace
						if err == nil {
							t.Fatal("recovery accepted invalid ownership manifest", change)
						}
						after, afterControl := kernelJournalState(t, path+".moved")
						if !bytes.Equal(admitted, after) || admittedControl != afterControl || len(j.records) != 2 {
							t.Fatal("invalid recovery manifest changed the object or journal")
						}
					}
				}
				profile := j.records[0]
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				prepared, closed := 0, 0
				report, err := recoverJournalDirectory(context.Background(), state, 8, 16, func(_ context.Context, info journalRecoveryInfo) (*journalRecoveryLease, error) {
					prepared++
					if info.Profile != profile.Profile || info.SID != profile.SID || info.Workspace != nil || len(info.Objects) != 1 || len(info.Volumes) != 1 || info.Volumes[0] != guard.objects[0].id.Volume {
						t.Fatal("selected sweep manifest", info)
					}
					return &journalRecoveryLease{Guard: recoveryTestGuard(func() error { closed++; return nil }), Volumes: map[uint64]windows.Handle{guard.objects[0].id.Volume: guard.ancestors[0]}}, nil
				})
				if err != nil || prepared != 1 || closed != 1 || len(report) != 1 || report[0].State != "recovered" {
					t.Fatal("selected recovery dispatch", report, err)
				}
				j, err = openBoundGrantJournal(context.Background(), journalPath, false)
				if err != nil {
					t.Fatal(err)
				}
				if err := recoverSelectedJournal(j, guard.ancestors[0]); err != nil {
					t.Fatal("repeat recovered completion", err)
				}
				after, afterControl := kernelJournalState(t, path+".moved")
				if !bytes.Equal(before, after) || control != afterControl || objectLabel(t, path+".moved") != label {
					t.Fatal("recovery did not restore the original kernel descriptor/label")
				}
				after, afterControl = kernelJournalState(t, path)
				if !bytes.Equal(replacement, after) || replacementControl != afterControl {
					t.Fatal("identity recovery edited the replacement path")
				}
				if !j.state.complete || len(j.state.pending) != 0 {
					t.Fatal("recovery obligations not durably completed")
				}
			})
		}
	}
}

func Test_WindowsAppContainerJournalProfileIntentRecovery(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	root := filepath.Join(t.TempDir(), "objects")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	guard, err := openWorkspaceTree(context.Background(), root, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	for _, stage := range []string{"intent-only", "deleted-before-complete"} {
		t.Run(stage, func(t *testing.T) {
			j, err := openGrantJournal(filepath.Join(t.TempDir(), "profile.journal"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if stage == "intent-only" {
				record := journalProfile()
				var random [16]byte
				if _, err := rand.Read(random[:]); err != nil {
					t.Fatal(err)
				}
				record.Profile = fmt.Sprintf("xmesh-l2-%x", random)
				name, err := windows.UTF16PtrFromString(record.Profile)
				if err != nil {
					t.Fatal(err)
				}
				var sid *windows.SID
				result, _, _ := deriveSID.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid)))
				if err := hresult("derive profile intent SID", result); err != nil {
					t.Fatal(err)
				}
				record.SID = sid.String()
				windows.FreeSid(sid)
				if _, err := j.append(record); err != nil {
					t.Fatal(err)
				}
			} else {
				p, err := newProfile(j)
				if err != nil {
					t.Fatal(err)
				}
				result, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(p.name)))
				if err := hresult("delete profile before completion record", result); err != nil {
					t.Fatal(err)
				}
				windows.FreeSid(p.sid)
				p.sid = nil
			}
			if err := recoverSelectedJournal(j, guard.ancestors[0]); err != nil {
				t.Fatal("recover profile creation/deletion window", err)
			}
			if !j.state.complete || len(j.records) != 2 {
				t.Fatal("profile recovery completion was not recorded")
			}
		})
	}
}

func Test_WindowsAppContainerSelectedJournalCrashHelper(t *testing.T) {
	journalPath := os.Getenv("XMESH_SELECTED_JOURNAL")
	if journalPath == "" {
		t.Skip("journaled grant forced-exit helper")
	}
	j, err := openBoundGrantJournal(context.Background(), journalPath, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newProfile(j)
	if err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("XMESH_SELECTED_PATH")
	grant, err := p.grantObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Close() == nil {
		t.Fatal("profile deleted before journal grant cleanup")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	switch os.Getenv("XMESH_SELECTED_STAGE") {
	case "removed":
		// Simulate cleanup completed before its release record reached disk.
		if err := setObjectGrant(grant.aclHandle, grant.ace, false); err != nil {
			t.Fatal(err)
		}
	case "released":
		if err := grant.Close(); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("ready")
	time.Sleep(25 * time.Second)
	t.Fatal("journaled grant helper was not terminated")
}
