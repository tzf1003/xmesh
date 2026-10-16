package winbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceExceedsCountsRootFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	// Root plus 2 directories plus 5 files = 8 objects.
	for _, dir := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		dir := "a"
		if i%2 == 0 {
			dir = "b"
		}
		if err := os.WriteFile(filepath.Join(root, dir, fmt.Sprint("f", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for limit, want := range map[int]bool{9: false, 8: false, 7: true, 1: true, 100: false} {
		if got := workspaceExceeds(ctx, root, limit); got != want {
			t.Errorf("limit %d: got %v want %v", limit, got, want)
		}
	}
}

func TestWorkspaceExceedsStopsEarlyAndIgnoresUnreadablePaths(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 700; i++ { // more than one ReadDir batch
		if err := os.WriteFile(filepath.Join(root, fmt.Sprint("f", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !workspaceExceeds(context.Background(), root, 300) || workspaceExceeds(context.Background(), root, 701) {
		t.Fatal("limit across ReadDir batches")
	}
	if workspaceExceeds(context.Background(), filepath.Join(root, "missing"), 1) != false {
		// A missing root counts as the root object only; admission reports the error.
		t.Fatal("missing root treated as oversized")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if workspaceExceeds(cancelled, root, 1) {
		t.Fatal("a cancelled count must not refuse the workspace")
	}
}
