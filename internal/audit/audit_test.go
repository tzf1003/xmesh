package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func Test_INV10_NoBodyInAudit(t *testing.T) {
	dir := t.TempDir()
	l, e := New(dir, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	secret := "fixture-sensitive-body"
	raw, _ := json.Marshal(map[string]any{"content": secret, "env": map[string]string{"KEY": secret}, "cmd": "/bin/echo", "args": []string{secret}})
	if e = l.Record("node/session", "", "device", "fs.write", raw, map[string]any{"content": secret, "resolved_path": "/tmp/a"}, false, time.Millisecond); e != nil {
		t.Fatal(e)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	b, _ := os.ReadFile(files[0])
	if strings.Contains(string(b), secret) {
		t.Fatal("body reached audit")
	}
	var record map[string]any
	_ = json.Unmarshal(b, &record)
	record["stdout"] = secret
	if e = l.Write(record); e == nil {
		t.Fatal("encoder accepted stdout")
	}
	b, _ = os.ReadFile(files[0])
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), "audit_dropped") {
		t.Fatal(string(b))
	}
	if e = l.Prune(time.Now().Add(2 * time.Hour)); e != nil {
		t.Fatal(e)
	}
	files, _ = filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 0 {
		t.Fatal("expired audit retained")
	}
}
