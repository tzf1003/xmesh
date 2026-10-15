package winbox

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type workspaceProof struct {
	AppContainer  bool
	ExistingRead  bool
	ExistingWrite bool
	NestedWrite   bool
	NestedRead    bool
	OutsideRead   bool
	OutsideWrite  bool
}

func Test_WindowsAppContainerWorkspaceGrant(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	root := filepath.Join(t.TempDir(), "workspace")
	child := filepath.Join(root, "existing-child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(child, "existing.txt")
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	for _, path := range []string{file, outside} {
		if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string][]byte{}
	labels := map[string]string{}
	for _, path := range []string{root, child, file} {
		setMediumLabel(t, path)
		before[path] = readObjectACL(t, path)
		labels[path] = objectLabel(t, path)
	}
	outsideACL := readObjectACL(t, outside)
	g, err := p.grantWorkspace(context.Background(), root, 64)
	if g != nil {
		t.Cleanup(func() {
			if err := g.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err == nil {
		t.Fatal("profile deleted before inherited grants were recovered")
	}
	if err := os.Rename(root, root+".moved"); err == nil {
		t.Fatal("workspace root binding lost")
	}
	helper := filepath.Join(p.Directory(), "workspace-helper.exe")
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, body, 0700); err != nil {
		t.Fatal(err)
	}
	run := func() workspaceProof {
		t.Helper()
		body := runAppContainerHelper(t, p, helper, "Test_WindowsAppContainerWorkspaceHelper", "XMESH_WINBOX_WORKSPACE_HELPER", root, outside)
		var proof workspaceProof
		if err := json.Unmarshal(body, &proof); err != nil {
			t.Fatal(err)
		}
		return proof
	}
	proof := run()
	if !proof.AppContainer || !proof.ExistingRead || !proof.ExistingWrite || !proof.NestedWrite || !proof.NestedRead || proof.OutsideRead || proof.OutsideWrite {
		t.Fatalf("workspace grant proof: %+v", proof)
	}
	created := filepath.Join(root, "new-tree", "level-1", "level-2", "created.txt")
	createdLabel := objectLabel(t, created)
	other, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
	})
	otherGrant, err := other.grantObject(created)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := otherGrant.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := g.Close(); err != nil {
		t.Fatal("recover workspace inheritance", err)
	}
	if err := g.Close(); err != nil {
		t.Fatal("repeat workspace release", err)
	}
	for path, acl := range before {
		if !bytes.Equal(acl, readObjectACL(t, path)) || objectLabel(t, path) != labels[path] {
			t.Fatal("existing object ACL or label changed after release", path)
		}
	}
	if !bytes.Equal(outsideACL, readObjectACL(t, outside)) || objectLabel(t, created) != createdLabel {
		t.Fatal("outside ACL or created object label changed")
	}
	// Inspect every resulting object independently, including the newly created
	// directories, and require no allow ACE for the released profile.
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		acl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		_, aces, err := aclParts(acl)
		if err != nil {
			return err
		}
		sid := unsafe.Slice((*byte)(unsafe.Pointer(p.sid)), p.sid.Len())
		for _, ace := range aces {
			if len(ace) >= 8 && ace[0] == windows.ACCESS_ALLOWED_ACE_TYPE && bytes.Equal(ace[8:], sid) {
				t.Fatal("released profile ACE retained", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	acl, err := objectDACL(otherGrant.handle)
	if err != nil {
		t.Fatal(err)
	}
	_, aces, err := aclParts(acl)
	if err != nil {
		t.Fatal(err)
	}
	foundOther := false
	for _, ace := range aces {
		foundOther = foundOther || bytes.Equal(ace, otherGrant.ace)
	}
	if !foundOther {
		t.Fatal("another profile ACE removed from new file")
	}
	proof = run()
	if !proof.AppContainer || proof.ExistingRead || proof.ExistingWrite || proof.NestedRead || proof.NestedWrite || proof.OutsideRead || proof.OutsideWrite {
		t.Fatalf("released workspace proof: %+v", proof)
	}
	if err := otherGrant.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+".moved"); err != nil {
		t.Fatal("workspace guard retained after cleanup", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerWorkspaceCleanupRetry(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	before := readObjectACL(t, root)
	g, err := p.grantWorkspace(context.Background(), root, 1)
	if g != nil {
		t.Cleanup(func() {
			if err := g.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(root, "new-directory")
	if err := os.Mkdir(created, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(created) })
	if err := g.Close(); err == nil {
		t.Fatal("incomplete cleanup scan accepted")
	}
	if err := p.Close(); err == nil {
		t.Fatal("profile deleted after incomplete cleanup")
	}
	if err := os.Rename(root, root+".moved"); err == nil {
		t.Fatal("namespace guard released after incomplete cleanup")
	}
	if err := os.Remove(created); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal("retry inherited cleanup", err)
	}
	if !bytes.Equal(before, readObjectACL(t, root)) {
		t.Fatal("root ACL not restored after cleanup retry")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerWorkspaceHelper(t *testing.T) {
	if os.Getenv("XMESH_WINBOX_WORKSPACE_HELPER") != "1" {
		t.Skip("AppContainer workspace helper")
	}
	args := os.Args[len(os.Args)-3:]
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil {
		t.Fatal(err)
	}
	proof := workspaceProof{AppContainer: container != 0}
	existing := filepath.Join(args[0], "existing-child", "existing.txt")
	_, err := os.ReadFile(existing)
	proof.ExistingRead = err == nil
	proof.ExistingWrite = os.WriteFile(existing, []byte("updated"), 0600) == nil
	nested := filepath.Join(args[0], "new-tree", "level-1", "level-2")
	if err := os.MkdirAll(nested, 0700); err == nil {
		file := filepath.Join(nested, "created.txt")
		proof.NestedWrite = os.WriteFile(file, []byte("created"), 0600) == nil
		body, err := os.ReadFile(file)
		proof.NestedRead = err == nil && string(body) == "created"
	}
	_, err = os.ReadFile(args[1])
	proof.OutsideRead = err == nil
	proof.OutsideWrite = os.WriteFile(args[1], []byte("changed"), 0600) == nil
	body, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[2], body, 0600); err != nil {
		t.Fatal(err)
	}
}
