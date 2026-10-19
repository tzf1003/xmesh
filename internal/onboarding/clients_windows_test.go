//go:build windows

package onboarding

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"
)

func TestRecordReplacementKeepsConcurrentReadersAvailable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "record.json")
	before := []byte(`{"status":"running"}`)
	after := []byte(`{"status":"succeeded"}`)
	if err := atomicWrite(file, before); err != nil {
		t.Fatal(err)
	}
	reader, err := openClientFile(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := atomicWriteCAS(file, digest(before), after); err != nil {
		t.Fatal(err)
	}
	current, exists, err := readClientFile(file)
	if err != nil || !exists || !bytes.Equal(current, after) {
		t.Fatalf("replacement unavailable while previous snapshot is open: exists=%v error=%v", exists, err)
	}
	previous, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(previous, before) {
		t.Fatal("existing reader lost its original snapshot", err)
	}
}
