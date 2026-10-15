package main

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kardianos/service"
)

func Test_UserTaskRegistration(t *testing.T) {
	cfg := &service.Config{Name: "xmesh-device", Description: "XmeSh 设备 & tools", Executable: `C:\Tools & apps\xmesh.exe`, WorkingDirectory: `C:\工作区\<node>`, Arguments: []string{"serve"}}
	arguments := `serve --config "C:\Work & apps\xmesh.yaml" --state-dir "C:\工作区\state"`
	var temporary string
	register := func(name, path string) error {
		temporary = filepath.Dir(path)
		if !strings.HasPrefix(name, `\xmesh-device-`) {
			t.Fatal(name)
		}
		doc, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var task userTask
		if err := xml.Unmarshal(doc, &task); err != nil {
			t.Fatal(err)
		}
		if task.XMLName.Space != taskNamespace || task.Trigger.UserID != "S-1-5-21-1000" || !task.Trigger.Enabled || task.Principal.UserID != task.Trigger.UserID || task.Principal.LogonType != "InteractiveToken" || task.Principal.RunLevel != "LeastPrivilege" {
			t.Fatal("task account/session contract", string(doc))
		}
		if task.Actions.Context != task.Principal.ID || task.Actions.Exec.Command != cfg.Executable || task.Actions.Exec.Arguments != arguments || task.Actions.Exec.WorkingDirectory != cfg.WorkingDirectory || task.Description != cfg.Description {
			t.Fatal("action escaped or shell substituted", string(doc))
		}
		if task.Settings.ExecutionLimit != "PT0S" || task.Settings.MultipleInstances != "IgnoreNew" || task.Settings.DisallowBattery || task.Settings.StopBattery || task.Settings.RestartInterval != "PT1M" || task.Settings.RestartCount != 999 {
			t.Fatal("long-running process policy", string(doc))
		}
		return nil
	}
	name, err := createUserTask(cfg, "S-1-5-21-1000", arguments, register)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("temporary XML retained", err)
	}
	other, err := createUserTask(cfg, "S-1-5-21-2000", arguments, func(string, string) error { return nil })
	if err != nil || other == name {
		t.Fatal("accounts shared task name", name, other, err)
	}
	expected := errors.New("scheduler rejected task")
	if _, err := createUserTask(cfg, "S-1-5-21-1000", arguments, func(_ string, path string) error {
		temporary = filepath.Dir(path)
		return expected
	}); !errors.Is(err, expected) {
		t.Fatal("registration error lost", err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("failed registration retained XML", err)
	}
}
