//go:build windows

package control

import (
	"path/filepath"
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
