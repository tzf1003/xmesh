//go:build windows

package privdir

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func userSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

func sddlOf(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

// setDACL replaces the DACL of path with sddl. protected disables inheritance.
func setDACL(t *testing.T, path, sddl string, protected bool) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	var info windows.SECURITY_INFORMATION = windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	if protected {
		info = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func mustPrincipals(t *testing.T) principals {
	t.Helper()
	p, err := currentPrincipals()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func Test_WindowsPrivdirCreateNew(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "state")
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
	got := sddlOf(t, dir)
	if !strings.HasPrefix(got, "D:P") {
		t.Fatal("DACL must be protected from inheritance:", got)
	}
	for _, other := range []string{";;;WD)", ";;;BU)", ";;;AU)", ";;;IU)", ";;;CO)"} {
		if strings.Contains(got, other) {
			t.Fatalf("DACL grants a broad group %s: %s", other, got)
		}
	}
	for _, sid := range []string{";;;SY)", ";;;BA)"} {
		if !strings.Contains(got, sid) {
			t.Fatalf("DACL lacks %s: %s", sid, got)
		}
	}
	// A file created later inherits the private access.
	f := filepath.Join(dir, "key")
	if err := os.WriteFile(f, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if child := sddlOf(t, f); strings.Contains(child, ";;;WD)") || strings.Contains(child, ";;;BU)") {
		t.Fatal("new child is not private:", child)
	}
	before := sddlOf(t, dir)
	if err := Ensure(dir); err != nil {
		t.Fatal("second Ensure:", err)
	}
	if after := sddlOf(t, dir); after != before {
		t.Fatalf("Ensure rewrote a private directory:\n%s\n%s", before, after)
	}
}

func Test_WindowsPrivdirTightensExisting(t *testing.T) {
	user := userSID(t)
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// A state directory made under a permissive parent: Everyone has access,
	// inherited by the files already inside.
	setDACL(t, dir, "D:(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;"+user+")", false)
	inherited := filepath.Join(dir, "inherited.json")
	explicit := filepath.Join(dir, "explicit.json")
	sub := filepath.Join(dir, "audit")
	nested := filepath.Join(sub, "nested.log")
	for _, f := range []string{inherited, explicit} {
		if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	setDACL(t, explicit, "D:P(A;;FA;;;WD)(A;;FA;;;"+user+")", true)
	if Verify(dir) == nil {
		t.Fatal("a directory open to Everyone must not verify")
	}
	for _, f := range []string{inherited, explicit, nested} {
		if !strings.Contains(sddlOf(t, f), ";;;WD)") {
			t.Fatalf("fixture %s is not open to Everyone: %s", f, sddlOf(t, f))
		}
	}
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{inherited, explicit, nested, sub} {
		if got := sddlOf(t, f); strings.Contains(got, ";;;WD)") {
			t.Fatalf("%s is still open to Everyone: %s", f, got)
		}
	}
}

func Test_WindowsPrivdirRefusesLinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("junctions cannot be created by this account: %v: %s", err, out)
	}
	before := sddlOf(t, target)
	if err := Ensure(link); err == nil {
		t.Fatal("a junction must be refused as a state directory")
	}
	if sddlOf(t, target) != before {
		t.Fatal("the junction target was modified")
	}

	// A junction planted inside a loose directory is refused during the sweep.
	dir := filepath.Join(root, "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	setDACL(t, dir, "D:(A;OICI;FA;;;WD)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;"+userSID(t)+")", false)
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(dir, "planted"), target).CombinedOutput(); err != nil {
		t.Skipf("junctions cannot be created by this account: %v: %s", err, out)
	}
	if err := Ensure(dir); err == nil || !strings.Contains(err.Error(), "junction") {
		t.Fatal("a planted junction must fail the sweep:", err)
	}
}

func Test_WindowsPrivdirRefusesFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(f); err == nil {
		t.Fatal("a regular file is not a state directory")
	}
}

func Test_WindowsPrivdirDescriptorPolicy(t *testing.T) {
	p := mustPrincipals(t)
	user := userSID(t)
	sdOf := func(s string) *windows.SECURITY_DESCRIPTOR {
		t.Helper()
		sd, err := windows.SecurityDescriptorFromString(s)
		if err != nil {
			t.Fatal(s, err)
		}
		return sd
	}
	canonical := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user + ")"
	cases := []struct {
		name, sddl string
		exact      bool
		want       string
	}{
		{"canonical", canonical, true, ""},
		{"deny entries are harmless", "D:P(D;;FA;;;BG)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user + ")", true, ""},
		{"everyone", "D:P(A;;FA;;;WD)(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + user + ")", true, "grants access to S-1-1-0"},
		{"users group", "D:P(A;;FRFW;;;BU)(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + user + ")", false, "grants access to S-1-5-32-545"},
		{"creator owner", "D:P(A;OICI;FA;;;CO)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user + ")", true, "grants access to S-1-3-0"},
		{"inherits", "D:(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user + ")", true, "inherits access from its parent"},
		{"inherits but allowed when not exact", "D:(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user + ")", false, ""},
		{"missing principal", "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + user + ")", true, "lacks access for S-1-5-32-544"},
		{"null dacl", "D:NO_ACCESS_CONTROL", false, "null DACL grants everyone access"},
	}
	for _, c := range cases {
		if got := p.daclProblem(sdOf(c.sddl), c.exact); got != c.want {
			t.Errorf("%s: daclProblem = %q, want %q", c.name, got, c.want)
		}
	}
	if got := p.ownerProblem(sdOf("O:BAD:P(A;;FA;;;SY)")); got != "" {
		t.Error("Administrators must be an acceptable owner:", got)
	}
	if got := p.ownerProblem(sdOf("O:SYD:P(A;;FA;;;SY)")); got != "" {
		t.Error("SYSTEM must be an acceptable owner:", got)
	}
	if got := p.ownerProblem(sdOf("O:S-1-5-21-1-2-3-1001D:P(A;;FA;;;SY)")); got != "owned by S-1-5-21-1-2-3-1001" {
		t.Error("a foreign owner must be refused:", got)
	}
}

func Test_WindowsPrivdirGrantRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "control.json")
	secret := filepath.Join(dir, "key")
	for _, f := range []string{manifest, secret} {
		if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dirBefore := sddlOf(t, dir)
	// Authenticated Users stands in for the installer account of a SYSTEM service.
	if err := GrantRead(manifest, "S-1-5-11"); err != nil {
		t.Fatal(err)
	}
	if got := sddlOf(t, manifest); !strings.Contains(got, ";;;AU)") {
		t.Fatal("the manifest does not grant read to the named SID:", got)
	}
	if got := sddlOf(t, secret); strings.Contains(got, ";;;AU)") {
		t.Fatal("another file in the directory was opened:", got)
	}
	if sddlOf(t, dir) != dirBefore {
		t.Fatal("the directory ACL changed")
	}
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
	if err := GrantRead(manifest, "not-a-sid"); err == nil {
		t.Fatal("an invalid SID must be refused")
	}
	if err := GrantRead(dir, "S-1-5-11"); err == nil {
		t.Fatal("only regular files can be granted")
	}
}
