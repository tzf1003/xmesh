package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func readExample(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "schemas", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func rawFiles(t *testing.T, dir string) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var all strings.Builder
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	return all.String()
}

// conformance: CTR-AUD-01 INV-10 CON-SEC-08
func Test_CTRAUD01_SchemaExamplesAndRejectedRecord(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err = l.Write(readExample(t, "audit-event.json")); err != nil {
		t.Fatalf("the contract example event is rejected: %v", err)
	}
	if len(records(t, dir)) != 1 {
		t.Fatal("example event was not written")
	}
	// The invalid example (a stdout field inside args_digest) is refused: nothing from
	// it is persisted, and one parameterless audit_dropped record takes its place.
	err = l.Write(readExample(t, "invalid/audit-with-stdout.json"))
	if err == nil {
		t.Fatal("event with stdout was accepted")
	}
	if !strings.Contains(err.Error(), "internal") && !strings.Contains(err.Error(), "审计") {
		t.Logf("rejection error: %v", err)
	}
	if strings.Contains(rawFiles(t, dir), "密钥泄漏") {
		t.Fatal("rejected content reached the log")
	}
	rows := records(t, dir)
	dropped := 0
	for _, row := range rows {
		if row["event"] == "audit_dropped" {
			dropped++
			if _, has := row["args_digest"]; has {
				t.Fatalf("audit_dropped carries arguments: %v", row)
			}
		}
	}
	if dropped != 1 {
		t.Fatalf("want one audit_dropped record, got %d: %v", dropped, rows)
	}
	// Forbidden body-like keys are refused wherever they appear in the digest.
	for _, key := range []string{"content", "old_string", "new_string", "stdout", "stderr", "stdin", "data", "env", "authorization", "pat", "token", "auth_key"} {
		event := readExample(t, "audit-event.json")
		event["args_digest"].(map[string]any)[key] = "sensitive-value"
		if err = l.Write(event); err == nil {
			t.Errorf("args_digest.%s accepted", key)
		}
	}
	if strings.Contains(rawFiles(t, dir), "sensitive-value") {
		t.Fatal("a refused value reached the log")
	}
}

// conformance: CTR-AUD-01
func Test_CTRAUD01_EventKindsTimeFormatAndFilePermissions(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	args := json.RawMessage(`{"session_id":"t1","data":"x","signal":"INT"}`)
	for tool, result := range map[string]any{
		"pty.open":     map[string]any{"session_id": "t1"},
		"pty.write":    map[string]any{"written": 1},
		"pty.signal":   map[string]any{"signaled": true},
		"pty.transfer": map[string]any{"owner": "x"},
		"fs.read":      map[string]any{"resolved_path": "/tmp/a"},
	} {
		if err = l.Record("node/s", "claude", "device", tool, args, result, false, 42*time.Millisecond); err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
	}
	if err = l.Record("node/s", "claude", "device", "fs.write", json.RawMessage(`{}`), map[string]any{"error": "unauthorized"}, true, 0); err != nil {
		t.Fatal(err)
	}
	// pty.list/resize/read are plain tool_call records, close flushes.
	if err = l.Record("node/s", "", "device", "pty.close", args, map[string]any{"closed": true}, false, 0); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	timePattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	for _, row := range records(t, dir) {
		event, _ := row["event"].(string)
		seen[event] = true
		if !timePattern.MatchString(row["time"].(string)) {
			t.Fatalf("time is not RFC3339 UTC with milliseconds: %v", row["time"])
		}
		if row["status"] != "ok" && row["status"] != "error" {
			t.Fatalf("status: %v", row)
		}
		if row["status"] == "ok" && row["error"] != nil {
			t.Fatalf("ok record with an error code: %v", row)
		}
		if _, ok := row["duration_ms"].(float64); !ok {
			t.Fatalf("duration_ms is not a number: %v", row)
		}
	}
	for _, event := range []string{"pty_open", "pty_close", "pty_transfer", "pty_signal", "pty_write", "tool_call", "authz_deny"} {
		if !seen[event] {
			t.Errorf("event %q never written; saw %v", event, seen)
		}
	}
	if runtime.GOOS == "windows" {
		// POSIX permission bits are not meaningful on Windows (Stat reports 0666/0777).
		return
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, file := range files {
		if info, err := os.Stat(file); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("audit file mode: %v %v", info, err)
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("audit directory mode: %v %v", info, err)
	}
}

// conformance: CON-SEC-07 CTR-AUD-01
func Test_CONSEC07_NoPTYContentInAudit(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const typed, shown = "TYPED-SECRET-123", "OUTPUT-SECRET-456"
	write, _ := json.Marshal(map[string]any{"session_id": "t1", "data": typed})
	read, _ := json.Marshal(map[string]any{"session_id": "t1", "cursor": 0})
	closeArgs, _ := json.Marshal(map[string]any{"session_id": "t1"})
	steps := []struct {
		tool   string
		args   json.RawMessage
		result any
	}{
		{"node/pty.write", write, map[string]any{"written": len(typed)}},
		{"node/pty.read", read, map[string]any{"data": shown, "next_cursor": len(shown)}},
		{"node/pty.read", read, map[string]any{"data": shown, "next_cursor": 2 * len(shown)}},
		{"node/pty.close", closeArgs, map[string]any{"closed": true}},
	}
	for _, step := range steps {
		if err = l.Record("node/s", "claude", "device", step.tool, step.args, step.result, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	text := rawFiles(t, dir)
	for _, secret := range []string{typed, shown} {
		if strings.Contains(text, secret) {
			t.Fatalf("PTY content %q reached the audit log:\n%s", secret, text)
		}
	}
	var summary, writeRecord map[string]any
	for _, row := range records(t, dir) {
		switch row["event"] {
		case "pty_io":
			summary = row
		case "pty_write":
			writeRecord = row
		}
	}
	if summary == nil || writeRecord == nil {
		t.Fatalf("pty_io or pty_write record missing: %s", text)
	}
	if digest := summary["args_digest"].(map[string]any); digest["bytes"] != float64(2*len(shown)) {
		t.Fatalf("pty_io must carry the byte count only: %v", digest)
	}
	if digest := writeRecord["args_digest"].(map[string]any); digest["bytes"] != float64(len(typed)) {
		t.Fatalf("pty_write must carry the byte count only: %v", digest)
	}
}

// conformance: CON-SEC-08
func Test_CONSEC08_ExpiredAuditFilesAreDeletedNotHidden(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	old, fresh := filepath.Join(dir, "20200101T0000.jsonl"), filepath.Join(dir, "20991231T2359.jsonl")
	for _, file := range []string{old, fresh} {
		if err = os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Chtimes(old, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = l.Prune(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired audit file still on disk: %v", err)
	}
	if _, err = os.Stat(fresh); err != nil {
		t.Fatalf("unexpired audit file removed: %v", err)
	}
	// Opening the log (process start) also prunes, without any query.
	stale := filepath.Join(dir, "20190101T0000.jsonl")
	if err = os.WriteFile(stale, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(stale, time.Now().Add(-72*time.Hour), time.Now().Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	again, err := New(dir, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err = os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("expired file survives restart")
	}
}
