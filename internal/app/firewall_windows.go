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
// of them applies to exe on a network this node is connected to. The script prints the rules with the
// profiles they apply to, and the active network categories; firewallBlocks decides. The output is
// forced to UTF-8 so that a user name in the path survives the console code page.
func probeInboundBlock(exe string) (bool, error) {
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"$profileOf = @{}; Get-NetFirewallRule -Direction Inbound -Action Block -Enabled True | ForEach-Object { $profileOf[$_.InstanceID] = [string]$_.Profile }; " +
		"Get-NetFirewallApplicationFilter | Where-Object { $profileOf.ContainsKey($_.InstanceID) } | ForEach-Object { 'rule|' + $_.Program + '|' + $profileOf[$_.InstanceID] }; " +
		"Get-NetConnectionProfile | ForEach-Object { 'network|' + $_.NetworkCategory }"
	cmd := exec.CommandContext(ctx, filepath.Join(directory, "WindowsPowerShell", "v1.0", "powershell.exe"),
		"-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return firewallBlocks(string(out), exe), nil
}
