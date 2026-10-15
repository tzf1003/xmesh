//go:build windows

package fs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Test_WindowsAtomicReplaceOpenReader(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := atomicWrite(target, []byte("old"), ""); err != nil {
		t.Fatal(err)
	}
	reader, err := openRead(target)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := atomicWrite(target, []byte("new complete contents"), ""); err != nil {
		t.Fatal(err, errors.Unwrap(err))
	}
	if old, err := io.ReadAll(reader); err != nil || string(old) != "old" {
		t.Fatal("existing reader changed", string(old), err)
	}
	if current, err := os.ReadFile(target); err != nil || string(current) != "new complete contents" {
		t.Fatal("new reader did not see replacement", string(current), err)
	}
}

func Test_WindowsAtomicReplaceLongPath(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		root = filepath.Join(root, strings.Repeat("segment", 10))
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.txt")
	if err := atomicWrite(target, []byte("old"), ""); err != nil {
		t.Fatal(err, errors.Unwrap(err))
	}
	reader, err := openRead(target)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := atomicWrite(target, []byte("new"), ""); err != nil {
		t.Fatal(err, errors.Unwrap(err))
	}
	if current, err := readBounded(target, 1024); err != nil || string(current) != "new" {
		t.Fatal("long path contents", string(current), err)
	}
}

func Test_WindowsAtomicReplaceLockedTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := atomicWrite(target, []byte("old"), ""); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := atomicWrite(target, []byte("new"), ""); err == nil {
		t.Fatal("replaced a target locked against deletion")
	}
	if current, err := os.ReadFile(target); err != nil || string(current) != "old" {
		t.Fatal("locked target changed", string(current), err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary file cleanup", entries, err)
	}
}
