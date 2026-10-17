package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/sandbox"
	"github.com/tzf1003/xmesh/internal/winbox"
	"golang.org/x/sys/windows"
)

// An account that cannot read the change journal is refused before the runner
// creates any state, with a reason the node can show; an elevated account is
// not (the other test covers it).
func Test_Runtime_WindowsL2RealRunnerRefusesAccountWithoutJournalAccess(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	if winbox.ProbeChangeJournalAccess(t.TempDir()) == nil {
		t.Skip("this account can read the NTFS change journal")
	}
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("an elevated account was refused")
	}
	state := filepath.Join(t.TempDir(), "state")
	runner, err := platformL2Runner(context.Background(), filepath.Join(state, "sandbox"))
	var refused *sandbox.UnavailableError
	if runner != nil || !errors.As(err, &refused) || refused.Reason != changeJournalReason {
		t.Fatal("ordinary account was not refused with the reason:", runner, err)
	}
	if _, statErr := os.Stat(state); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("the refusal left runner state behind:", statErr)
	}
}

// The real AppContainer runner opens over an empty state directory, finds nothing
// to recover and shuts down cleanly. It launches nothing, so the test binary
// serving as supervisor and keeper executable does not matter here.
func Test_Runtime_WindowsL2RealRunnerOpensEmptyState(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	if err := winbox.ProbeChangeJournalAccess(t.TempDir()); err != nil {
		t.Skip("this account cannot read the NTFS change journal (needs an elevated Administrator or LocalSystem): ", err)
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for round := 0; round < 2; round++ {
		runner, err := platformL2Runner(ctx, filepath.Join(state, "sandbox"))
		if err != nil || runner == nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if err = runner.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
}
