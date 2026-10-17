package winjob

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tzf1003/xmesh/internal/winjob/winjobtest"
)

func Test_WindowsAppExecLinkDetection(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.exe")
	if err := os.WriteFile(plain, []byte("MZ"), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.exe")
	winjobtest.AppExecLink(t, alias)
	if IsAppExecLink(plain) || IsAppExecLink(filepath.Join(dir, "missing.exe")) {
		t.Fatal("a regular or missing file was taken for an application execution alias")
	}
	if !IsAppExecLink(alias) {
		t.Fatal("the application execution alias was not recognised")
	}
}
