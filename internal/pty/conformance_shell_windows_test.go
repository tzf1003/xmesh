//go:build windows

package pty

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shellDir returns a directory containing placeholder executables with the
// given names, as the only PATH entry.
func shellDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("MZ"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// Resolving `auto` prefers pwsh, falls back to powershell, and fails rather
// than falling back to cmd.exe when neither exists.
// conformance: CON-IMPL-04
func Test_CONIMPL04_WindowsAutoResolution(t *testing.T) {
	t.Run("pwsh is preferred", func(t *testing.T) {
		dir := shellDir(t, "cmd.exe", "powershell.exe", "pwsh.exe")
		got, err := DefaultShell("auto")
		if err != nil || !strings.EqualFold(got, filepath.Join(dir, "pwsh.exe")) {
			t.Fatalf("auto = %q, %v", got, err)
		}
	})
	t.Run("powershell is the fallback", func(t *testing.T) {
		dir := shellDir(t, "cmd.exe", "powershell.exe")
		got, err := DefaultShell("")
		if err != nil || !strings.EqualFold(got, filepath.Join(dir, "powershell.exe")) {
			t.Fatalf("auto = %q, %v", got, err)
		}
	})
	t.Run("cmd is never selected", func(t *testing.T) {
		shellDir(t, "cmd.exe")
		t.Setenv("COMSPEC", filepath.Join(os.Getenv("PATH"), "cmd.exe"))
		got, err := DefaultShell("auto")
		if err == nil {
			t.Fatalf("auto resolved to %q with only cmd.exe available", got)
		}
	})
}
