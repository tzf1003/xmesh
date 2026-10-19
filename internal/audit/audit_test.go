package audit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func Test_FileTransferAuditSummaries(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	secret := "TRANSFER_PRIVATE_CONTENT"
	data := base64.StdEncoding.EncodeToString([]byte(secret))
	args, _ := json.Marshal(map[string]any{"path": "artifact", "data": data, "sha256": hash([]byte(secret)), "force": true})
	for _, tool := range []string{"fs.upload", "fs.download"} {
		out := map[string]any{"resolved_path": "/tmp/artifact", "encoding": "base64", "data": data, "sha256": hash([]byte(secret)), "bytes": len(secret)}
		if err := l.Record("node/session", "client", "node", tool, args, out, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	rows := records(t, dir)
	if len(rows) != 2 {
		t.Fatal("transfer audit missing", len(rows))
	}
	for _, row := range rows {
		body, _ := json.Marshal(row)
		if bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte(data)) || row["event"] == "audit_dropped" {
			t.Fatal("transfer body appeared in audit")
		}
	}
}

func records(t *testing.T, dir string) []map[string]any {
	t.Helper()
	files, e := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	var result []map[string]any
	for _, file := range files {
		b, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(b), []byte{'\n'}) {
			var row map[string]any
			if e := json.Unmarshal(line, &row); e != nil {
				t.Fatal(e)
			}
			result = append(result, row)
		}
	}
	return result
}

// conformance: CTR-AUD-01
func Test_CTRAUD01_PTYSummaryFlush(t *testing.T) {
	for _, mode := range []string{"timer", "session-close", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			l, e := New(dir, time.Hour)
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			args := json.RawMessage(`{"session_id":"terminal"}`)
			for _, data := range []string{"one", "two"} {
				if e = l.Record("gateway/session", "client", "device", "node/pty.read", args, map[string]any{"data": data}, false, 0); e != nil {
					t.Fatal(e)
				}
			}
			if len(records(t, dir)) != 0 {
				t.Fatal("read emitted before aggregation interval")
			}
			switch mode {
			case "timer":
				if e = l.flush(time.Now().Add(time.Minute), false, "", ""); e != nil {
					t.Fatal(e)
				}
			case "session-close":
				if e = l.Record("gateway/session", "client", "device", "node/pty.close", args, map[string]any{"closed": true}, false, 0); e != nil {
					t.Fatal(e)
				}
			case "shutdown":
				l.Close()
			}
			rows := records(t, dir)
			if len(rows) == 0 {
				t.Fatal("summary missing")
			}
			r := rows[0]
			if r["event"] != "pty_io" || r["oauth_client"] != "client" || r["tool"] != "node/pty.read" {
				t.Fatal(r)
			}
			digest := r["args_digest"].(map[string]any)
			if digest["bytes"] != float64(6) || digest["session_id"] != "terminal" {
				t.Fatal(digest)
			}
			l.Close()
			if len(records(t, dir)) != len(rows) {
				t.Fatal("duplicate flush")
			}
		})
	}
}

// conformance: CTR-AUD-01
func Test_CTRAUD01_PTYReadFailureRecorded(t *testing.T) {
	dir := t.TempDir()
	l, e := New(dir, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if e = l.Record("node/session", "", "device", "pty.read", json.RawMessage(`{}`), map[string]any{"error": "unauthorized"}, true, 0); e != nil {
		t.Fatal(e)
	}
	r := records(t, dir)
	if len(r) != 1 || r[0]["event"] != "authz_deny" {
		t.Fatal(r)
	}
}

// conformance: INV-10 CON-SEC-08
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
	// The rejected write may cross a minute boundary into the next log file.
	files, e = filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	for _, file := range files {
		b, e = os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(b), secret) {
			t.Fatal("body reached audit")
		}
	}
	rows := records(t, dir)
	events := map[any]int{}
	for _, row := range rows {
		events[row["event"]]++
	}
	if len(rows) != 2 || events["tool_call"] != 1 || events["audit_dropped"] != 1 {
		t.Fatal("audit event counts", events)
	}
	if e = l.Prune(time.Now().Add(2 * time.Hour)); e != nil {
		t.Fatal(e)
	}
	files, _ = filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 0 {
		t.Fatal("expired audit retained")
	}
}
