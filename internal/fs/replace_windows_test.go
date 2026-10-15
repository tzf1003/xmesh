//go:build windows

package fs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func Test_WindowsAtomicReplaceOpenReader(t *testing.T) {
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
