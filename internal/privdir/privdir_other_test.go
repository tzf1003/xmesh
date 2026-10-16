//go:build !windows

package privdir

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_PrivdirUnixCreateAndTighten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "state")
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if Verify(dir) == nil {
		t.Fatal("a 0755 directory must not verify")
	}
	if err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err != nil {
		t.Fatal("Ensure must tighten an existing directory:", err)
	}
	if err := GrantRead(filepath.Join(dir, "none"), "S-1-5-11"); err != nil {
		t.Fatal("GrantRead is a no-op on Unix:", err)
	}
}
