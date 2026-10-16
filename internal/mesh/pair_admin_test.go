package mesh

import (
	"path/filepath"
	"testing"
)

func Test_CTRGW05_LANPairAllowsAdmin(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	path := filepath.Join(t.TempDir(), "admin.json")
	fp, e := ExportPair(a, "admin", path)
	if e != nil {
		t.Fatal(e)
	}
	if e = ImportPair(b, path, fp, []string{"tag:root"}); e == nil {
		t.Fatal("unknown role accepted")
	}
	if e = ImportPair(b, path, fp, []string{"tag:admin"}); e != nil {
		t.Fatal(e)
	}
	trust, e := OpenTrust(b)
	if e != nil {
		t.Fatal(e)
	}
	peer, ok := trust.Find(fp)
	if !ok || len(peer.Tags) != 1 || peer.Tags[0] != "tag:admin" {
		t.Fatal("administrator role not persisted", peer)
	}
}
