package policy

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// Rule CON-SEC-12 (its matrix entry arrives with the control contract): an L2 workspace may not expose the control channel's socket
func Test_CONSEC12_L2WorkspaceMustNotContainControlSocket(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	for _, dir := range []string{state, filepath.Join(root, "work")} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(state, "control.sock")
	if err = credentialWorkspace(root); err != nil {
		t.Fatalf("nothing is protected yet: %v", err)
	}
	release := ProtectPath(socket)
	for _, workspace := range []string{root, state, socket} {
		var denied *toolerr.Error
		err = credentialWorkspace(workspace)
		if e, ok := err.(*toolerr.Error); ok {
			denied = e
		}
		if denied == nil || denied.Fields["rule"] != "sandbox.control_channel" {
			t.Errorf("%s: %v", workspace, err)
		}
	}
	if err = credentialWorkspace(filepath.Join(root, "work")); err != nil {
		t.Errorf("a separate workspace was refused: %v", err)
	}
	release()
	if err = credentialWorkspace(root); err != nil {
		t.Errorf("still protected after release: %v", err)
	}
}

// Reference counting: two nodes in one process protect and release independently.
func Test_ProtectPathReferenceCounted(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(root, "control.sock")
	a, b := ProtectPath(path), ProtectPath(path)
	a()
	if protectedWorkspace(root) == nil {
		t.Fatal("released while still held")
	}
	b()
	if err := protectedWorkspace(root); err != nil {
		t.Fatal(err)
	}
}

// Test_L2CannotConnectToControlSocket runs a sandboxed process that tries to
// connect to a live control socket outside its workspace. Needs a host with
// bubblewrap (Linux) or sandbox-exec (macOS).
func Test_L2CannotConnectToControlSocket(t *testing.T) {
	if socket := os.Getenv("XMESH_TEST_CONTROL_SOCKET"); socket != "" {
		// Helper process, running inside the sandbox. The sandbox environment does
		// not carry XMESH_TEST_L2, so this must come before the skip below; with
		// the order reversed the helper skipped and the parent saw a clean exit.
		c, err := net.Dial("unix", socket)
		if err == nil {
			c.Close()
			os.Exit(0)
		}
		os.Exit(3)
	}
	if os.Getenv("XMESH_TEST_L2") != "1" || (runtime.GOOS != "linux" && runtime.GOOS != "darwin") {
		t.Skip("set XMESH_TEST_L2=1 on Linux with bubblewrap or macOS with sandbox-exec")
	}
	// A unix socket path is limited to about 100 bytes and the macOS test
	// temp directory is longer than that, so use a short one.
	short, err := os.MkdirTemp("/tmp", "xl2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	root, err := filepath.EvalSymlinks(short)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "work")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "control.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := l.Accept(); err == nil {
			c.Close()
			accepted <- struct{}{}
		}
	}()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(workspace, "helper")
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(helper, data, 0700); err != nil {
		t.Fatal(err)
	}
	engine, _ := New(config.Policy{Isolation: "L2"})
	env, err := engine.Environment(nil)
	if err != nil {
		t.Fatal(err)
	}
	env = append(env, "XMESH_TEST_CONTROL_SOCKET="+socket)
	launch, args, env, err := engine.Sandbox(helper, []string{"-test.run=^Test_L2CannotConnectToControlSocket$"}, workspace, env)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(launch, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 3 {
		t.Fatalf("sandboxed process was not refused (exit %v): %s", err, out)
	}
	select {
	case <-accepted:
		t.Fatal("the control socket accepted a sandboxed connection")
	default:
	}
}
