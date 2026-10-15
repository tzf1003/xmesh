package winbox

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type isolationProof struct {
	AppContainer          bool
	OutsideRead           bool
	OutsideWrite          bool
	ParentRead            bool
	Network               bool
	NetworkError          string
	NetworkReason         uint32
	NetworkDiagnosisError string
	TokenError            string
	Capabilities          uint32
	ParentSecret          bool
}

// This opt-in test exercises the Windows kernel boundary using a fresh profile.
// Workspace binding and exec/ConPTY integration have separate acceptance gates.
func Test_WindowsAppContainerIsolation(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMESH_WINBOX_PARENT_SECRET", "sentinel")
	var process windows.ProcessInformation
	t.Cleanup(func() {
		if process.Process != 0 {
			_ = windows.TerminateProcess(process.Process, 1)
			_, _ = windows.WaitForSingleObject(process.Process, 10000)
			windows.CloseHandle(process.Process)
		}
		if process.Thread != 0 {
			windows.CloseHandle(process.Thread)
		}
		if err := p.Close(); err != nil {
			t.Error("AppContainer cleanup", err)
		}
		if err := p.Close(); err != nil {
			t.Error("repeated AppContainer cleanup", err)
		}
		if _, err := os.Stat(p.Directory()); !os.IsNotExist(err) {
			t.Error("AppContainer directory retained", err)
		}
	})
	workspace := filepath.Join(p.Directory(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	helper := filepath.Join(workspace, "helper.exe")
	out, err := os.Create(helper)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	outside := filepath.Join(t.TempDir(), "credential-sentinel")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Confirm the listener is reachable outside the AppContainer.
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	accepted.Close()
	if reason, err := networkIsolationReason("127.0.0.1"); err != nil || reason != 0 {
		t.Fatal("uncontained network capability control", reason, err)
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	if err := p.Apply(attrs); err != nil {
		t.Fatal(err)
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attrs.List()}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{helper, "-test.run=^Test_WindowsAppContainerHelper$", "--", outside, listener.Addr().String(), strconv.Itoa(os.Getpid())}))
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := windows.UTF16PtrFromString(workspace)
	env, err := Environment([]string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_WINBOX_HELPER=1"})
	if err != nil {
		t.Fatal(err)
	}
	environment := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(nil, command, nil, nil, false, flags, &environment[0], dir, &startup.StartupInfo, &process); err != nil {
		t.Fatal("AppContainer process creation", err)
	}
	if result, err := windows.WaitForSingleObject(process.Process, 30000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("AppContainer process wait", result, err)
	}
	var exit uint32
	if err := windows.GetExitCodeProcess(process.Process, &exit); err != nil || exit != 0 {
		t.Fatal("AppContainer helper exit", exit, err)
	}
	body, err := os.ReadFile(filepath.Join(workspace, "proof.json"))
	if err != nil {
		t.Fatal("AppContainer workspace write", err)
	}
	var proof isolationProof
	if err := json.Unmarshal(body, &proof); err != nil {
		t.Fatal(err)
	}
	if !proof.AppContainer || proof.Capabilities != 0 || proof.TokenError != "" || proof.OutsideRead || proof.OutsideWrite || proof.ParentRead || proof.Network || proof.ParentSecret {
		t.Fatal("AppContainer isolation", proof)
	}
	// WFP can silently drop an AppContainer connection. A transport timeout is
	// accepted only with Windows' explicit missing-capability diagnosis, a live
	// listener control on both sides, and no accepted sandbox connection.
	if proof.NetworkDiagnosisError != "" || (proof.NetworkError != "access_denied" && (proof.NetworkReason < 1 || proof.NetworkReason > 3)) {
		t.Fatal("network failure has no Windows access-control evidence", proof)
	}
	tcp := listener.(*net.TCPListener)
	if err := tcp.SetDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if connection, err := tcp.Accept(); err == nil {
		connection.Close()
		t.Fatal("listener accepted an AppContainer connection")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal("listener acceptance probe", err)
	}
	if err := tcp.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	connection, err = net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal("listener unavailable after sandbox probe", err)
	}
	connection.Close()
	t.Logf("AppContainer network: connection=%s missing_capability=%d; host controls succeeded and listener accepted no sandbox connection", proof.NetworkError, proof.NetworkReason)
	if body, err := os.ReadFile(outside); err != nil || string(body) != "private" {
		t.Fatal("outside file modified", string(body), err)
	}
}

func Test_WindowsAppContainerHelper(t *testing.T) {
	if os.Getenv("XMESH_WINBOX_HELPER") != "1" {
		t.Skip("AppContainer helper")
	}
	args := os.Args[len(os.Args)-3:]
	proof := isolationProof{}
	proof.ParentSecret = os.Getenv("XMESH_WINBOX_PARENT_SECRET") != ""
	var isContainer, size uint32
	// TOKEN_INFORMATION_CLASS values introduced after TokenLogonSid are not
	// currently exported by x/sys/windows.
	const tokenIsAppContainer, tokenCapabilities = 29, 30
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), tokenIsAppContainer, (*byte)(unsafe.Pointer(&isContainer)), uint32(unsafe.Sizeof(isContainer)), &size)
	if err != nil {
		proof.TokenError = err.Error()
	}
	proof.AppContainer = isContainer != 0
	var capabilities struct {
		Count   uint32
		Entries [4]windows.SIDAndAttributes
	}
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), tokenCapabilities, (*byte)(unsafe.Pointer(&capabilities)), uint32(unsafe.Sizeof(capabilities)), &size); err != nil {
		proof.TokenError = err.Error()
	}
	proof.Capabilities = capabilities.Count
	_, err = os.ReadFile(args[0])
	proof.OutsideRead = err == nil
	proof.OutsideWrite = os.WriteFile(args[0], []byte("changed"), 0600) == nil
	pid, _ := strconv.ParseUint(args[2], 10, 32)
	parent, err := windows.OpenProcess(windows.PROCESS_VM_READ, false, uint32(pid))
	proof.ParentRead = err == nil
	if err == nil {
		windows.CloseHandle(parent)
	}
	connection, err := net.DialTimeout("tcp", args[1], time.Second)
	proof.Network = err == nil
	if err == nil {
		connection.Close()
	} else if errors.Is(err, windows.WSAEACCES) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		proof.NetworkError = "access_denied"
	} else {
		proof.NetworkError = err.Error()
	}
	host, _, err := net.SplitHostPort(args[1])
	if err == nil {
		proof.NetworkReason, err = networkIsolationReason(host)
	}
	if err != nil {
		proof.NetworkDiagnosisError = err.Error()
	}
	body, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("proof.json", body, 0600); err != nil {
		t.Fatal(err)
	}
}

func networkIsolationReason(host string) (uint32, error) {
	diagnose := windows.NewLazySystemDLL("Firewallapi.dll").NewProc("NetworkIsolationDiagnoseConnectFailureAndGetInfo")
	if err := diagnose.Find(); err != nil {
		return 0, err
	}
	server, err := windows.UTF16PtrFromString(host)
	if err != nil {
		return 0, err
	}
	var reason uint32
	result, _, _ := diagnose.Call(uintptr(unsafe.Pointer(server)), uintptr(unsafe.Pointer(&reason)))
	if result != 0 {
		return 0, syscall.Errno(result)
	}
	return reason, nil
}
