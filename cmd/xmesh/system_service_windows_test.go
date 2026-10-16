package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kardianos/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// This opt-in test installs the built CLI as a disposable LocalSystem service.
// The runtime uses explicit loopback development mode and an isolated workspace.
func Test_WindowsSystemServiceRuntime(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SYSTEM_SERVICE") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SYSTEM_SERVICE=1 on an elevated Windows host")
	}
	binary := os.Getenv("XMESH_TEST_WINDOWS_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("XMESH_TEST_WINDOWS_BINARY must name the built xmesh executable")
	}
	root := filepath.Join(t.TempDir(), "service space & 中文")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("scm-validation-%d", time.Now().UnixNano())
	serviceName := "xmesh-" + name
	configFile := filepath.Join(root, "config & 中文.json")
	stateDir := filepath.Join(root, "state 中文")
	body := serviceFixture(t, name, root)
	if err := os.WriteFile(configFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	controller, err := service.New(&program{}, &service.Config{Name: serviceName})
	if err != nil {
		manager.Disconnect()
		t.Fatal(err)
	}
	var registered *mgr.Service
	var ownedProcess windows.Handle
	installedService := false
	t.Cleanup(func() {
		defer manager.Disconnect()
		if registered != nil {
			_, _ = registered.Control(svc.Stop)
			if _, err := waitSCMState(registered, svc.Stopped, 30*time.Second); err != nil {
				t.Error("service cleanup stop", err)
			}
			registered.Close()
		}
		if ownedProcess != 0 {
			if event, err := windows.WaitForSingleObject(ownedProcess, 5000); err != nil || event != windows.WAIT_OBJECT_0 {
				t.Error("service cleanup process retained", event, err)
				_ = windows.TerminateProcess(ownedProcess, 1)
				_, _ = windows.WaitForSingleObject(ownedProcess, 5000)
			}
			windows.CloseHandle(ownedProcess)
		}
		if !installedService {
			return
		}
		if err := controller.Uninstall(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			t.Error("service cleanup uninstall", err)
		}
		for deadline := time.Now().Add(5 * time.Second); ; {
			s, err := manager.OpenService(serviceName)
			if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
				break
			}
			if s != nil {
				s.Close()
			}
			if time.Now().After(deadline) {
				t.Error("service registration retained", err)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	if err := run([]string{"install", "--system", "--executable", binary, "--config", configFile, "--state-dir", stateDir}); err != nil {
		t.Fatal("SCM installation", err)
	}
	installedService = true
	registered, err = manager.OpenService(serviceName)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := registered.Config()
	if err != nil {
		t.Fatal(err)
	}
	args, err := windows.DecomposeCommandLine(installed.BinaryPathName)
	wantArgs := []string{binary, "serve", "--service", "--config", configFile, "--state-dir", stateDir}
	if err != nil || !reflect.DeepEqual(args, wantArgs) || installed.StartType != mgr.StartAutomatic || !strings.EqualFold(installed.ServiceStartName, "LocalSystem") {
		t.Fatal("installed SCM configuration", installed, args, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	// Keep networking within the disposable test host while running the actual
	// serve --service entry point and the same installed configuration paths.
	installed.BinaryPathName = windows.ComposeCommandLine(append(args, "--local", "--listen", address))
	if err := registered.UpdateConfig(installed); err != nil {
		t.Fatal(err)
	}
	if err := registered.Start(); err != nil {
		t.Fatal("SCM start", err)
	}
	running, err := waitSCMState(registered, svc.Running, 15*time.Second)
	if err != nil || running.ProcessId == 0 {
		t.Fatal("SCM running state", running, err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, running.ProcessId)
	if err != nil {
		t.Fatal(err)
	}
	ownedProcess = process
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var sessionID uint32
	if err := windows.ProcessIdToSessionId(running.ProcessId, &sessionID); err != nil || sessionID != 0 || user.User.Sid.String() != "S-1-5-18" {
		t.Fatal("SCM account/session", user.User.Sid.String(), sessionID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var client *mcp.ClientSession
	for {
		attempt, stop := context.WithTimeout(ctx, 2*time.Second)
		client, err = mcp.NewClient(&mcp.Implementation{Name: "scm-validation", Version: "1"}, nil).Connect(attempt, &mcp.StreamableClientTransport{Endpoint: "http://" + address + "/mcp"}, nil)
		stop()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("SCM MCP readiness", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer client.Close()
	info := scmTool(t, ctx, client, "sys.info", map[string]any{})
	if info["hostname"] != name || info["os"] != "windows" {
		t.Fatal("SCM runtime identity", info)
	}
	scmTool(t, ctx, client, "fs.write", map[string]any{"path": "service-proof.txt", "content": "SCM runtime proof"})
	proof, err := os.ReadFile(filepath.Join(root, "service-proof.txt"))
	if err != nil || string(proof) != "SCM runtime proof" {
		t.Fatal("SCM file side effect", string(proof), err)
	}
	read := scmTool(t, ctx, client, "fs.read", map[string]any{"path": "service-proof.txt"})
	content, _ := read["content"].(string)
	if !strings.Contains(content, "SCM runtime proof") {
		t.Fatal("SCM file readback", read)
	}
	client.Close()
	if _, err := registered.Control(svc.Stop); err != nil {
		t.Fatal("SCM stop", err)
	}
	stopped, err := waitSCMState(registered, svc.Stopped, 30*time.Second)
	if err != nil || stopped.Win32ExitCode != 0 || stopped.ServiceSpecificExitCode != 0 {
		t.Fatal("SCM requested stop", stopped, err)
	}
	if event, err := windows.WaitForSingleObject(process, 10000); err != nil || event != windows.WAIT_OBJECT_0 {
		t.Fatal("stopped service process retained", event, err)
	}
	connection, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("stopped service retained MCP listener")
	}
}

func waitSCMState(s *mgr.Service, state svc.State, timeout time.Duration) (svc.Status, error) {
	for deadline := time.Now().Add(timeout); ; {
		status, err := s.Query()
		if err != nil || status.State == state {
			return status, err
		}
		if time.Now().After(deadline) {
			return status, fmt.Errorf("SCM state %d, want %d", status.State, state)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func scmTool(t *testing.T, ctx context.Context, client *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || out == nil || out.IsError {
		t.Fatal("SCM tool call", name, out, err)
	}
	body, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	return object
}
