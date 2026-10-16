package control

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/audit"
)

func record(at time.Time, tool, status, errCode string) map[string]any {
	r := map[string]any{"time": at.UTC().Format("2006-01-02T15:04:05.000Z"), "event": "tool_call", "caller": "n1/s", "device": "ctl-node", "tool": tool, "args_digest": map[string]any{"keys": []string{"cmd"}}, "status": status, "duration_ms": 3}
	if status == "error" {
		r["error"] = errCode
	}
	return r
}

func Test_ControlAuditView(t *testing.T) {
	f := newFixture(t)
	dir := f.server.o.AuditDir
	log, err := audit.New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	base := time.Now().Add(-10 * time.Second).Truncate(time.Millisecond)
	for i := 0; i < 6; i++ {
		status, code := "ok", ""
		if i%3 == 2 {
			status, code = "error", "timeout"
		}
		if err = log.Write(record(base.Add(time.Duration(i)*time.Second), fmt.Sprintf("exec.run%d", i), status, code)); err != nil {
			t.Fatal(err)
		}
	}
	all := decode(t, f.do("GET", "/v1/audit", nil, nil))
	records := all["records"].([]any)
	if len(records) != 6 || records[0].(map[string]any)["tool"] != "exec.run5" || records[5].(map[string]any)["tool"] != "exec.run0" {
		t.Fatalf("newest-first order: %v", records)
	}
	if got := decode(t, f.do("GET", "/v1/audit?status=error", nil, nil))["records"].([]any); len(got) != 2 {
		t.Fatalf("status filter: %v", got)
	}
	if got := decode(t, f.do("GET", "/v1/audit?tool=run3", nil, nil))["records"].([]any); len(got) != 1 {
		t.Fatalf("tool filter: %v", got)
	}
	since := base.Add(2 * time.Second).Format(time.RFC3339Nano)
	until := base.Add(4 * time.Second).Format(time.RFC3339Nano)
	if got := decode(t, f.do("GET", "/v1/audit?since="+since+"&until="+until, nil, nil))["records"].([]any); len(got) != 3 {
		t.Fatalf("time filter: %v", got)
	}
	// paging walks all six records exactly once
	seen, cursor := []string{}, ""
	for page := 0; page < 10; page++ {
		path := "/v1/audit?limit=4"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		got := decode(t, f.do("GET", path, nil, nil))
		for _, r := range got["records"].([]any) {
			seen = append(seen, r.(map[string]any)["tool"].(string))
		}
		if cursor, _ = got["next_cursor"].(string); cursor == "" {
			break
		}
	}
	if strings.Join(seen, ",") != "exec.run5,exec.run4,exec.run3,exec.run2,exec.run1,exec.run0" {
		t.Fatalf("paging: %v", seen)
	}
	for _, bad := range []string{"limit=0", "limit=5000", "since=yesterday", "status=maybe", "cursor=../../etc/passwd:1", "cursor=nope"} {
		if r := f.do("GET", "/v1/audit?"+bad, nil, nil); r.Status != 400 {
			t.Errorf("%s: %d %s", bad, r.Status, r.Body)
		}
	}
}

// conformance: INV-10 the audit view refuses to relay records that carry content
func Test_INV10_AuditViewWithholdsContent(t *testing.T) {
	f := newFixture(t)
	dir := f.server.o.AuditDir
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	good := `{"time":"2026-10-16T10:00:00.000Z","event":"tool_call","caller":"n/s","device":"d","tool":"exec.run","status":"ok","duration_ms":1}`
	lines := []string{
		good,
		`{"time":"2026-10-16T10:00:01.000Z","event":"tool_call","caller":"n/s","device":"d","tool":"exec.run","status":"ok","duration_ms":1,"stdout":"SECRET-OUTPUT"}`,
		`{"time":"2026-10-16T10:00:02.000Z","event":"tool_call","caller":"n/s","device":"d","tool":"exec.run","status":"ok","duration_ms":1,"args_digest":{"keys":["cmd"],"env":{"K":"SECRET-ENV"}}}`,
		`not json SECRET-GARBAGE`,
	}
	if err := os.WriteFile(filepath.Join(dir, "20261016T1000.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := f.do("GET", "/v1/audit", nil, nil)
	got := decode(t, r)
	if len(got["records"].([]any)) != 1 || got["rejected"] != 3.0 {
		t.Fatalf("%s", r.Body)
	}
	if strings.Contains(string(r.Body), "SECRET") {
		t.Fatalf("content leaked: %s", r.Body)
	}
}

func Test_ControlAuditNoDirectory(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.AuditDir = "" })
	if r := f.do("GET", "/v1/audit", nil, nil); r.Status != 404 {
		t.Fatal(r.Status)
	}
}
