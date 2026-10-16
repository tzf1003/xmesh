//go:build windows

package control

import (
	"strings"
	"testing"
	"unsafe"

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
	for _, bad := range []string{"", "WD", "S-1-5-18)(A;;GA;;;WD", "x"} {
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
	h, err := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
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
