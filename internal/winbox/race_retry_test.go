package winbox

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// raceRuntimeLogs returns the contents of the race runtime logs a helper left in
// dir under the GORACE log_path prefix.
func raceRuntimeLogs(dir, prefix string) []string {
	var logs []string
	paths, _ := filepath.Glob(filepath.Join(dir, prefix+".*"))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(file, 64*1024))
		_ = file.Close()
		logs = append(logs, string(body))
	}
	return logs
}

// raceRuntimeStartFailed reports whether a helper's exit status 66 came from the
// race runtime failing to map its own memory at start instead of from a reported
// data race. Only that signature earns a single relaunch; any other status 66,
// and any output that also carries a race report, is a real failure.
func raceRuntimeStartFailed(exit uint32, diagnostics ...string) bool {
	if exit != 66 {
		return false
	}
	text := strings.Join(diagnostics, "\n")
	return strings.Contains(text, "failed to allocate") && !strings.Contains(text, "DATA RACE")
}

func Test_RaceRuntimeStartFailureClassification(t *testing.T) {
	const allocate = "ThreadSanitizer failed to allocate 0x1000 (4096) bytes at address 7ff000000000 (error code: 1455)"
	for _, c := range []struct {
		name        string
		exit        uint32
		diagnostics []string
		retry       bool
	}{
		{"allocation failure", 66, []string{allocate}, true},
		{"allocation failure in race log", 66, []string{"", allocate}, true},
		{"no diagnostics", 66, nil, false},
		{"other exit status", 1, []string{allocate}, false},
		{"zero status", 0, []string{allocate}, false},
		{"data race report", 66, []string{"WARNING: DATA RACE\n" + allocate}, false},
		{"data race in another file", 66, []string{allocate, "WARNING: DATA RACE"}, false},
		{"unrelated sanitizer error", 66, []string{"ThreadSanitizer: unexpected memory mapping"}, false},
	} {
		if got := raceRuntimeStartFailed(c.exit, c.diagnostics...); got != c.retry {
			t.Errorf("%s: retry=%t, want %t", c.name, got, c.retry)
		}
	}
}
