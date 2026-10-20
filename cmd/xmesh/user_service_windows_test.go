package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/control"
	"golang.org/x/sys/windows"
)

// Exercise the production CLI, not just a task that runs a test helper: legacy
// configurations must fail startup, and migrated kernels must answer control.
func Test_WindowsUserKernelStart(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_USER_SERVICE") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_USER_SERVICE=1 in an interactive Windows session")
	}
	binary := os.Getenv("XMESH_TEST_WINDOWS_BINARY")
	if !filepath.IsAbs(binary) {
		t.Skip("XMESH_TEST_WINDOWS_BINARY must name the built xmesh executable")
	}
	root := t.TempDir()
	node := fmt.Sprintf("start-validation-%d", time.Now().UnixNano())
	configFile := filepath.Join(root, "config.json")
	stateDir := filepath.Join(root, "state")
	if err := os.WriteFile(configFile, serviceFixture(t, node, root), 0600); err != nil {
		t.Fatal(err)
	}
	if err := install([]string{"--config", configFile, "--state-dir", stateDir, "--executable", binary, "--autostart=false"}); err != nil {
		t.Fatal(err)
	}
	args := []string{"--name", serviceName(node)}
	t.Cleanup(func() {
		// Stop removes control.json before Task Scheduler releases the process.
		// Retain its identity before stopping so TempDir cleanup cannot race it.
		ep, endpointErr := control.ReadEndpoint(stateDir)
		_ = serviceCommand("stop", args)
		if endpointErr == nil {
			if err := waitKernelExit(ep, kernelExitLimit); err != nil {
				t.Error(err)
			}
		}
		if err := serviceCommand("uninstall", args); err != nil {
			t.Error(err)
		}
		// Task Scheduler can briefly retain the action's working directory
		// after the kernel exits and the task has been deleted.
		for deadline := time.Now().Add(10 * time.Second); ; {
			err := os.RemoveAll(root)
			if err == nil {
				break
			}
			if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || time.Now().After(deadline) {
				t.Error("task working directory cleanup", err)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	if err := serviceCommand("start", args); err == nil {
		t.Fatal("Task Scheduler accepted a legacy config, but the kernel never started")
	}
	if err := migrate([]string{"--config", configFile, "--state-dir", stateDir}); err != nil {
		t.Fatal(err)
	}
	if err := serviceCommand("start", args); err != nil {
		t.Fatal("migrated kernel failed startup acceptance", err)
	}
	if err := serviceCommand("start", args); err != nil {
		t.Fatal("starting an already running kernel must remain idempotent", err)
	}
}

type taskRuntimeProof struct {
	SID           string
	Session       uint32
	CWD           string
	Args          []string
	Elevated      bool
	ElevationType uint32
}

// This opt-in test creates, runs and deletes a disposable current-user task.
// Run it from the Windows account/session intended to host GUI backends.
func Test_WindowsUserTaskRuntime(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_USER_SERVICE") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_USER_SERVICE=1 in an interactive Windows session")
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		t.Fatal("interactive Windows session required", session, err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	parentElevated, parentType, err := taskTokenElevation(windows.GetCurrentProcessToken())
	if err != nil {
		t.Fatal(err)
	}
	// Windows ignores RunLevel for built-in Administrator and when UAC is
	// disabled. A token with no linked token cannot be lowered by the task.
	const elevationDefault, elevationLimited = 1, 3
	wantElevated, wantType := false, uint32(elevationLimited)
	if parentType == elevationDefault {
		wantElevated, wantType = parentElevated, elevationDefault
	}
	if wantElevated {
		t.Log("account has an elevated token with no linked limited token; non-elevated task acceptance requires a standard or UAC-filtered account")
	}
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "space & 中文")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	proof := filepath.Join(workspace, "proof.json")
	wantArgs := []string{`value "quoted"`, `trailing slash\`, "", "& < > 中文"}
	args := append([]string{"-test.run=^Test_WindowsUserTaskHelper$", "--", "xmesh-user-task-helper", proof}, wantArgs...)
	cfg := &service.Config{Name: fmt.Sprintf("xmesh-validation-%d", time.Now().UnixNano()), Executable: executable, WorkingDirectory: workspace}
	name, err := createUserTask(cfg, user.User.Sid.String(), windows.ComposeCommandLine(args), func(name, path string) error {
		_, err := taskCommand(directory, "/Create", "/TN", name, "/XML", path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = taskCommand(directory, "/End", "/TN", name)
		if _, err := taskCommand(directory, "/Delete", "/TN", name, "/F"); err != nil {
			t.Error("task cleanup", err)
		}
	})
	if _, err := taskCommand(directory, "/Run", "/TN", name); err != nil {
		t.Fatal(err)
	}
	var got taskRuntimeProof
	for deadline := time.Now().Add(20 * time.Second); ; {
		data, err := os.ReadFile(proof)
		if err == nil && json.Unmarshal(data, &got) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduled task did not produce runtime proof", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got.SID != user.User.Sid.String() || got.Session != session || got.Elevated != wantElevated || got.ElevationType != wantType || !strings.EqualFold(got.CWD, workspace) || !reflect.DeepEqual(got.Args, wantArgs) {
		t.Fatal("task identity, session, privilege, cwd or arguments changed", got)
	}
	t.Logf("task runtime verified: session=%d elevated=%t elevation_type=%d", got.Session, got.Elevated, got.ElevationType)
}

func Test_WindowsUserTaskHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "xmesh-user-task-helper" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Skip("scheduled task helper")
	}
	if len(os.Args) <= index+1 {
		t.Fatal("missing proof path")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	elevated, elevationType, err := taskTokenElevation(windows.GetCurrentProcessToken())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(taskRuntimeProof{SID: user.User.Sid.String(), Session: session, CWD: workingDirectory, Args: os.Args[index+2:], Elevated: elevated, ElevationType: elevationType})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Args[index+1], doc, 0600); err != nil {
		t.Fatal(err)
	}
}

func taskTokenElevation(token windows.Token) (bool, uint32, error) {
	var elevated, kind, size uint32
	if err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &size); err != nil {
		return false, 0, err
	}
	if err := windows.GetTokenInformation(token, windows.TokenElevationType, (*byte)(unsafe.Pointer(&kind)), uint32(unsafe.Sizeof(kind)), &size); err != nil {
		return false, 0, err
	}
	if kind < 1 || kind > 3 {
		return false, 0, fmt.Errorf("unknown token elevation type %d", kind)
	}
	return elevated != 0, kind, nil
}
