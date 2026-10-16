package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The real AppContainer runner opens over an empty state directory, finds nothing
// to recover and shuts down cleanly. It launches nothing, so the test binary
// serving as supervisor and keeper executable does not matter here.
func Test_Runtime_WindowsL2RealRunnerOpensEmptyState(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
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
