package winbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerJournalExclusiveAndCrash(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, mode := range []string{"flushed", "torn"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "grants.journal")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^Test_WindowsAppContainerJournalCrashHelper$")
			cmd.Env = append(os.Environ(), "XMESH_JOURNAL_HELPER="+path, "XMESH_JOURNAL_TAIL="+mode)
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
				if line != "flushed\n" && line != "flushed\r\n" {
					t.Fatal("journal helper did not flush", line)
				}
			case <-ctx.Done():
				t.Fatal("journal helper startup deadline")
			}
			if other, err := openGrantJournal(path, false); err == nil || other != nil {
				if other != nil {
					other.Close()
				}
				t.Fatal("recovery opened the active owner's journal")
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			waited = true
			journal, err := openGrantJournal(path, false)
			if mode == "flushed" && err != nil {
				t.Fatal(err)
			}
			if mode == "torn" && !errors.Is(err, errJournalIncomplete) {
				t.Fatal("crashed partial tail was not stopped", err)
			}
			if journal == nil {
				t.Fatal("flushed recovery prefix unavailable")
			}
			defer journal.Close()
			if len(journal.records) != 2 || !journal.state.pending[2] {
				t.Fatal("forced owner exit lost the flushed grant intent")
			}
			if other, err := openGrantJournal(path, true); err == nil || other != nil {
				t.Fatal("journal creation overwrote an existing record")
			}
			if mode == "flushed" {
				if _, err := journal.append(journalRecord{Kind: "released", GrantSequence: 2}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func Test_WindowsAppContainerJournalSecurity(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, dacl := range []string{
		"D:P(A;;FA;;;WD)", "D:(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")",
		"D:P(A;;FR;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")", "D:NO_ACCESS_CONTROL",
	} {
		sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + dacl)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJournalSecurity(sd, user.User.Sid); err == nil {
			t.Fatal("journal accepted an unsafe security descriptor", dacl)
		}
	}
	path := filepath.Join(t.TempDir(), "private.journal")
	j, err := openGrantJournal(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalProfile()); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	alias := path + ".hardlink"
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if linked, err := openGrantJournal(alias, false); err == nil || linked != nil {
		if linked != nil {
			linked.Close()
		}
		t.Fatal("journal recovery accepted a hard-linked record")
	}
}

func Test_WindowsAppContainerJournalCrashHelper(t *testing.T) {
	path := os.Getenv("XMESH_JOURNAL_HELPER")
	if path == "" {
		t.Skip("forced-exit journal helper")
	}
	j, err := openGrantJournal(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, record := range []journalRecord{journalProfile(), journalTestGrant()} {
		if _, err := j.append(record); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("XMESH_JOURNAL_TAIL") == "torn" {
		if _, err := j.file.WriteAt([]byte{1, 2, 3}, j.bytes); err != nil {
			t.Fatal(err)
		}
		if err := j.file.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("flushed")
	time.Sleep(20 * time.Second)
	t.Fatal("journal helper was not terminated")
}
