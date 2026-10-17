package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/oauth"
)

func Test_IngressAdminPasswordFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "oauth-password")
	if err := os.WriteFile(file, []byte("an old password!\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := oauth.New(oauth.Options{Issuer: "https://mesh.example", Password: "an old password!", StateDir: filepath.Join(dir, "oauth")})
	if err != nil {
		t.Fatal(err)
	}
	admin := &ingressAdmin{auth: auth, passwordFile: file}
	if err = admin.RotatePassword("a new password!!"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file)
	if strings.TrimSpace(string(got)) != "a new password!!" {
		t.Fatalf("%q", got)
	}
	if st, _ := os.Stat(file); runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", st.Mode())
	}
	// a failed write leaves the file and the running password alone
	broken := &ingressAdmin{auth: auth, passwordFile: filepath.Join(dir, "missing", "pw")}
	if err = broken.RotatePassword("never applied!!!"); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
	var source *control.PasswordSourceError
	if err = (&ingressAdmin{auth: auth}).RotatePassword("never applied!!!"); err == nil || !asSource(err, &source) {
		t.Fatalf("%v", err)
	}
	if admin.Issuer() != "https://mesh.example" || len(admin.Clients()) != 0 {
		t.Fatal("issuer or clients")
	}
	if found, err := admin.RevokeClient("nobody"); found || err != nil {
		t.Fatal(found, err)
	}
}

func asSource(err error, target **control.PasswordSourceError) bool {
	s, ok := err.(*control.PasswordSourceError)
	*target = s
	return ok
}
