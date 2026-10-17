package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

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

// userTaskName is the scheduled task name for a service installed by one user.
func userTaskName(service, sid string) string {
	suffix := sha256.Sum256([]byte(sid))
	return fmt.Sprintf(`\%s-%x`, service, suffix[:6])
}

// isUserTaskOf reports whether name is a logon task that userTaskName makes for sid. The names end
// in a hash of the SID, so this matches the tasks of that user for every node and no one else's.
func isUserTaskOf(name, sid string) bool {
	suffix := sha256.Sum256([]byte(sid))
	tail := fmt.Sprintf("-%x", suffix[:6])
	const prefix = `\xmesh-`
	return strings.HasPrefix(name, prefix) && len(name) > len(prefix)+len(tail) && strings.HasSuffix(name, tail)
}

// programUnder reports whether program, a path to an executable, lies inside dir.
func programUnder(program, dir string) bool {
	dir = strings.TrimRight(dir, `\/`) + `\`
	return len(program) > len(dir) && strings.EqualFold(program[:len(dir)], dir)
}

// taskDocumentText turns the output of schtasks /Query /XML into text. Task documents are UTF-16
// when they start with a byte order mark or carry NUL bytes; anything else is taken as it is.
func taskDocumentText(raw []byte) string {
	switch {
	case len(raw) >= 2 && raw[0] == 0xff && raw[1] == 0xfe:
		raw = raw[2:]
	case bytes.IndexByte(raw, 0) < 0:
		return string(raw)
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(raw[i:]))
	}
	return string(utf16.Decode(units))
}

// taskProgram is the executable a task document runs.
func taskProgram(document string) (string, error) {
	var task userTask
	decoder := xml.NewDecoder(strings.NewReader(document))
	// The declaration may say UTF-16; the text is already decoded, so take it as it is.
	decoder.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := decoder.Decode(&task); err != nil {
		return "", err
	}
	return task.Actions.Exec.Command, nil
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
	// schtasks reads task documents as UTF-16. Keep its declaration, BOM and
	// actual code units consistent, including non-ASCII paths and arguments.
	units := utf16.Encode([]rune("<?xml version=\"1.0\" encoding=\"UTF-16\"?>\r\n" + string(doc)))
	encoded := make([]byte, 2+2*len(units))
	encoded[0], encoded[1] = 0xff, 0xfe
	for i, unit := range units {
		binary.LittleEndian.PutUint16(encoded[2+2*i:], unit)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		return "", err
	}
	// Task names are machine-wide; include a stable current-user suffix so
	// separate Windows accounts can install the same node name independently.
	name := userTaskName(c.Name, sid)
	if err := register(name, path); err != nil {
		return "", err
	}
	return name, nil
}
