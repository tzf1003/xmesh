package winbox

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// startWithConsole attaches a caller-owned pseudoconsole capability. The caller
// drains its output concurrently and retains the HPCON until all attached
// processes have exited. Job membership, census admission and supervision are
// identical to other managed starts. Pipes and HPCON lifetime stay with caller.
func (e *executionProcesses) startWithConsole(ctx context.Context, args []string, directory string, environment []string, console windows.Handle) (windows.ProcessInformation, error) {
	if console == 0 || console == windows.InvalidHandle {
		return windows.ProcessInformation{}, fmt.Errorf("AppContainer requires a valid pseudoconsole")
	}
	return e.startWithIO(ctx, args, directory, environment, nil, console)
}

var updateExecutionConsoleAttribute = windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")

func applyExecutionConsole(attrs *windows.ProcThreadAttributeListContainer, console windows.Handle) error {
	// PSEUDOCONSOLE takes the HPCON value itself, rather than its address.
	// Keep the value opaque through the syscall boundary.
	ok, _, err := updateExecutionConsoleAttribute.Call(uintptr(unsafe.Pointer(attrs.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(console), unsafe.Sizeof(console), 0, 0)
	if ok == 0 {
		return fmt.Errorf("attach AppContainer pseudoconsole: %w", err)
	}
	return nil
}
