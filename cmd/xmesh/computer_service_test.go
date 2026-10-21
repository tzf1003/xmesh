package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kardianos/service"
)

func TestEnsureComputerServiceStartedIsIdempotent(t *testing.T) {
	t.Run("already running", func(t *testing.T) {
		calls := 0
		err := ensureComputerServiceStarted(
			func() (service.Status, error) { return service.StatusRunning, nil },
			func() error { calls++; return nil },
			func() error { calls++; return nil },
		)
		if err != nil || calls != 0 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})

	t.Run("installed but stopped", func(t *testing.T) {
		started := false
		err := ensureComputerServiceStarted(
			func() (service.Status, error) { return service.StatusStopped, nil },
			func() error { t.Fatal("installed service should not be reinstalled"); return nil },
			func() error { started = true; return nil },
		)
		if err != nil || !started {
			t.Fatalf("err=%v started=%v", err, started)
		}
	})

	t.Run("not installed", func(t *testing.T) {
		installed, started := false, false
		err := ensureComputerServiceStarted(
			func() (service.Status, error) { return service.StatusUnknown, service.ErrNotInstalled },
			func() error { installed = true; return nil },
			func() error { started = true; return nil },
		)
		if err != nil || !installed || !started {
			t.Fatalf("err=%v installed=%v started=%v", err, installed, started)
		}
	})

	t.Run("status error", func(t *testing.T) {
		want := errors.New("launchd status failed")
		calls := 0
		err := ensureComputerServiceStarted(
			func() (service.Status, error) { return service.StatusUnknown, want },
			func() error { calls++; return nil },
			func() error { calls++; return nil },
		)
		if err != want || calls != 0 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})

	t.Run("install failure", func(t *testing.T) {
		want := errors.New("install failed")
		err := ensureComputerServiceStarted(
			func() (service.Status, error) { return service.StatusUnknown, service.ErrNotInstalled },
			func() error { return want },
			func() error { t.Fatal("start called after failed install"); return nil },
		)
		if err != want {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("start failure", func(t *testing.T) {
		want := errors.New("start failed")
		err := ensureComputerServiceStarted(
			func() (service.Status, error) { return service.StatusStopped, nil },
			func() error { t.Fatal("installed service reinstalled"); return nil },
			func() error { return want },
		)
		if err != want {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestComputerSetupReusesScheduledTask(t *testing.T) {
	for _, state := range []string{"1", "2", "3", "4", "\r\n4\r\n", "0", "", "Running", "5"} {
		t.Run(state, func(t *testing.T) {
			started := false
			err := ensureComputerServiceStarted(
				func() (service.Status, error) { return userTaskRuntimeStatus(state) },
				func() error { t.Fatal("existing task reinstalled"); return nil },
				func() error { started = true; return nil },
			)
			switch strings.TrimSpace(state) {
			case "1", "3":
				if err != nil || !started {
					t.Fatalf("err=%v started=%v", err, started)
				}
			case "2", "4":
				if err != nil || started {
					t.Fatalf("err=%v started=%v", err, started)
				}
			default:
				if err == nil || started {
					t.Fatalf("err=%v started=%v", err, started)
				}
			}
		})
	}
}

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
