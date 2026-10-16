package policy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func Test_L2CredentialWorkspaceBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix isolation workspace policy")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err = os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	external := filepath.Join(root, "external-ssh")
	if err = os.Mkdir(external, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(external, filepath.Join(home, ".ssh")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"/", home, filepath.Join(home, ".aws"), filepath.Join(home, ".azure"), external, filepath.Join(external, "child")} {
		if err = credentialWorkspace(dir); err == nil {
			t.Fatal("credential workspace accepted", dir)
		}
	}
	if err = credentialWorkspace(filepath.Join(home, "workspace")); err != nil {
		t.Fatal("independent workspace", err)
	}
}
