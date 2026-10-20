//go:build windows

package control

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigAuthkeyWindowsPrivateACL(t *testing.T) {
	file := filepath.Join(t.TempDir(), "authkeys", "key")
	if err := createConfigAuthkey(file, "tskey-auth-windows-test"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{filepath.Dir(file), file} {
		sd, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		for _, group := range []string{";;;WD)", ";;;BU)", ";;;AU)", ";;;IU)"} {
			if strings.Contains(sd.String(), group) {
				t.Fatal("credential grants broad access:", sd.String())
			}
		}
	}
}
