package computer

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNativeProcessHasNoConsole(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestNativeConsoleHelper$")
	cmd.Env = append(os.Environ(), "XMESH_TEST_CONSOLE_PROOF=1")
	prepareNativeProcess(cmd)
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var proof struct {
		Console uintptr `json:"console"`
	}
	if err := json.Unmarshal(output, &proof); err != nil || proof.Console != 0 {
		t.Fatalf("native process created a console: %s (%v)", output, err)
	}
}

func TestNativeConsoleHelper(t *testing.T) {
	if os.Getenv("XMESH_TEST_CONSOLE_PROOF") != "1" {
		t.Skip("subprocess helper")
	}
	window, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]uintptr{"console": window}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
