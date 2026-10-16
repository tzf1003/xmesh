package winbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func Test_WindowsAppContainerSelectedObjects(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	for _, mode := range []string{"release", "rollback", "cancel", "retry"} {
		t.Run(mode, func(t *testing.T) {
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
			child := filepath.Join(root, "child")
			file := filepath.Join(child, "file.txt")
			if err := os.MkdirAll(child, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("content"), 0600); err != nil {
				t.Fatal(err)
			}
			before := map[string][]byte{}
			for _, path := range []string{root, child, file} {
				before[path] = readObjectACL(t, path)
			}
			tree, err := openWorkspaceTree(context.Background(), root, 3)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tree.Close)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			if mode == "rollback" {
				// Reject the last selection after root and directory admission.
				tree.objects[len(tree.objects)-1].id.ID[0] ^= 1
			}
			selected, err := p.grantSelectedObjects(ctx, tree)
			if mode == "rollback" || mode == "cancel" {
				if err == nil || selected != nil {
					if selected != nil {
						selected.Close()
					}
					t.Fatal("failed admission did not completely roll back", err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("admission cancellation lost", err)
				}
			} else {
				if err != nil {
					if selected != nil {
						selected.Close()
					}
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := selected.Close(); err != nil {
						t.Error(err)
					}
				})
				if p.grants != len(tree.objects) {
					t.Fatal("incomplete snapshot authorization", p.grants)
				}
				for path, acl := range before {
					if bytes.Equal(acl, readObjectACL(t, path)) {
						t.Fatal("selected object was not authorized", path)
					}
				}
				if err := os.Rename(root, root+".moved"); err == nil {
					t.Fatal("snapshot namespace guard lost")
				}
				if mode == "retry" {
					// An external ACL writer removes one owned ACE. Cleanup must
					// retain that object while releasing the other grants.
					blocked := selected.grants[1]
					if err := setObjectGrant(blocked.aclHandle, blocked.ace, false); err != nil {
						t.Fatal(err)
					}
					if err := selected.Close(); err == nil || p.grants != 1 {
						t.Fatal("failed release did not retain exactly one grant", err, p.grants)
					}
					if err := p.Close(); err == nil {
						t.Fatal("profile deleted before failed release recovered")
					}
					if err := setObjectGrant(blocked.aclHandle, blocked.ace, true); err != nil {
						t.Fatal(err)
					}
				}
				if err := selected.Close(); err != nil {
					t.Fatal("release selected objects", err)
				}
				if err := selected.Close(); err != nil {
					t.Fatal("repeat release", err)
				}
			}
			for path, acl := range before {
				if !bytes.Equal(acl, readObjectACL(t, path)) {
					t.Fatal("selected object ACL not restored", path)
				}
			}
			if err := p.Close(); err != nil {
				t.Fatal("profile retained after cleanup", err)
			}
			tree.Close()
			if err := os.Rename(root, root+".moved"); err != nil {
				t.Fatal("namespace guard retained after snapshot close", err)
			}
		})
	}
}
