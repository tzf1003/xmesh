package main

import (
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kardianos/service"
)

const taskNamespace = "http://schemas.microsoft.com/windows/2004/02/mit/task"

type userTask struct {
	XMLName     xml.Name `xml:"Task"`
	XMLNS       string   `xml:"xmlns,attr"`
	Version     string   `xml:"version,attr"`
	Description string   `xml:"RegistrationInfo>Description"`
	Trigger     struct {
		Enabled bool   `xml:"Enabled"`
		UserID  string `xml:"UserId"`
	} `xml:"Triggers>LogonTrigger"`
	Principal struct {
		ID        string `xml:"id,attr"`
		UserID    string `xml:"UserId"`
		LogonType string `xml:"LogonType"`
		RunLevel  string `xml:"RunLevel"`
	} `xml:"Principals>Principal"`
	Settings struct {
		MultipleInstances string `xml:"MultipleInstancesPolicy"`
		DisallowBattery   bool   `xml:"DisallowStartIfOnBatteries"`
		StopBattery       bool   `xml:"StopIfGoingOnBatteries"`
		StartAvailable    bool   `xml:"StartWhenAvailable"`
		Enabled           bool   `xml:"Enabled"`
		ExecutionLimit    string `xml:"ExecutionTimeLimit"`
		RestartInterval   string `xml:"RestartOnFailure>Interval"`
		RestartCount      int    `xml:"RestartOnFailure>Count"`
	} `xml:"Settings"`
	Actions struct {
		Context string `xml:"Context,attr"`
		Exec    struct {
			Command          string `xml:"Command"`
			Arguments        string `xml:"Arguments"`
			WorkingDirectory string `xml:"WorkingDirectory"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

// createUserTask registers a current-user logon task. Registration is exclusive:
// schtasks reports an existing task instead of replacing its configuration.
func createUserTask(c *service.Config, sid, arguments string, register func(string, string) error) (string, error) {
	task := userTask{XMLNS: taskNamespace, Version: "1.2", Description: c.Description}
	task.Trigger.Enabled, task.Trigger.UserID = true, sid
	task.Principal.ID, task.Principal.UserID = "CurrentUser", sid
	task.Principal.LogonType, task.Principal.RunLevel = "InteractiveToken", "LeastPrivilege"
	task.Settings.MultipleInstances = "IgnoreNew"
	task.Settings.StartAvailable, task.Settings.Enabled = true, true
	task.Settings.ExecutionLimit = "PT0S"
	task.Settings.RestartInterval, task.Settings.RestartCount = "PT1M", 999
	task.Actions.Context = task.Principal.ID
	task.Actions.Exec.Command, task.Actions.Exec.Arguments = c.Executable, arguments
	task.Actions.Exec.WorkingDirectory = c.WorkingDirectory
	doc, err := xml.MarshalIndent(task, "", "  ")
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "xmesh-user-task-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "task.xml")
	if err := os.WriteFile(path, append([]byte(xml.Header), doc...), 0600); err != nil {
		return "", err
	}
	// Task names are machine-wide; include a stable current-user suffix so
	// separate Windows accounts can install the same node name independently.
	suffix := sha256.Sum256([]byte(sid))
	name := fmt.Sprintf(`\%s-%x`, c.Name, suffix[:6])
	if err := register(name, path); err != nil {
		return "", err
	}
	return name, nil
}
