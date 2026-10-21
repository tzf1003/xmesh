package fs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// conformance: CTR-FS-07
func Test_ExistingTargetConflictMetadata(t *testing.T) {
	for _, kind := range []string{"file", "empty", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s, root := fixture(t)
			source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
			writeBytes(t, source, []byte("source"))
			content := []byte("existing target")
			if kind == "empty" {
				content = []byte{}
			}
			if kind == "directory" {
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeBytes(t, target, content)
			}
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			v := fail(t, s, "fs.move", Object{"from": "source", "to": "target", "overwrite": false}, "conflict")
			if _, ok := v.Fields["current_sha256"]; ok {
				t.Errorf("existence conflict reports unread content hash: %v", v.Fields)
			}
			if v.Fields["path"] != target || v.Fields["mtime"] != Stamp(before.ModTime()) {
				t.Errorf("target metadata: %v", v.Fields)
			}
			if string(readBytes(t, source)) != "source" {
				t.Error("source changed")
			}
			after, err := os.Stat(target)
			if err != nil || after.IsDir() != before.IsDir() || !after.ModTime().Equal(before.ModTime()) {
				t.Fatalf("target changed: %v, %v", after, err)
			}
			if kind != "directory" {
				if string(readBytes(t, target)) != string(content) {
					t.Error("target content changed")
				}
				v = fail(t, s, "fs.mkdir", Object{"path": "target"}, "conflict")
				if _, ok := v.Fields["current_sha256"]; ok {
					t.Errorf("mkdir conflict reports unread content hash: %v", v.Fields)
				}
			}
		})
	}
}

// conformance: CTR-FS-07 INV-06
func Test_MoveConflictPreservesSymlinksAndPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows privileges")
	}
	s, root := fixture(t)
	writeBytes(t, filepath.Join(root, "source"), []byte("source"))
	for _, target := range []string{"allowed", "secret"} {
		writeBytes(t, filepath.Join(root, target), []byte("target"))
		link := filepath.Join(root, target+"-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		want := "conflict"
		if target == "secret" {
			want = "invalid_cwd"
		}
		v := fail(t, s, "fs.move", Object{"from": "source", "to": target + "-link"}, want)
		if _, ok := v.Fields["current_sha256"]; ok {
			t.Errorf("move conflict reports content hash: %v", v.Fields)
		}
		if got, err := os.Readlink(link); err != nil || got != target {
			t.Errorf("link changed: %q, %v", got, err)
		}
		if string(readBytes(t, filepath.Join(root, target))) != "target" || string(readBytes(t, filepath.Join(root, "source"))) != "source" {
			t.Error("move refusal changed source or target")
		}
	}
}

// conformance: CTR-FS-02 CTR-FS-03
func Test_ContentConflictHashesIncludeEmptyFiles(t *testing.T) {
	for _, content := range []string{"", "current contents"} {
		for _, name := range []string{"fs.write", "fs.edit"} {
			t.Run(name+"/"+content, func(t *testing.T) {
				s, root := fixture(t)
				path := filepath.Join(root, "target")
				writeBytes(t, path, []byte(content))
				v := fail(t, s, name, Object{"path": "target", "content": "new", "old_string": "current", "new_string": "new", "expected_sha256": Hash([]byte("stale"))}, "conflict")
				if v.Fields["current_sha256"] != Hash([]byte(content)) {
					t.Errorf("current content hash: %v", v.Fields)
				}
				if _, err := time.Parse(time.RFC3339Nano, v.Fields["mtime"].(string)); err != nil {
					t.Fatal(err)
				}
				if string(readBytes(t, path)) != content {
					t.Error("content changed")
				}
			})
		}
	}
}
