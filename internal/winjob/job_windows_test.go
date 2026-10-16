package winjob

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The host is killed while its child is still suspended, immediately after
// CreateProcess returns. No subsequent assignment or cleanup code can run.
func Test_WindowsJobAtomicAdmission(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_EXEC") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_EXEC=1 on Windows")
	}
	for _, mode := range []string{"bound", "control"} {
		t.Run(mode, func(t *testing.T) { windowsJobHostCrash(t, mode) })
	}
}

func windowsJobHostCrash(t *testing.T, mode string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	proof := filepath.Join(t.TempDir(), "created.json")
	command := exec.Command(executable, "-test.run=^Test_WindowsJobHost$", "--", proof)
	command.Env = append(os.Environ(), "XMESH_WINJOB_HOST=1", "XMESH_WINJOB_MODE="+mode)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	hostDone := make(chan error, 1)
	go func() { hostDone <- command.Wait() }()
	waited := false
	t.Cleanup(func() {
		_ = command.Process.Kill()
		if !waited {
			<-hostDone
		}
	})
	var pid uint32
	for deadline := time.Now().Add(15 * time.Second); ; {
		body, err := os.ReadFile(proof)
		if err == nil && json.Unmarshal(body, &pid) == nil && pid != 0 {
			break
		}
		select {
		case err := <-hostDone:
			waited = true
			t.Fatal("Job host exited before publishing child", err, output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("host did not publish its suspended child")
		}
		time.Sleep(10 * time.Millisecond)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.TerminateProcess(child, 1); _ = windows.CloseHandle(child) })
	if status, err := windows.WaitForSingleObject(child, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("suspended child is not alive", status, err)
	}
	if _, err := os.Stat(proof + ".ran"); !os.IsNotExist(err) {
		t.Fatal("child ran before admission completed", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-hostDone
	waited = true
	if mode == "control" {
		if status, err := windows.WaitForSingleObject(child, 200); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("control child did not survive its host", status, err, output.String())
		}
		return
	}
	if status, err := windows.WaitForSingleObject(child, 10000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("child survived hosting process termination", status, err, output.String())
	}
}

func Test_WindowsJobHost(t *testing.T) {
	if os.Getenv("XMESH_WINJOB_HOST") != "1" {
		t.Skip("Job host helper")
	}
	job, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	if os.Getenv("XMESH_WINJOB_MODE") == "bound" {
		if err := Apply(attributes, job); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	proof := os.Args[len(os.Args)-1]
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{executable, "-test.run=^Test_WindowsJobChild$", "--", proof + ".ran"}))
	if err != nil {
		t.Fatal(err)
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attributes.List()}
	var process windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(nil, line, nil, nil, false, flags, nil, nil, &startup.StartupInfo, &process); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	body, _ := json.Marshal(process.ProcessId)
	if err := os.WriteFile(proof, body, 0600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func Test_WindowsJobChild(t *testing.T) {
	if os.Getenv("XMESH_WINJOB_HOST") != "1" {
		t.Skip("Job child helper")
	}
	if err := os.WriteFile(os.Args[len(os.Args)-1], []byte("ran"), 0600); err != nil {
		t.Fatal(err)
	}
}
