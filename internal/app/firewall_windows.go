package app

import (
	"context"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// probeInboundBlock reads the enabled inbound block rules of Windows Firewall and reports whether one
// of them applies to exe. Rules for any program, or for other programs, do not count. The output is
// forced to UTF-8 so that a user name in the path survives the console code page.
func probeInboundBlock(exe string) (bool, error) {
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"Get-NetFirewallRule -Direction Inbound -Action Block -Enabled True | Get-NetFirewallApplicationFilter | ForEach-Object { $_.Program }"
	cmd := exec.CommandContext(ctx, filepath.Join(directory, "WindowsPowerShell", "v1.0", "powershell.exe"),
		"-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return blockedBy(string(out), exe), nil
}
