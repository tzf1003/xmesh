package winbox

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// describeHelperExit reports how an exited sandbox helper ended: its exit
// status, the race runtime logs left under the GORACE log_path prefix in dir,
// and the failure reason the helper itself wrote next to its proof file.
func describeHelperExit(process windows.Handle, dir, prefix, proof string) string {
	var exit uint32
	exitErr := windows.GetExitCodeProcess(process, &exit)
	diagnostics := raceRuntimeLogs(dir, prefix)
	if reason, err := os.ReadFile(proof + ".fail"); err == nil {
		diagnostics = append(diagnostics, string(reason))
	}
	return fmt.Sprintf("exit %d (%v), race start failure %t, diagnostics %q", exit, exitErr, raceRuntimeStartFailed(exit, diagnostics...), diagnostics)
}
