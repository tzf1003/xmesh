package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestBackgroundStandardIO(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"pipes", "closed"} {
		cmd := exec.Command(executable, "-test.run=^TestBackgroundStandardIOHelper$")
		cmd.Env = append(os.Environ(), "XMESH_TEST_BACKGROUND_STDIO="+mode)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		output, err := cmd.CombinedOutput()
		if err != nil || mode == "pipes" && string(output) != "inherited output" {
			t.Fatalf("%s: %q %v", mode, output, err)
		}
	}
}

func TestBackgroundStandardIOHelper(t *testing.T) {
	mode := os.Getenv("XMESH_TEST_BACKGROUND_STDIO")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	if mode == "closed" {
		_ = os.Stdin.Close()
		_ = os.Stdout.Close()
		_ = os.Stderr.Close()
	}
	prepareStandardIO()
	for _, stream := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		if _, err := stream.Stat(); err != nil {
			os.Exit(2)
		}
	}
	if _, err := fmt.Fprint(os.Stdout, "inherited output"); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
