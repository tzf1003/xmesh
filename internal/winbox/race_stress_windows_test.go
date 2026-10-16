package winbox

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// Logs of earlier launches in this profile must not be attributed to this one.
	stale, _ := filepath.Glob(filepath.Join(p.Directory(), "probe-race.*"))
	for _, path := range stale {
		_ = os.Remove(path)
	}
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
		body, err := os.ReadFile(path)
		_ = os.Remove(path)
		if err == nil {
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

// Compare the same race-enabled child inside and outside an AppContainer. The
// racing child must report a data race and exit 66 when run normally. Whatever
// the AppContainer run does is recorded: the race runtime can instead abort with
// an internal error that leaves the same exit status without a race report.
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
	outsideHelper := ordinaryHelper(t)
	for _, mode := range []struct {
		name, marker string
		launches     int
	}{{"race", "XMESH_RACE_PROBE_RACE", 3}, {"epoch", "XMESH_RACE_PROBE_EPOCH", 10}, {"alloc", "XMESH_RACE_PROBE_ALLOC", 10}} {
		outside := probeOrdinaryHelper(t, outsideHelper, mode.marker)
		t.Logf("%s outside AppContainer: exit=%d report=%q output=%q", mode.name, outside.exit, firstLine(outside.races), truncate(outside.runtime, 600))
		histogram := map[string]int{}
		for i := 0; i < mode.launches; i++ {
			inside := probeAppContainerHelper(t, p, helper, "Test_WindowsAppContainerRaceProbeHelper", mode.marker)
			histogram[fmt.Sprintf("exit=%d report=%q runtime=%q", inside.exit, firstLine(inside.races), firstLine([]string{inside.runtime}))]++
		}
		for outcome, count := range histogram {
			t.Logf("%s inside AppContainer: %d of %d launches: %s", mode.name, count, mode.launches, outcome)
		}
	}
}

// raceRuntimeStartFailure recognises exit 66 produced by the race runtime's own
// memory mapping failing instead of by a reported data race.
func raceRuntimeStartFailure(r probeResult) bool {
	if r.exit != 66 {
		return false
	}
	text := strings.Join(r.races, "\n") + r.runtime
	return strings.Contains(text, "ThreadSanitizer failed to allocate") || strings.Contains(text, "ThreadSanitizer: failed to restore")
}

func truncate(text string, limit int) string {
	if len(text) > limit {
		return text[:limit]
	}
	return text
}

func firstLine(values []string) string {
	if len(values) == 0 {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(values[0]), "\n")
	return line
}

// ordinaryHelper copies the test binary once so repeated outside launches share it.
func ordinaryHelper(t *testing.T) string {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "probe-helper.exe")
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, body, 0700); err != nil {
		t.Fatal(err)
	}
	return helper
}

// probeOrdinaryHelper runs the helper as the current user with the same race
// log configuration, in a fresh working directory.
func probeOrdinaryHelper(t *testing.T, helper, marker string) probeResult {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "run")
	if err != nil {
		t.Fatal(err)
	}
	cmd := osexec.Command(helper, "-test.run=^Test_WindowsAppContainerRaceProbeHelper$")
	cmd.Dir = dir
	// The sanitizer option parser splits on ':', so a drive-letter path is not a
	// valid log_path; the child runs in dir and names the log relatively.
	cmd.Env = append(os.Environ(), marker+"=1", "GORACE=log_path=probe-race")
	output, _ := cmd.CombinedOutput()
	var result probeResult
	result.exit = uint32(cmd.ProcessState.ExitCode())
	result.runtime = string(output)
	logs, _ := filepath.Glob(filepath.Join(dir, "probe-race.*"))
	for _, path := range logs {
		if body, err := os.ReadFile(path); err == nil {
			result.races = append(result.races, string(body))
		}
	}
	return result
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
	startFailures := 0
	for i := 0; i < count; i++ {
		p, helper := stressProfile(t)
		result := probeAppContainerHelper(t, p, helper, "Test_WindowsAppContainerRaceProbeHelper", "XMESH_RACE_PROBE_NOOP")
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		histogram[result.exit]++
		if result.exit != 0 {
			line := fmt.Sprintf("launch %d: exit=%d runtime=%q races=%q", i, result.exit, result.runtime, result.races)
			if raceRuntimeStartFailure(result) {
				// The race runtime failed to map its own memory before any test code
				// ran. Recorded, not a product failure.
				t.Log("race runtime start failure:", line)
				startFailures++
			} else {
				failures = append(failures, line)
			}
		}
	}
	// The same launches outside an AppContainer give the baseline rate of the
	// race runtime's own start failures.
	outsideStartFailures, outsideOther := 0, 0
	outsideHelper := ordinaryHelper(t)
	for i := 0; i < count; i++ {
		outside := probeOrdinaryHelper(t, outsideHelper, "XMESH_RACE_PROBE_NOOP")
		if outside.exit != 0 {
			if raceRuntimeStartFailure(outside) {
				t.Log("outside race runtime start failure:", truncate(strings.Join(outside.races, "|")+outside.runtime, 300))
				outsideStartFailures++
			} else {
				outsideOther++
			}
		}
	}
	t.Logf("race runtime start failures: inside AppContainer %d of %d, outside %d of %d (other outside failures %d)", startFailures, count, outsideStartFailures, count, outsideOther)
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
var epochCounter atomic.Int64

func Test_WindowsAppContainerRaceProbeHelper(t *testing.T) {
	switch {
	case os.Getenv("XMESH_RACE_PROBE_NOOP") == "1":
		os.Exit(0)
	case os.Getenv("XMESH_RACE_PROBE_EPOCH") == "1":
		// Many synchronization events without any race, to exercise the race
		// runtime's per-thread clock and shadow memory handling.
		var mu sync.Mutex
		for i := 0; i < 400000; i++ {
			mu.Lock()
			epochCounter.Add(1)
			mu.Unlock()
		}
		os.Exit(0)
	case os.Getenv("XMESH_RACE_PROBE_ALLOC") == "1":
		block := make([]byte, 256<<20)
		for i := 0; i < len(block); i += 4096 {
			block[i] = 1
		}
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
