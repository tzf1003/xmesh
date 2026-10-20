package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDesktopAuditOmitsInputAndGuessableDigestsAtEveryLayer(t *testing.T) {
	dir := t.TempDir()
	l, e := New(dir, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	raw := json.RawMessage(`{"session_id":"cs-1","action_id":"a-1","actions":[{"type":"text","text":"SECRET-1234"}]}`)
	for _, name := range []string{"computer.act", "mac/computer.act"} {
		if e = l.Record("node/session", "client", "mac", name, raw, map[string]any{"state": "executed"}, false, time.Millisecond); e != nil {
			t.Fatal(e)
		}
	}
	h := sha256.Sum256(raw)
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) == 0 {
		t.Fatal("no audit")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, v := range []string{"SECRET-1234", hex.EncodeToString(h[:]), "value_sha256", "output_sha256"} {
			if bytes.Contains(b, []byte(v)) {
				t.Fatal("desktop content or digest reached audit", v)
			}
		}
	}
	for _, r := range records(t, dir) {
		d := r["args_digest"].(map[string]any)
		if d["action_id"] != "a-1" || d["session_id"] != "cs-1" {
			t.Fatal(r)
		}
	}
}
