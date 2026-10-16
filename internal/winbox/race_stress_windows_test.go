package winbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The Go race detector exits with status 66 when it has reported a race, and
// the sanitizer runtime uses the same status when it aborts internally (for
// example when it cannot map shadow memory at start). Both look identical to a
// parent that only sees the exit code. These tests establish, in a real
// AppContainer, what a status-66 child leaves behind and how often a trivial
// race-enabled child fails to start.

type probeResult struct {
	exit    uint32
	runtime string
	races   []string
}

// probeAppContainerHelper launches helper in a fresh managed execution without
// failing on a nonzero exit, returning the exit status and every diagnostic the
// child could leave in its profile directory.
func probeAppContainerHelper(t *testing.T, p *Profile, helper, testName, marker string, arguments ...string) probeResult {
	t.Helper()
	owner, err := newExecutionProcesses(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := owner.reap(ctx, 1); err != nil {
			t.Error("probe reaping", err)
		}
		if err := owner.Close(); err != nil {
			t.Error("probe owner close", err)
		}
	}()
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	diagnostics, err := os.Create(filepath.Join(p.Directory(), "probe-runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer diagnostics.Close()
	stdio := &executionStdio{windows.Handle(input.Fd()), windows.Handle(diagnostics.Fd()), windows.Handle(diagnostics.Fd())}
	command := append([]string{helper, "-test.run=^" + testName + "$", "--"}, arguments...)
	process, err := owner.startWithStdio(context.Background(), command, p.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), marker + "=1", "GORACE=log_path=probe-race"}, stdio)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	if result, err := windows.WaitForSingleObject(process.Process, 30000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("probe helper wait", result, err)
	}
	var result probeResult
	if err := windows.GetExitCodeProcess(process.Process, &result.exit); err != nil {
		t.Fatal(err)
	}
	if _, err := diagnostics.Seek(0, io.SeekStart); err == nil {
		body, _ := io.ReadAll(io.LimitReader(diagnostics, 64*1024))
		result.runtime = string(body)
	}
	logs, _ := filepath.Glob(filepath.Join(p.Directory(), "probe-race.*"))
	for _, path := range logs {
		if body, err := os.ReadFile(path); err == nil {
			if len(body) > 64*1024 {
				body = body[:64*1024]
			}
			result.races = append(result.races, string(body))
		}
	}
	return result
}

func stressProfile(t *testing.T) (*Profile, string) {
	t.Helper()
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(p.Directory(), "probe-helper.exe")
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		_ = p.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, body, 0700); err != nil {
		_ = p.Close()
		t.Fatal(err)
	}
	return p, helper
}

// A child that really races must exit with 66 inside an AppContainer, leave a
// report in its private directory and show the same status to its parent as any
// other status-66 failure. This pins the mapping and proves the capture works.
func Test_WindowsAppContainerRaceDiagnosticsControl(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" || os.Getenv("XMESH_TEST_WINDOWS_RACE_STRESS") == "" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 and XMESH_TEST_WINDOWS_RACE_STRESS on Windows")
	}
	p, helper := stressProfile(t)
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	result := probeAppContainerHelper(t, p, helper, "Test_WindowsAppContainerRaceProbeHelper", "XMESH_RACE_PROBE_RACE")
	t.Logf("racing child: exit=%d runtime=%q races=%d", result.exit, result.runtime, len(result.races))
	if result.exit != 66 {
		t.Fatalf("racing AppContainer child exited %d, want the race detector status 66", result.exit)
	}
	if len(result.races) == 0 || !strings.Contains(result.races[0], "DATA RACE") {
		t.Fatalf("race report was not captured: runtime=%q races=%q", result.runtime, result.races)
	}
}

// Launch trivial race-enabled children repeatedly, each in a fresh profile, and
// record every exit status with its diagnostics. A start-up failure of the race
// runtime shows as status 66 without a data-race report.
func Test_WindowsAppContainerRaceHelperLaunchStress(t *testing.T) {
	count, _ := strconv.Atoi(os.Getenv("XMESH_TEST_WINDOWS_RACE_STRESS"))
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" || count < 1 {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 and XMESH_TEST_WINDOWS_RACE_STRESS=<launches> on Windows")
	}
	histogram := map[uint32]int{}
	var failures []string
	for i := 0; i < count; i++ {
		p, helper := stressProfile(t)
		result := probeAppContainerHelper(t, p, helper, "Test_WindowsAppContainerRaceProbeHelper", "XMESH_RACE_PROBE_NOOP")
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		histogram[result.exit]++
		if result.exit != 0 {
			failures = append(failures, fmt.Sprintf("launch %d: exit=%d runtime=%q races=%q", i, result.exit, result.runtime, result.races))
		}
	}
	var keys []int
	for exit := range histogram {
		keys = append(keys, int(exit))
	}
	sort.Ints(keys)
	for _, exit := range keys {
		t.Logf("exit status %d: %d of %d launches", exit, histogram[uint32(exit)], count)
	}
	if len(failures) != 0 {
		t.Errorf("%d of %d trivial AppContainer launches failed:\n%s", len(failures), count, strings.Join(failures, "\n"))
	}
}

var racyCounter int

func Test_WindowsAppContainerRaceProbeHelper(t *testing.T) {
	switch {
	case os.Getenv("XMESH_RACE_PROBE_NOOP") == "1":
		os.Exit(0)
	case os.Getenv("XMESH_RACE_PROBE_RACE") == "1":
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 1000; j++ {
					racyCounter++
				}
			}()
		}
		wg.Wait()
		os.Exit(0)
	default:
		t.Skip("AppContainer race probe helper")
	}
}
