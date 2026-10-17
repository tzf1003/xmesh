package winbox

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Programs that run inside an AppContainer in these tests are copies of this
// test binary selected by -test.run. Under `go test -race` that binary carries
// the ThreadSanitizer runtime, and the runtime maps its shadow memory at fixed
// addresses when the process starts. When Windows has placed something else in
// that range the process dies before any Go code runs, with exit status 66 and
// "ThreadSanitizer failed to allocate ... (error code: 487)". The payload code
// in these helpers is not what the race detector is meant to check, so the
// payload runs from an ordinary build of the same package. Helpers that are
// themselves the code under test (supervisor, keeper) and the race-runtime
// probes keep using os.Args[0].
//
// The ordinary build is made once, before the first test, because tests give
// their sandbox work a fixed time budget that a build must not eat into.
const sandboxHelperImageEnv = "XMESH_TEST_SANDBOX_HELPER_IMAGE"

func TestMain(m *testing.M) {
	// Child processes inherit the variable, so only the outermost test process builds.
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") == "1" && os.Getenv(sandboxHelperImageEnv) == "" {
		directory, err := os.MkdirTemp("", "xmesh-sandbox-helper-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "sandbox helper image: using the test binary itself:", err)
			os.Setenv(sandboxHelperImageEnv, "-")
		} else if image, err := buildSandboxHelperImage(directory); err != nil {
			fmt.Fprintln(os.Stderr, "sandbox helper image: using the test binary itself:", err)
			os.Setenv(sandboxHelperImageEnv, "-")
		} else {
			os.Setenv(sandboxHelperImageEnv, image)
		}
		code := m.Run()
		if directory != "" {
			_ = os.RemoveAll(directory)
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// buildSandboxHelperImage compiles this package's tests without the race
// detector. `go test` runs the test binary in the package directory.
func buildSandboxHelperImage(directory string) (string, error) {
	tool, err := osexec.LookPath("go")
	if err != nil {
		tool = filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	}
	image := filepath.Join(directory, "winbox-helper.test.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := osexec.CommandContext(ctx, tool, "test", "-c", "-o", image, ".")
	for _, entry := range os.Environ() {
		// A GOFLAGS=-race would put the race runtime straight back.
		if strings.HasPrefix(strings.ToUpper(entry), "GOFLAGS=") && strings.Contains(entry, "-race") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go test -c: %w: %s", err, output)
	}
	return image, nil
}

// sandboxHelperImage returns the executable image for programs that run inside
// an AppContainer: the ordinary build made by TestMain, or this test binary
// when that build is unavailable.
func sandboxHelperImage() ([]byte, error) {
	if image := os.Getenv(sandboxHelperImageEnv); image != "" && image != "-" {
		return os.ReadFile(image)
	}
	return os.ReadFile(os.Args[0])
}
