package winbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func privateJournalDirectory(t *testing.T, path string) {
	t.Helper()
	d, err := openJournalDirectory(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerBoundJournalNamespace(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, mode := range []string{"normal", "torn", "close-error"} {
		t.Run(mode, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "parent")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(parent, "state")
			privateJournalDirectory(t, state)
			if existing, err := openJournalDirectory(context.Background(), state, true); err == nil || existing != nil {
				t.Fatal("private directory creation reused an existing namespace")
			}
			path := filepath.Join(state, "one.journal")
			j, err := openBoundGrantJournal(context.Background(), path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if other, err := openBoundGrantJournal(context.Background(), path, false); err == nil || other != nil {
				t.Fatal("active bound journal admitted a second owner")
			}
			if _, err := j.append(journalProfile()); err != nil {
				t.Fatal(err)
			}
			second, err := openBoundGrantJournal(context.Background(), filepath.Join(state, "two.journal"), true)
			if err != nil {
				t.Fatal("independent journal owner in bound directory", err)
			}
			defer second.Close()
			if err := os.Rename(state, state+".moved"); err == nil {
				t.Fatal("active journal lost its directory binding")
			}
			if err := os.Rename(parent, parent+".moved"); err == nil {
				t.Fatal("active journal lost its ancestor binding")
			}
			if mode == "torn" {
				if _, err := j.file.WriteAt([]byte{1, 2, 3}, j.bytes); err != nil {
					t.Fatal(err)
				}
				if err := j.file.Sync(); err != nil {
					t.Fatal(err)
				}
			} else if mode == "close-error" {
				if err := j.file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.Close(); (err != nil) != (mode == "close-error") {
				t.Fatal("bound close error propagation", err)
			}
			if j.namespace != nil || j.file != nil {
				t.Fatal("closed journal retained ownership")
			}
			if err := os.Rename(state, state+".moved"); err == nil {
				t.Fatal("one owner released another owner's namespace guard")
			}
			if err := second.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = openBoundGrantJournal(context.Background(), path, false)
			if j != nil {
				defer j.Close()
			}
			if mode == "torn" {
				if !errors.Is(err, errJournalIncomplete) || j == nil || len(j.records) != 1 {
					t.Fatal("bound recovery lost damaged journal prefix", err)
				}
			} else if err != nil || j == nil || len(j.records) != 1 {
				t.Fatal("bound journal readback", err)
			}
			if err := os.Rename(state, state+".moved"); err == nil {
				t.Fatal("recovery journal lost namespace binding")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(state, state+".moved"); err != nil {
				t.Fatal("journal namespace guard retained after Close", err)
			}
			if err := os.Rename(parent, parent+".moved"); err != nil {
				t.Fatal("journal ancestor guard retained after Close", err)
			}
		})
	}
}

func Test_WindowsAppContainerJournalDirectorySecurity(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	base := t.TempDir()
	unsafe := filepath.Join(base, "inherited")
	if err := os.Mkdir(unsafe, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(unsafe, "never-created.journal")
	if j, err := openBoundGrantJournal(context.Background(), path, true); err == nil || j != nil {
		t.Fatal("unprotected directory admitted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe state-directory admission created a journal", err)
	}
	state := filepath.Join(base, "state")
	privateJournalDirectory(t, state)
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	if j, err := openBoundGrantJournal(context.Background(), filepath.Join(alias, "alias.journal"), true); err == nil || j != nil {
		t.Fatal("reparse state-directory alias admitted")
	}
	if _, err := os.Stat(filepath.Join(state, "alias.journal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reparse admission touched the real directory", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d, err := openJournalDirectory(ctx, filepath.Join(base, "cancelled"), true); !errors.Is(err, context.Canceled) || d != nil {
		t.Fatal("cancelled state-directory creation admitted", err)
	}
	if _, err := os.Stat(filepath.Join(base, "cancelled")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled admission created a directory", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, dacl := range []string{
		"D:P(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")",
		"D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FR;;;WD)",
		"D:(A;OICI;FA;;;SY)(A;OICI;FA;;;" + user.User.Sid.String() + ")",
		"D:P(A;OICI;FR;;;SY)(A;OICI;FA;;;" + user.User.Sid.String() + ")",
	} {
		sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + dacl)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRecoverySecurity(sd, user.User.Sid, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE); err == nil {
			t.Fatal("unsafe directory descriptor admitted")
		}
	}
}

func Test_WindowsAppContainerJournalDirectoryWorkspaceBoundary(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	base := t.TempDir()
	state := filepath.Join(base, "state")
	privateJournalDirectory(t, state)
	inside := filepath.Join(state, "workspace")
	file := filepath.Join(inside, "selected.txt")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := openBoundGrantJournal(context.Background(), filepath.Join(state, "boundary.journal"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	p, err := newProfile(j)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, path := range []string{base, state, inside} {
		before, control := kernelJournalState(t, path)
		if g, err := p.grantWorkspace(context.Background(), path, 32); err == nil || g != nil {
			t.Fatal("workspace/state containment admitted", path)
		}
		after, afterControl := kernelJournalState(t, path)
		if !bytes.Equal(before, after) || control != afterControl || len(j.records) != 1 || p.grants != 0 {
			t.Fatal("rejected state containment changed ACL or journal")
		}
	}
	for _, path := range []string{base, state, inside, file} {
		before, control := kernelJournalState(t, path)
		if g, err := p.grantObject(path); err == nil || g != nil {
			t.Fatal("selected state boundary admitted", path)
		}
		after, afterControl := kernelJournalState(t, path)
		if !bytes.Equal(before, after) || control != afterControl || len(j.records) != 1 || p.grants != 0 {
			t.Fatal("rejected selected boundary changed ACL or journal")
		}
	}
	root := filepath.Join(base, "independent-workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	g, err := p.grantWorkspace(context.Background(), root, 32)
	if err != nil {
		t.Fatal("disjoint bound workspace admission", err)
	}
	defer g.Close()
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(state, state+".moved"); err == nil {
		t.Fatal("profile completion released the journal namespace")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(state, state+".moved"); err != nil {
		t.Fatal("closed journal retained the state namespace", err)
	}
}
