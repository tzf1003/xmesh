package winbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/sandbox"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// timingCapture collects the runner's per-execution cost records.
type timingCapture struct {
	mu      sync.Mutex
	records []string
}

func (c *timingCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *timingCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *timingCapture) WithGroup(string) slog.Handler            { return c }
func (c *timingCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "sandbox_execution_timing" {
		return nil
	}
	var b strings.Builder
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, "%s=%v ", a.Key, a.Value)
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, strings.TrimSpace(b.String()))
	c.mu.Unlock()
	return nil
}

func (c *timingCapture) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.records
	c.records = nil
	return out
}

// populateWorkspace adds count ordinary files below directories of at most a
// hundred entries, so the object count is count plus the directories.
func populateWorkspace(t *testing.T, root string, count int) int {
	t.Helper()
	objects := 0
	for created := 0; created < count; {
		dir := filepath.Join(root, fmt.Sprintf("d%04d", created/100))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		objects++
		for i := 0; i < 100 && created < count; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			created++
			objects++
		}
	}
	return objects
}

// Test_WindowsAdmissionCost is a measurement, not a regression gate: it prints
// where one execution's sandbox cost goes for small and large workspaces, with
// the in-test coordinator and with the production keeper plus writer fence.
// Select it explicitly; it takes a few minutes at the largest size.
func Test_WindowsAdmissionCost(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_ADMISSION_COST") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_ADMISSION_COST=1 and XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	capture := &timingCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, production := range []bool{false, true} {
		for _, files := range []int{0, 1000, 4000, 9000} {
			if production && files == 4000 {
				continue
			}
			name := fmt.Sprintf("production=%v/files=%d", production, files)
			t.Run(name, func(t *testing.T) {
				f := newRunnerFixtureWith(t, production)
				f.runner.config.MaxObjects = 10000
				objects := populateWorkspace(t, f.root, files)
				m := f.execManager(t, "300s")
				for run := 1; run <= 3; run++ {
					start := time.Now()
					result, err := f.run(m, f.helperCall("conpty"))
					wall := time.Since(start)
					if err != nil {
						t.Fatal(err)
					}
					if result["exit_code"] != 0 {
						t.Fatalf("exec result: %v", result)
					}
					for _, record := range capture.take() {
						t.Logf("COST %s run=%d wall=%dms workspace_objects~%d %s", name, run, wall.Milliseconds(), objects, record)
					}
				}
			})
		}
	}
}

func countWorkspaceObjects(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(string, os.DirEntry, error) error { count++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return count
}

// A workspace over the object limit is refused with a rule the caller can act
// on, before any profile, keeper or fence is created; one exactly at the limit
// is admitted.
func Test_WindowsManagedRunnerWorkspaceTooLarge(t *testing.T) {
	f := newRunnerFixture(t)
	populateWorkspace(t, f.root, 100)
	objects := countWorkspaceObjects(t, f.root)
	m := f.execManager(t, "60s")

	f.runner.config.MaxObjects = objects - 1
	_, err := f.run(m, f.helperCall("conpty"))
	var denied *toolerr.Error
	if !errors.As(err, &denied) || denied.Code != "policy_denied" || denied.Fields["rule"] != "sandbox.workspace_too_large" || denied.Fields["limit"] != objects-1 {
		t.Fatal("oversized workspace was not refused with its own rule", err)
	}
	var tooLarge *sandbox.WorkspaceTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatal("the typed cause was dropped", err)
	}
	f.mu.Lock()
	prepared := f.prepared
	f.mu.Unlock()
	if prepared != 0 {
		t.Fatal("an oversized workspace reached the coordinator", prepared)
	}
	if entries, err := os.ReadDir(f.runner.config.StateDir); err != nil || len(entries) != 0 {
		t.Fatal("an oversized workspace left state behind", entries, err)
	}

	f.runner.config.MaxObjects = objects
	result, err := f.run(m, f.helperCall("conpty"))
	if err != nil || result["exit_code"] != 0 {
		t.Fatal("a workspace at the limit was refused", result, err)
	}
}
