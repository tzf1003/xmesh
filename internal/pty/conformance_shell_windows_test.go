//go:build windows

package pty

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
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

// `auto` picks pwsh first; when the system cannot launch that candidate (here a
// placeholder that is not an executable, standing in for a Store alias that
// refuses to start) pty.open falls back to powershell, while an explicitly
// requested pwsh fails with the Win32 error code in the result.
// conformance: CON-IMPL-04
func Test_CONIMPL04_WindowsAutoFallsBackWhenPwshCannotStart(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_PTY") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_PTY=1 on Windows with ConPTY and PowerShell")
	}
	dir := shellDir(t, "pwsh.exe")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0"))
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := cwd.New(root, nil, cwd.Policy{Allow: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.New(config.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	m := New(config.PTY{MaxSessions: 20, MaxPerCaller: 30, IdleTimeout: "30m", OrphanGrace: "10m", ScrollbackBytes: "1MiB", DefaultShell: "auto", ClosedRetain: "5m", ClosedRetainMax: 20}, identity.NewRegistry())
	t.Cleanup(m.Shutdown)
	c := identity.Caller{ID: "node/session"}
	open := func(shell string) (Object, error) {
		raw, _ := json.Marshal(Object{"cols": 80, "rows": 24, "shell": shell})
		return m.Call(context.Background(), c, r, p, "pty.open", raw)
	}
	opened, err := open("auto")
	if err != nil || opened["pid"] == 0 {
		t.Fatal("auto did not fall back to powershell", opened, err)
	}
	_, err = open("pwsh")
	var failure *toolerr.Error
	if !errors.As(err, &failure) || failure.Code != "internal" || failure.Fields["win32_error"] == nil {
		t.Fatalf("explicit pwsh must fail with its Win32 error code: %#v", err)
	}
}
