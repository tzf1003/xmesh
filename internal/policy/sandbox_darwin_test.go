package policy

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/tzf1003/xmesh/internal/config"
)

func Test_L2SeatbeltRuntime(t *testing.T) {
	if os.Getenv("XMESH_TEST_L2") != "1" {
		t.Skip("set XMESH_TEST_L2=1 on macOS with sandbox-exec")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, `workspace" (allow network*)`)
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err = os.WriteFile(outside, []byte("host-only"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	helper := filepath.Join(workspace, "probe")
	out, err := os.OpenFile(helper, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	engine, _ := New(config.Policy{Isolation: "L2"})
	env, err := engine.Environment(nil)
	if err != nil {
		t.Fatal(err)
	}
	launch, args, env, err := engine.Sandbox(helper, []string{"-test.run=^Test_L2SeatbeltHelper$", "--", outside, listener.Addr().String(), strconv.Itoa(os.Getpid())}, workspace, env)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(launch, args...)
	cmd.Dir = workspace
	cmd.Env = append(env, "XMESH_L2_HELPER=1")
	data, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(data), "ISOLATED") {
		t.Fatalf("seatbelt: %v %s", err, data)
	}
	if data, err = os.ReadFile(outside); err != nil || string(data) != "host-only" {
		t.Fatal("outside file changed", err)
	}
	if data, err = os.ReadFile(filepath.Join(workspace, "created")); err != nil || string(data) != "workspace" {
		t.Fatal("workspace write", err)
	}
}

func Test_L2SeatbeltHelper(t *testing.T) {
	if os.Getenv("XMESH_L2_HELPER") != "1" {
		return
	}
	args := os.Args[len(os.Args)-3:]
	fail := func(reason string) { fmt.Fprintln(os.Stderr, reason); os.Exit(1) }
	if _, err := os.ReadFile(args[0]); err == nil {
		fail("outside file readable")
	}
	if err := os.WriteFile(args[0], []byte("blocked"), 0600); err == nil {
		fail("outside file writable")
	}
	if err := os.WriteFile("escape", []byte("blocked"), 0600); err == nil {
		fail("symlink escaped workspace")
	}
	if conn, err := net.DialTimeout("tcp", args[1], time.Second); err == nil {
		conn.Close()
		fail("network permitted")
	}
	pid, _ := strconv.Atoi(args[2])
	if err := syscall.Kill(pid, 0); err == nil {
		fail("signal to parent permitted")
	}
	// KERN_PROCARGS2 exposes argv and environment for a same-user host process.
	mib := [3]int32{1, 49, int32(pid)}
	buffer := make([]byte, 1<<20)
	size := uintptr(len(buffer))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 3, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
	if errno != syscall.EPERM && errno != syscall.EACCES {
		fail("host process environment query was not denied")
	}
	cwd, err := os.Getwd()
	if err != nil || os.Getenv("HOME") != cwd || os.Getenv("TMPDIR") != cwd {
		fail("sandbox environment")
	}
	if err := os.WriteFile("created", []byte("workspace"), 0600); err != nil {
		fail("workspace write denied")
	}
	fmt.Println("ISOLATED")
	os.Exit(0)
}
