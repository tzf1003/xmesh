package computer

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Anonymous protocol pipes still work without creating a console on the desktop.
func prepareNativeProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
