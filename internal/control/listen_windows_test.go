//go:build windows

package control

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"golang.org/x/sys/windows"
)

func Test_CONSEC12_PipeSDDL(t *testing.T) {
	sid, err := CurrentSID()
	if err != nil {
		t.Fatal(err)
	}
	got, err := PipeSDDL(sid, []string{"S-1-5-21-1-2-3-1001", sid})
	if err != nil {
		t.Fatal(err)
	}
	want := "D:P(A;;GA;;;" + sid + ")(A;;GA;;;S-1-5-18)(A;;GA;;;S-1-5-32-544)(A;;GA;;;S-1-5-21-1-2-3-1001)"
	if got != want {
		t.Fatalf("SDDL\n got %s\nwant %s", got, want)
	}
	for _, banned := range []string{"WD", "S-1-1-0", "BU", "AU", "AC", "S-1-15-2-1"} {
		if strings.Contains(got, ";;;"+banned+")") {
			t.Errorf("DACL grants %s", banned)
		}
	}
	// Anything that is not a SID must not reach the descriptor string.
	for _, bad := range []string{"", "WD", "S-1-5-18)(A;;GA;;;WD", "x", "BA", "S-1-1-0", "S-1-5-11", "S-1-15-2-1"} {
		if _, err = PipeSDDL(sid, []string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// The live pipe carries exactly the protected DACL: AppContainer packages and
// Everyone are absent, so sandboxed processes cannot open it.
func Test_CONSEC12_LivePipeDACL(t *testing.T) {
	f := newFixture(t)
	address := f.server.Endpoint().Address()
	if !strings.HasPrefix(address, `\\.\pipe\xmesh-S-1-`) || !strings.HasSuffix(address, "-ctl-node") {
		t.Fatalf("pipe name %s", address)
	}
	name, err := windows.UTF16PtrFromString(address)
	if err != nil {
		t.Fatal(err)
	}
	// A pipe whose listening instance is between two accepts answers PIPE_BUSY for a moment.
	var h windows.Handle
	for deadline := time.Now().Add(5 * time.Second); ; {
		h, err = windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != windows.ERROR_PIPE_BUSY || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL not protected: %v %v", control, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("no DACL: %v", err)
	}
	own, _ := CurrentSID()
	allowed := map[string]bool{own: true, "S-1-5-18": true, "S-1-5-32-544": true}
	seen := map[string]bool{}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("ACE type %d", ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if !allowed[sid] {
			t.Errorf("unexpected principal %s", sid)
		}
		seen[sid] = true
	}
	if len(seen) != len(allowed) {
		t.Fatalf("DACL principals %v", seen)
	}
}

// The name cannot be claimed twice: a second listener fails instead of sharing it.
func Test_ControlPipeNameIsExclusive(t *testing.T) {
	f := newFixture(t)
	if _, _, err := Listen(ListenOptions{StateDir: f.server.o.StateDir, Node: "ctl-node"}); err == nil {
		t.Fatal("second listener on the same pipe name")
	}
}

// The state directory is private, so the SIDs allowed on the pipe get read access to control.json
// itself, and again after every rewrite.
func Test_ControlEndpointFileIsReadableByAllowedSIDs(t *testing.T) {
	const reader = "S-1-5-21-1-2-3-1001"
	dir := t.TempDir()
	e := v1.ControlEndpointFile{APIVersion: v1.APIVersion, Node: "n", Transport: v1.TransportPipe, Endpoint: `\\.\pipe\x`}
	for i := 0; i < 2; i++ {
		if err := WriteEndpoint(dir, e, reader); err != nil {
			t.Fatal(err)
		}
		sd, err := windows.GetNamedSecurityInfo(filepath.Join(dir, FileName), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil {
			t.Fatalf("DACL: %v %v", acl, err)
		}
		found := false
		for n := uint32(0); n < uint32(acl.AceCount); n++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, n, &ace); err != nil {
				t.Fatal(err)
			}
			if (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String() == reader && ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE {
				found = true
			}
		}
		if !found {
			t.Fatalf("pass %d: %s has no allow entry on control.json", i, reader)
		}
	}
}

// Closing the listener right after a client connected and went away must not hang. go-winio's
// accept loop retries such a connection (ERROR_NO_DATA) and, on that path, consumed the one
// close request, so Close waited forever (seen as a 2-minute test timeout in cleanup).
// XMESH_TEST_PIPE_CLOSE_ROUNDS sets the number of rounds. XMESH_TEST_PIPE_CLOSE_RAW=1 uses the
// bare go-winio listener instead, counts the rounds that hang, and releases each with a second
// Close; that run only reports and never fails.
func Test_ControlPipeListenerCloseAfterClientDrops(t *testing.T) {
	rounds := 60
	if v, err := strconv.Atoi(os.Getenv("XMESH_TEST_PIPE_CLOSE_ROUNDS")); err == nil && v > 0 {
		rounds = v
	}
	raw := os.Getenv("XMESH_TEST_PIPE_CLOSE_RAW") == "1"
	hung := 0
	for i := 0; i < rounds; i++ {
		node := fmt.Sprintf("close-drop-%d-%d", os.Getpid(), i)
		l, bound, err := Listen(ListenOptions{StateDir: t.TempDir(), Node: node})
		if err != nil {
			t.Fatal(err)
		}
		accepted := make(chan struct{})
		go func() {
			defer close(accepted)
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		name, err := windows.UTF16PtrFromString(bound.Address)
		if err != nil {
			t.Fatal(err)
		}
		var h windows.Handle
		for deadline := time.Now().Add(5 * time.Second); ; {
			h, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
			if err != windows.ERROR_PIPE_BUSY || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		windows.CloseHandle(h)
		closer := l
		if raw {
			closer = l.(*pipeListener).Listener
		}
		stopped := make(chan struct{})
		go func() { closer.Close(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			if !raw {
				t.Fatalf("round %d: Close did not return 5s after the client dropped", i)
			}
			hung++
			go closer.Close()
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatalf("round %d: a second Close did not release the first", i)
			}
		}
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: Accept still blocked after Close", i)
		}
	}
	if raw {
		t.Logf("bare go-winio listener: %d of %d rounds needed a second Close", hung, rounds)
	}
}

// stuckListener never stops, like go-winio's loop after it has lost a close request.
type stuckListener struct {
	net.Listener
	closes chan struct{}
}

func (l *stuckListener) Close() error {
	l.closes <- struct{}{}
	select {} // blocks for good
}

// A listener that never stops must make pipeListener.Close return an error once the retries
// are used up, not block the caller.
func Test_ControlPipeListenerCloseGivesUp(t *testing.T) {
	oldRetry, oldAttempts := pipeCloseRetry, pipeCloseAttempts
	pipeCloseRetry, pipeCloseAttempts = time.Millisecond, 5
	defer func() { pipeCloseRetry, pipeCloseAttempts = oldRetry, oldAttempts }()
	inner := &stuckListener{closes: make(chan struct{}, 100)}
	l := &pipeListener{Listener: inner}
	result := make(chan error, 1)
	go func() { result <- l.Close() }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Close of a listener that never stops returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on a listener that never stops")
	}
	if len(inner.closes) < 2 {
		t.Fatalf("Close was requested %d times, want retries", len(inner.closes))
	}
}
