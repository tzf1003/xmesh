package main

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/kardianos/service"
)

func Test_UserTaskRegistration(t *testing.T) {
	cfg := &service.Config{Name: "xmesh-device", Description: "XmeSh 设备 & tools", Executable: `C:\Tools & apps\xmesh.exe`, WorkingDirectory: `C:\工作区\<node>😀`, Arguments: []string{"serve"}}
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
		if len(doc) < 2 || doc[0] != 0xff || doc[1] != 0xfe || len(doc)%2 != 0 {
			t.Fatal("task document must be UTF-16LE with a BOM")
		}
		units := make([]uint16, (len(doc)-2)/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(doc[2+2*i:])
		}
		text := string(utf16.Decode(units))
		if !strings.HasPrefix(text, "<?xml version=\"1.0\" encoding=\"UTF-16\"?>") {
			t.Fatal("task document encoding declaration does not match its bytes")
		}
		decoder := xml.NewDecoder(strings.NewReader(text))
		decoder.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
			if charset != "UTF-16" {
				return nil, errors.New("unexpected task XML encoding declaration")
			}
			// The wire bytes were decoded above; the XML parser receives UTF-8.
			return input, nil
		}
		if err := decoder.Decode(&task); err != nil {
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

func Test_RelocateTaskDocument(t *testing.T) {
	const document = `<?xml version="1.0" encoding="UTF-16"?><Task xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Principals><Principal id="CurrentUser"><UserId>S-1-5-21-1000</UserId><LogonType>InteractiveToken</LogonType></Principal></Principals><Settings><Hidden>true</Hidden><UnknownSetting>preserved</UnknownSetting></Settings><Actions Context="CurrentUser"><Exec><Command>C:\O&apos;Brien &amp; Apps\resources\bin\xmesh.exe</Command><Arguments>serve --config &quot;D:\节点\xmesh.yaml&quot; --state-dir &quot;D:\state dir&quot;</Arguments><WorkingDirectory>C:\O&apos;Brien &amp; Apps\workspace</WorkingDirectory></Exec></Actions></Task>`
	got, err := relocateTaskDocument(document, `C:\O'Brien & Apps`, `D:\new & 中文`)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := parseUserTask(document)
	after, err := parseUserTask(got)
	if err != nil {
		t.Fatal(err)
	}
	if after.Actions.Exec.Command != `D:\new & 中文\resources\bin\xmesh.exe` || after.Actions.Exec.WorkingDirectory != `D:\new & 中文\workspace` {
		t.Fatal("installation paths", after.Actions.Exec)
	}
	if after.Actions.Exec.Arguments != before.Actions.Exec.Arguments || !reflect.DeepEqual(after.Principal, before.Principal) || !strings.Contains(got, "<UnknownSetting>preserved</UnknownSetting>") {
		t.Fatal("task registration was changed", got)
	}
	outside := strings.Replace(document, `C:\O&apos;Brien &amp; Apps\workspace`, `D:\user workspace`, 1)
	got, err = relocateTaskDocument(outside, `C:\O'Brien & Apps`, `D:\new`)
	after, _ = parseUserTask(got)
	if err != nil || after.Actions.Exec.WorkingDirectory != `D:\user workspace` {
		t.Fatal("external working directory", after.Actions.Exec.WorkingDirectory, err)
	}
}

func Test_InstallerUserTaskOperations(t *testing.T) {
	const sid = "S-1-5-21-1000"
	mine, elsewhere, other := userTaskName("xmesh-node", sid), userTaskName("xmesh-other-copy", sid), userTaskName("xmesh-node", "S-1-5-21-2000")
	var listing bytes.Buffer
	writer := csv.NewWriter(&listing)
	_ = writer.WriteAll([][]string{{mine, "ready"}, {elsewhere, "ready"}, {other, "ready"}})
	base := `<Task><Principals><Principal><UserId>` + sid + `</UserId></Principal></Principals><Actions><Exec><Command>C:\old\resources\bin\xmesh.exe</Command><Arguments>serve --state-dir D:\state</Arguments><WorkingDirectory>D:\workspace</WorkingDirectory></Exec></Actions></Task>`
	for _, destination := range []string{"", `D:\new & Apps`} {
		t.Run(destination, func(t *testing.T) {
			var mutations []string
			command := func(args ...string) ([]byte, error) {
				switch args[0] {
				case "/Query":
					if args[1] == "/FO" {
						return listing.Bytes(), nil
					}
					if args[2] == other {
						t.Fatal("queried other account")
					}
					if args[2] == elsewhere {
						return []byte(strings.Replace(base, `C:\old\`, `C:\other\`, 1)), nil
					}
					return []byte(base), nil
				case "/Create":
					raw, err := os.ReadFile(args[4])
					if err != nil {
						t.Fatal(err)
					}
					task, err := parseUserTask(taskDocumentText(raw))
					if err != nil || task.Actions.Exec.Command != destination+`\resources\bin\xmesh.exe` || task.Actions.Exec.Arguments != `serve --state-dir D:\state` || task.Actions.Exec.WorkingDirectory != `D:\workspace` || task.Principal.UserID != sid {
						t.Fatal("migration document", task, err)
					}
				}
				if args[2] != mine {
					t.Fatal("modified another installation", args)
				}
				mutations = append(mutations, args[0])
				return nil, nil
			}
			count, err := updateUserTasksUnder(`C:\old`, destination, sid, command)
			want := []string{"/End", "/Delete"}
			if destination != "" {
				want = []string{"/Create"}
			}
			if err != nil || count != 1 || !reflect.DeepEqual(mutations, want) {
				t.Fatal(count, mutations, err)
			}
		})
	}
	for _, failure := range []string{"list", "inspect", "delete", "register"} {
		t.Run(failure, func(t *testing.T) {
			expected := errors.New("scheduler access failed")
			destination := ""
			if failure == "register" {
				destination = `D:\new`
			}
			_, err := updateUserTasksUnder(`C:\old`, destination, sid, func(args ...string) ([]byte, error) {
				if args[0] == "/Query" && args[1] == "/FO" {
					if failure == "list" {
						return nil, expected
					}
					return listing.Bytes(), nil
				}
				if args[0] == "/Query" {
					if failure == "inspect" {
						return nil, expected
					}
					return []byte(base), nil
				}
				if args[0] == "/Delete" && failure == "delete" || args[0] == "/Create" && failure == "register" {
					return nil, expected
				}
				return nil, nil
			})
			if !errors.Is(err, expected) {
				t.Fatal("scheduler failure must reach installer", err)
			}
		})
	}
}

func Test_TaskDocumentEncodingForMigration(t *testing.T) {
	for _, declaration := range []string{"", `<?xml version="1.0" encoding="UTF-8"?>`, `<?xml version="1.0" encoding="UTF-16"?>`} {
		document := declaration + `<Task><Actions><Exec><Command>C:\中文\xmesh.exe</Command></Exec></Actions></Task>`
		err := withTaskDocument(document, func(path string) error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := taskDocumentText(raw)
			if !strings.HasPrefix(text, `<?xml version="1.0" encoding="UTF-16"?>`) {
				t.Fatal("scheduler document encoding", text)
			}
			program, err := taskProgram(text)
			if err != nil || program != `C:\中文\xmesh.exe` {
				t.Fatal(program, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
