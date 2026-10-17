//go:build windows

package toolerr

import (
	"fmt"
	"testing"

	"golang.org/x/sys/windows"
)

func Test_WindowsInternalCarriesWin32Error(t *testing.T) {
	e := Internal("exec.start", fmt.Errorf("create process: %w", windows.ERROR_ACCESS_DENIED))
	if e.Fields["op"] != "exec.start" || e.Fields["win32_error"] != 5 || e.Fields["reason"] == "" {
		t.Fatalf("%v", e.Object())
	}
	if plain := Internal("pty.open", fmt.Errorf("no code")); plain.Fields["win32_error"] != nil {
		t.Fatalf("%v", plain.Object())
	}
}
