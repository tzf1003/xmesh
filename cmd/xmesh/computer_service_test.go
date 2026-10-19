package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestComputerHostServiceRunsInUserGraphicalSession(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "xmesh")
	helper := filepath.Join(root, "xmesh-computer")
	state := filepath.Join(root, "state")
	c := computerServiceConfig(exe, helper, state)
	if c.Name != "xmesh-computer-host" || c.Executable != exe || c.Option["UserService"] != true {
		t.Fatal(c)
	}
	if !slices.Equal(c.Arguments, []string{"computer", "host", "--state-dir", state, "--helper", helper}) {
		t.Fatal(c.Arguments)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(c.Option["LaunchdConfig"].(string), "Aqua") {
		t.Fatal("desktop host lacks Aqua session constraint")
	}
	if runtime.GOOS == "linux" && !strings.Contains(c.Option["SystemdScript"].(string), "PartOf=graphical-session.target") {
		t.Fatal("host outlives graphical session")
	}
}

func TestComputerHelperUsesStableApplicationIdentity(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "xmesh")
	if runtime.GOOS != "darwin" {
		return
	}
	app := filepath.Join(root, "xmesh-computer.app", "Contents", "MacOS", "xmesh-computer")
	if err := os.MkdirAll(filepath.Dir(app), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app, []byte("binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if nativeHelperPath(exe) != app {
		t.Fatal(nativeHelperPath(exe))
	}
}
