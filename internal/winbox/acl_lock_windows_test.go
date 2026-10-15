package winbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func aclLockFileIdentity(t *testing.T, path string) fileIdentity {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	object, err := inspectTreeObject(windows.Handle(f.Fd()), "")
	if err != nil {
		t.Fatal(err)
	}
	return object.id
}

func aclLockHelperCommand(t *testing.T, path, report, mode string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^Test_WindowsAppContainerACLMutexHelper$")
	cmd.Env = append(os.Environ(), "XMESH_ACL_LOCK_MODE="+mode, "XMESH_ACL_LOCK_PATH="+path, "XMESH_ACL_LOCK_REPORT="+report)
	return cmd
}

func Test_WindowsAppContainerACLMutex(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	path := filepath.Join(t.TempDir(), "selected.txt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	id := aclLockFileIdentity(t, path)
	lock, err := acquireObjectACL(id, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	moved := path + ".moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(filepath.Dir(path), "report")
	for _, test := range []struct{ path, mode string }{{moved, "timeout"}, {path, "acquire"}} {
		if output, err := aclLockHelperCommand(t, test.path, report, test.mode).CombinedOutput(); err != nil {
			t.Fatalf("mutex helper %s: %v %s", test.mode, err, output)
		}
	}
	lock.Close()
	if output, err := aclLockHelperCommand(t, moved, report, "acquire").CombinedOutput(); err != nil {
		t.Fatalf("mutex acquisition after release: %v %s", err, output)
	}
	// Retain the named object while a child exits holding ownership, so the
	// next owner observes WAIT_ABANDONED rather than a newly created mutex.
	h, err := openObjectACLMutex(id)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if output, err := aclLockHelperCommand(t, moved, report, "abandon").CombinedOutput(); err != nil {
		t.Fatalf("abandoned mutex helper: %v %s", err, output)
	}
	lock, err = acquireObjectACL(id, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if !lock.abandoned {
		t.Fatal("child owner exit was not reported as abandoned")
	}
}

func Test_WindowsAppContainerACLConcurrentProcesses(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	path := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	before := readObjectACL(t, path)
	type child struct {
		command *exec.Cmd
		report  string
		output  bytes.Buffer
		waited  bool
	}
	children := make([]*child, 3)
	for index := range children {
		c := &child{report: filepath.Join(filepath.Dir(path), "ready-"+string(rune('a'+index)))}
		c.command = aclLockHelperCommand(t, path, c.report, "grant")
		c.command.Stdout, c.command.Stderr = &c.output, &c.output
		if err := c.command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if !c.waited {
				_ = os.WriteFile(c.report+".release", nil, 0600)
				_ = c.command.Wait()
			}
		})
		children[index] = c
	}
	deadline := time.Now().Add(20 * time.Second)
	sids := make([][]byte, len(children))
	for index, c := range children {
		for {
			body, err := os.ReadFile(c.report)
			if err == nil && json.Unmarshal(body, &sids[index]) == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("concurrent ACL grant readiness timed out", index)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	acl := readObjectACL(t, path)
	_, aces, err := aclParts((*windows.ACL)(unsafe.Pointer(&acl[0])))
	if err != nil {
		t.Fatal(err)
	}
	for _, sid := range sids {
		matches := 0
		for _, ace := range aces {
			if len(ace) >= 8 && ace[0] == windows.ACCESS_ALLOWED_ACE_TYPE && bytes.Equal(ace[8:], sid) {
				matches++
			}
		}
		if matches != 1 {
			t.Fatal("concurrent process grant lost or duplicated", matches)
		}
	}
	for _, c := range children {
		if err := os.WriteFile(c.report+".release", nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range children {
		err := c.command.Wait()
		c.waited = true
		if err != nil {
			t.Fatalf("concurrent grant/release: %v %s", err, c.output.String())
		}
	}
	if !bytes.Equal(before, readObjectACL(t, path)) {
		t.Fatal("concurrent process release did not restore the original ACL")
	}
}

func Test_WindowsAppContainerACLMutexHelper(t *testing.T) {
	mode := os.Getenv("XMESH_ACL_LOCK_MODE")
	if mode == "" {
		t.Skip("cross-process ACL coordination helper")
	}
	path, report := os.Getenv("XMESH_ACL_LOCK_PATH"), os.Getenv("XMESH_ACL_LOCK_REPORT")
	if mode == "grant" {
		p, err := New()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := p.Close(); err != nil {
				t.Error(err)
			}
		}()
		grant, err := p.grantObject(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := grant.Close(); err != nil {
				t.Error(err)
			}
		}()
		sid := unsafe.Slice((*byte)(unsafe.Pointer(p.sid)), p.sid.Len())
		body, err := json.Marshal(sid)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, body, 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(25 * time.Second)
		for {
			if _, err := os.Stat(report + ".release"); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("concurrent ACL grant release timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	lock, err := acquireObjectACL(aclLockFileIdentity(t, path), 100)
	if mode == "timeout" {
		if !errors.Is(err, errObjectACLTimeout) {
			if lock != nil {
				lock.Close()
			}
			t.Fatal("cross-process contention did not time out", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == "abandon" {
		os.Exit(0)
	}
	lock.Close()
}
