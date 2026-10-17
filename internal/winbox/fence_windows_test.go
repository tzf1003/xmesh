package winbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func skipUnlessWindowsSandbox(t *testing.T) {
	t.Helper()
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
}

// skipUnlessChangeJournal skips a test that needs the NTFS change journal when
// the account cannot read it (an ordinary account or an unelevated token). L2
// does not run there by design: the node refuses it and says why, which
// Test_WindowsAppContainerChangeSourceAccess and the probe test assert.
func skipUnlessChangeJournal(t *testing.T) {
	t.Helper()
	skipUnlessWindowsSandbox(t)
	if err := ProbeChangeJournalAccess(t.TempDir()); err != nil {
		t.Skip("this account cannot read the NTFS change journal (needs an elevated Administrator or LocalSystem): ", err)
	}
}

// fenceFixture creates base\outer\workspace and a private base\state journal
// directory, all on the local test volume.
func fenceFixture(t *testing.T) (outer, workspace, state string) {
	t.Helper()
	base := t.TempDir()
	outer = filepath.Join(base, "outer")
	workspace = filepath.Join(outer, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	state = filepath.Join(base, "state")
	privateJournalDirectory(t, state)
	return outer, workspace, state
}

// tamperDACL adds an Everyone read ACE as an external ACL writer would.
func tamperDACL(t *testing.T, path string) { tamperDACLFor(t, path, windows.WinWorldSid) }

// tamperDACLFor adds a distinct read ACE per well-known SID, so a second change
// to one object is a real descriptor change.
func tamperDACLFor(t *testing.T, path string, kind windows.WELL_KNOWN_SID_TYPE) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	everyone, err := windows.CreateWellKnownSid(kind)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.FILE_GENERIC_READ, AccessMode: windows.GRANT_ACCESS, Inheritance: windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP, TrusteeValue: windows.TrusteeValueFromSID(everyone)},
	}}, current)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

// makeJunction converts an empty directory into a mount-point reparse point.
func makeJunction(t *testing.T, directory, target string) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal("open directory for reparse", err)
	}
	defer windows.CloseHandle(h)
	substitute := utf16.Encode([]rune(`\??\` + target))
	length := len(substitute) * 2
	buffer := make([]byte, 8+8+length+2+2)
	binary.LittleEndian.PutUint32(buffer, windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(8+length+2+2))
	binary.LittleEndian.PutUint16(buffer[10:], uint16(length))
	binary.LittleEndian.PutUint16(buffer[12:], uint16(length+2))
	for i, unit := range substitute {
		binary.LittleEndian.PutUint16(buffer[16+i*2:], unit)
	}
	var returned uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil); err != nil {
		t.Fatal("set reparse point", err)
	}
}

func Test_WindowsAppContainerWriterFencePins(t *testing.T) {
	skipUnlessChangeJournal(t)
	ctx := context.Background()
	outer, workspace, state := fenceFixture(t)
	f, err := acquireWorkspaceWriterFence(ctx, workspace, state)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// The kernel itself rejects renaming or deleting any pinned directory.
	for _, path := range []string{outer, workspace, state, filepath.Dir(outer)} {
		if err := os.Rename(path, path+"-moved"); err == nil {
			t.Fatalf("pinned directory %s was renamed", path)
		}
		if err := os.Remove(path); err == nil {
			t.Fatalf("pinned directory %s was removed", path)
		}
	}
	// Activity below the pinned directories is not a violation.
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(outer, "sibling"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "other.journal"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(ctx); err != nil {
		t.Fatal("benign activity reported", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := f.Verify(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled verification", err)
	}
	if err := f.Verify(ctx); err != nil {
		t.Fatal("cancellation was recorded as a violation", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal("close is not idempotent", err)
	}
	if err := f.Verify(ctx); !errors.Is(err, errWriterFenceClosed) {
		t.Fatal("closed fence verified", err)
	}
	if err := os.Rename(outer, outer+"-moved"); err != nil {
		t.Fatal("pins were retained after close", err)
	}
}

func Test_WindowsAppContainerWriterFenceDetection(t *testing.T) {
	skipUnlessChangeJournal(t)
	ctx := context.Background()
	for _, mode := range []string{"ancestor-acl", "journal-acl", "root-acl-sealed", "root-reparse", "volume-journal-lost"} {
		t.Run(mode, func(t *testing.T) {
			outer, workspace, state := fenceFixture(t)
			f, err := acquireWorkspaceWriterFence(ctx, workspace, state)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			switch mode {
			case "ancestor-acl":
				tamperDACL(t, outer)
			case "journal-acl":
				tamperDACL(t, state)
			case "root-acl-sealed":
				// Before sealing, the root descriptor belongs to grant admission.
				tamperDACL(t, workspace)
				if err := f.Verify(ctx); err != nil {
					t.Fatal("unsealed root descriptor change reported", err)
				}
				if err := f.sealRoot(ctx); err != nil {
					t.Fatal(err)
				}
				if err := f.Verify(ctx); err != nil {
					t.Fatal("sealed root reported without a change", err)
				}
				tamperDACLFor(t, workspace, windows.WinBuiltinUsersSid)
			case "root-reparse":
				makeJunction(t, workspace, state)
			case "volume-journal-lost":
				// Losing the ability to observe is a violation, not a pass.
				f.volumes[0].cursor.Next = 1 << 62
			}
			err = f.Verify(ctx)
			if !errors.Is(err, errWriterFenceViolated) {
				t.Fatalf("%s was not reported: %v", mode, err)
			}
			if again := f.Verify(ctx); !errors.Is(again, errWriterFenceViolated) {
				t.Fatal("violation was not sticky", again)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Verify(ctx); !errors.Is(err, errWriterFenceViolated) {
				t.Fatal("close cleared a violation", err)
			}
		})
	}
	t.Run("restored-acl", func(t *testing.T) {
		// A restored descriptor is still reported: the change journal records
		// the intermediate write.
		outer, workspace, state := fenceFixture(t)
		f, err := acquireWorkspaceWriterFence(ctx, workspace, state)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		original, err := windows.GetNamedSecurityInfo(outer, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err := original.DACL()
		if err != nil {
			t.Fatal(err)
		}
		tamperDACL(t, outer)
		if err := windows.SetNamedSecurityInfo(outer, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
			t.Fatal(err)
		}
		if err := f.Verify(ctx); !errors.Is(err, errWriterFenceViolated) {
			t.Fatal("restored descriptor change was not reported", err)
		}
	})
}

func Test_WindowsAppContainerWriterFenceBinding(t *testing.T) {
	skipUnlessChangeJournal(t)
	ctx := context.Background()
	_, workspace, state := fenceFixture(t)
	f, err := acquireWorkspaceWriterFence(ctx, workspace, state)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	open := func(workspace, state string) (*workspaceTree, *workspaceTree, func()) {
		w, err := openWorkspaceRoot(ctx, workspace)
		if err != nil {
			t.Fatal(err)
		}
		j, err := openJournalDirectory(ctx, state, false)
		if err != nil {
			w.Close()
			t.Fatal(err)
		}
		return w, j.tree, func() { w.Close(); j.Close() }
	}
	w, j, closeTrees := open(workspace, state)
	if err := f.bindChains(w, j); err != nil {
		t.Fatal("matching chains rejected", err)
	}
	closeTrees()
	other := filepath.Join(filepath.Dir(workspace), "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	w, j, closeTrees = open(other, state)
	defer closeTrees()
	if err := f.bindChains(w, j); !errors.Is(err, errWriterFenceViolated) {
		t.Fatal("replaced workspace chain accepted", err)
	}
	if err := f.Verify(ctx); !errors.Is(err, errWriterFenceViolated) {
		t.Fatal("binding mismatch did not poison the fence", err)
	}
}

func Test_WindowsAppContainerWorkspaceExecutionWriterFence(t *testing.T) {
	skipUnlessChangeJournal(t)
	t.Run("opaque-guard-rejected", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		options := workspaceExecutionTestOptions(t)
		options.RequireWriterFence = true
		closes := 0
		e, err := newWorkspaceExecution(ctx, options, func(context.Context, workspaceExecutionOptions) (io.Closer, error) {
			return recoveryTestGuard(func() error { closes++; return nil }), nil
		})
		if err == nil || e == nil {
			t.Fatal("an opaque guard satisfied the fence requirement", err)
		}
		if _, statErr := os.Stat(options.Journal); !errors.Is(statErr, os.ErrNotExist) || e.journal != nil || e.ready {
			t.Fatal("state was created before the fence proof")
		}
		if err := e.closeContext(ctx, 0); err != nil || closes != 1 {
			t.Fatal("rejected guard was not released exactly once", err, closes)
		}
	})
	for _, mode := range []string{"clean", "violation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			options := workspaceExecutionTestOptions(t)
			options.RequireWriterFence = true
			if err := os.WriteFile(filepath.Join(options.Workspace, "initial.txt"), []byte("initial"), 0600); err != nil {
				t.Fatal(err)
			}
			before, control := kernelJournalState(t, options.Workspace)
			e, err := newWorkspaceExecution(ctx, options, kernelWriterFencePrepare)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := e.closeContext(context.Background(), 91); err != nil && mode != "violation" {
					t.Error("lifecycle cleanup", err)
				}
			}()
			if e.fence == nil {
				t.Fatal("lifecycle did not retain the verified fence")
			}
			journal, profile, monitor := e.journal, e.profile, e.monitor
			helper := filepath.Join(profile.Directory(), "fence-helper.exe")
			body, err := os.ReadFile(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(helper, body, 0700); err != nil {
				t.Fatal(err)
			}
			proof := filepath.Join(profile.Directory(), "ready")
			process, err := e.start(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerWorkspaceChangeMonitorHelper$", "--", options.Workspace, proof, "plain"},
				profile.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_CHANGE_MONITOR_HELPER=1"}, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process.Process)
			defer windows.CloseHandle(process.Thread)
			for {
				data, _ := os.ReadFile(proof)
				if string(data) == "ready" {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("fence helper readiness", ctx.Err())
				case <-monitor.done:
					t.Fatal("fence monitor ended before any external write", monitor.err)
				case <-time.After(10 * time.Millisecond):
				}
			}
			if mode == "clean" {
				// Many monitor cycles pass over the sandbox's own creations and
				// this package's grants without a false violation.
				time.Sleep(500 * time.Millisecond)
				select {
				case <-monitor.done:
					t.Fatal("clean execution faulted", monitor.err)
				default:
				}
				if err := e.fence.Verify(ctx); err != nil {
					t.Fatal(err)
				}
				if err := e.closeContext(ctx, 91); err != nil {
					t.Fatal("clean close", err)
				}
				if err := e.fence.Verify(ctx); !errors.Is(err, errWriterFenceClosed) {
					t.Fatal("fence pins outlived the lifecycle", err)
				}
			} else {
				tamperDACL(t, filepath.Dir(options.Workspace))
				select {
				case <-monitor.done:
				case <-ctx.Done():
					t.Fatal("external ACL write did not fault the monitor")
				}
				if !errors.Is(monitor.err, errWriterFenceViolated) {
					t.Fatal("monitor fault cause", monitor.err)
				}
				if result, err := windows.WaitForSingleObject(process.Process, 5000); err != nil || result != windows.WAIT_OBJECT_0 {
					t.Fatal("violation left the sandbox process running", result, err)
				}
				if _, err := e.start(ctx, []string{helper}, profile.Directory(), nil, nil, 0); err == nil {
					t.Fatal("violated lifecycle admitted a process")
				}
				if err := e.closeContext(ctx, 91); !errors.Is(err, errWriterFenceViolated) {
					t.Fatal("close did not report the violation", err)
				}
			}
			if !e.closed || !journal.state.complete {
				t.Fatal("cleanup did not complete", e.closed)
			}
			after, afterControl := kernelJournalState(t, options.Workspace)
			if mode == "clean" && (string(before) != string(after) || control != afterControl) {
				t.Fatal("workspace descriptor changed after fenced lifecycle")
			}
			// The external ACL write re-propagated inheritance through the Win32
			// API, so the violated run only requires the profile ACE to be gone.
			sid, err := windows.StringToSid(journal.records[0].SID)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(after, unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())) {
				t.Fatal("workspace retained the profile ACE after fenced lifecycle")
			}
		})
	}
}
