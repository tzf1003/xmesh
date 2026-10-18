//go:build !windows

package ci

import (
	"os/exec"
	"runtime"
	"sync"
)

// SystemInhibitor holds a sleep inhibition while jobs run: caffeinate on macOS, systemd-inhibit
// on Linux. Where neither exists it does nothing.
func SystemInhibitor() Inhibitor {
	return func() func() {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("caffeinate", "-i")
		case "linux":
			cmd = exec.Command("systemd-inhibit", "--what=sleep:idle", "--who=xmesh", "--why=GitHub Actions job running", "--mode=block", "sleep", "infinity")
		default:
			return func() {}
		}
		if err := cmd.Start(); err != nil {
			return func() {}
		}
		var once sync.Once
		return func() {
			once.Do(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
		}
	}
}
