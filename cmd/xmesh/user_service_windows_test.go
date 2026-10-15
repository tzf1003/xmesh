package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
)

type taskRuntimeProof struct {
	SID     string
	Session uint32
	CWD     string
	Args    []string
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
	if got.SID != user.User.Sid.String() || got.Session != session || !strings.EqualFold(got.CWD, workspace) || !reflect.DeepEqual(got.Args, wantArgs) {
		t.Fatal("task identity, session, cwd or arguments changed", got)
	}
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
	doc, err := json.Marshal(taskRuntimeProof{SID: user.User.Sid.String(), Session: session, CWD: workingDirectory, Args: os.Args[index+2:]})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Args[index+1], doc, 0600); err != nil {
		t.Fatal(err)
	}
}
