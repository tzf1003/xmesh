package winbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerCredentialBoundaries(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	base := t.TempDir()
	home := filepath.Join(base, "home")
	secret := filepath.Join(home, ".ssh")
	child := filepath.Join(secret, "child")
	work := filepath.Join(home, "workspace")
	for _, dir := range []string{child, work} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{secret, filepath.Join(home, ".config", "gcloud"), filepath.Join(home, ".aws")}
	for _, path := range []string{base, home, secret, child} {
		tree, err := openWorkspaceTree(context.Background(), path, 32)
		if err != nil {
			t.Fatal(err)
		}
		handles, err := pinCredentialBoundaries(context.Background(), tree, paths)
		closeCredentialHandles(handles)
		tree.Close()
		if err == nil {
			t.Fatal("credential overlap admitted", path)
		}
	}
	tree, err := openWorkspaceTree(context.Background(), work, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	if handles, err := pinCredentialBoundaries(context.Background(), tree, []string{filepath.Join(work, ".aws")}); err == nil {
		closeCredentialHandles(handles)
		t.Fatal("workspace containing a missing credential directory admitted")
	}
	// Case folding and alternate path spelling must resolve to object identity.
	for _, spelling := range []string{secret, strings.ToUpper(secret)} {
		handles, err := pinCredentialBoundaries(context.Background(), tree, []string{spelling})
		if err != nil {
			t.Fatal("independent sibling workspace", err)
		}
		if err := os.Rename(secret, secret+".moved"); err == nil {
			t.Fatal("credential guard did not retain its binding")
		}
		closeCredentialHandles(handles)
	}
	// A missing credential directory still reserves its ancestors, while an
	// unrelated sibling remains admissible.
	handles, err := pinCredentialBoundaries(context.Background(), tree, paths)
	if err != nil {
		t.Fatal("missing credential directories", err)
	}
	closeCredentialHandles(handles)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if handles, err := pinCredentialBoundaries(ctx, tree, paths); !errors.Is(err, context.Canceled) {
		closeCredentialHandles(handles)
		t.Fatal("credential scan cancellation", err)
	}
	if err := os.Rename(secret, secret+".moved"); err != nil {
		t.Fatal("credential handles retained after close or failure", err)
	}
	if err := os.Rename(secret+".moved", secret); err != nil {
		t.Fatal(err)
	}
	// An existing non-directory credential path cannot provide a complete
	// directory boundary.
	file := filepath.Join(home, ".aws")
	if err := os.WriteFile(file, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if handles, err := pinCredentialBoundaries(context.Background(), tree, paths); err == nil {
		closeCredentialHandles(handles)
		t.Fatal("non-directory credential boundary accepted")
	}
}

func Test_WindowsAppContainerCredentialAliases(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	base := t.TempDir()
	secret := filepath.Join(base, "credential-target")
	work := filepath.Join(base, "workspace")
	for _, dir := range []string{secret, work} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(base, "credential-alias")
	if err := os.Symlink(secret, alias); err != nil {
		t.Fatal("credential alias fixture", err)
	}
	for _, candidate := range []struct {
		path string
		deny bool
	}{{secret, true}, {work, false}} {
		tree, err := openWorkspaceTree(context.Background(), candidate.path, 1)
		if err != nil {
			t.Fatal(err)
		}
		handles, err := pinCredentialBoundaries(context.Background(), tree, []string{alias})
		closeCredentialHandles(handles)
		tree.Close()
		if (err != nil) != candidate.deny {
			t.Fatal("credential alias identity", candidate.deny, err)
		}
	}
	treeWithCase, err := openWorkspaceTree(context.Background(), secret, 1)
	if err != nil {
		t.Fatal(err)
	}
	handles, caseErr := pinCredentialBoundaries(context.Background(), treeWithCase, []string{strings.ToUpper(secret)})
	closeCredentialHandles(handles)
	treeWithCase.Close()
	if caseErr == nil {
		t.Fatal("case-variant credential identity admitted")
	}
	// The OS may leave 8.3 spelling unchanged on a volume with short names
	// disabled; otherwise the same identity must still be rejected.
	name, err := windows.UTF16PtrFromString(secret)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(name, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		t.Fatal("query credential short path", n, err)
	}
	tree, err := openWorkspaceTree(context.Background(), secret, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	if handles, err := pinCredentialBoundaries(context.Background(), tree, []string{windows.UTF16ToString(buffer[:n])}); err == nil {
		closeCredentialHandles(handles)
		t.Fatal("credential short-path identity admitted")
	}
}

func Test_WindowsAppContainerCredentialTokenDirectories(t *testing.T) {
	// Directory selection is bound to the process token, including when a
	// launcher has supplied a different child home or application directory.
	before, err := credentialDirectories()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HOME", "USERPROFILE", "APPDATA"} {
		t.Setenv(name, `C:\untrusted-child-home`)
	}
	after, err := credentialDirectories()
	if err != nil || strings.Join(before, "\x00") != strings.Join(after, "\x00") {
		t.Fatal("credential directories changed with environment", err)
	}
}
