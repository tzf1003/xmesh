package control

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/audit"
)

func record(at time.Time, tool, status, errCode string) map[string]any {
	r := map[string]any{"time": at.UTC().Format("2006-01-02T15:04:05.000Z"), "event": "tool_call", "caller": "n1/s", "device": "ctl-node", "tool": tool, "args_digest": map[string]any{"keys": []string{"cmd"}}, "status": status, "duration_ms": 3}
	if status == "error" {
		r["error"] = errCode
	}
	return r
}

// auditTools lists the tool field of every event in an audit response, in order.
func auditTools(t *testing.T, r *Response) (tools []string, next *string) {
	t.Helper()
	var out v2.AuditResponse
	decodeAs(t, r, &out)
	for _, raw := range out.Events {
		var e struct{ Tool string }
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		tools = append(tools, e.Tool)
	}
	return tools, out.NextCursor
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
	list := func(query string) []string {
		t.Helper()
		tools, _ := auditTools(t, f.ok("GET", "/v2/audit"+query, nil, "AuditResponse"))
		return tools
	}
	var all v2.AuditResponse
	decodeAs(t, f.ok("GET", "/v2/audit", nil, "AuditResponse"), &all)
	if !all.Enabled || all.NextCursor != nil {
		t.Fatalf("%+v", all)
	}
	if got := strings.Join(list(""), ","); got != "exec.run5,exec.run4,exec.run3,exec.run2,exec.run1,exec.run0" {
		t.Fatalf("newest-first order: %s", got)
	}
	if got := list("?status=error"); len(got) != 2 {
		t.Fatalf("status filter: %v", got)
	}
	if got := list("?tool=run3"); len(got) != 1 {
		t.Fatalf("tool filter: %v", got)
	}
	// a zone offset such as +08:00 carries a '+' that a raw query string would read as a space
	since := url.QueryEscape(base.Add(2 * time.Second).Format(time.RFC3339Nano))
	until := url.QueryEscape(base.Add(4 * time.Second).Format(time.RFC3339Nano))
	if got := list("?since=" + since + "&until=" + until); len(got) != 3 {
		t.Fatalf("time filter: %v", got)
	}
	// paging walks all six records exactly once
	seen, cursor := []string{}, ""
	for page := 0; page < 10; page++ {
		path := "/v2/audit?limit=4"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		tools, next := auditTools(t, f.ok("GET", path, nil, "AuditResponse"))
		seen = append(seen, tools...)
		if next == nil {
			break
		}
		cursor = *next
	}
	if strings.Join(seen, ",") != "exec.run5,exec.run4,exec.run3,exec.run2,exec.run1,exec.run0" {
		t.Fatalf("paging: %v", seen)
	}
	for _, bad := range []string{"limit=0", "limit=5000", "since=yesterday", "status=maybe", "cursor=../../etc/passwd:1", "cursor=nope"} {
		apiError(t, f.do("GET", "/v2/audit?"+bad, nil, nil), v2.ErrInvalidArgs)
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
	r := f.ok("GET", "/v2/audit", nil, "AuditResponse")
	var got v2.AuditResponse
	decodeAs(t, r, &got)
	if len(got.Events) != 1 || strings.Contains(string(r.Body), "SECRET") {
		t.Fatalf("content leaked or good record lost: %s", r.Body)
	}
}

// With no audit directory the view is empty and says so.
func Test_ControlAuditDisabled(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.AuditDir = "" })
	var got v2.AuditResponse
	decodeAs(t, f.ok("GET", "/v2/audit", nil, "AuditResponse"), &got)
	if got.Enabled || len(got.Events) != 0 || got.NextCursor != nil {
		t.Fatalf("%+v", got)
	}
	// a directory that nothing has written to yet is enabled and empty (the first node must be
	// gone: the channel's address is derived from the node name)
	f.server.Close()
	f2 := newFixture(t)
	decodeAs(t, f2.ok("GET", "/v2/audit", nil, "AuditResponse"), &got)
	if !got.Enabled || len(got.Events) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Every non-GET request is audited as control_call with the route id as the tool and only key
// names and sizes as arguments; reads are not (CTR-CTL-02).
func Test_ControlCallsAreAudited(t *testing.T) {
	var log *audit.Logger
	f := newFixture(t, func(o *Options) {
		var err error
		if log, err = audit.New(o.AuditDir, 0); err != nil {
			t.Fatal(err)
		}
		o.Audit = log
	})
	defer log.Close()
	f.ok("GET", "/v2/status", nil, "StatusResponse")
	f.ok("POST", "/v2/drain", map[string]string{"mode": "keep"}, "DrainResponse")
	apiError(t, f.do("POST", "/v2/drain", map[string]string{"mode": "bogus"}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("POST", "/v2/pair/import", v2.PairImportRequest{Fingerprint: "SECRET-FP"}, nil), v2.ErrFingerprintMismatch)
	r := f.ok("GET", "/v2/audit?event=control_call", nil, "AuditResponse")
	var got v2.AuditResponse
	decodeAs(t, r, &got)
	if len(got.Events) != 3 {
		t.Fatalf("%s", r.Body)
	}
	type row struct {
		Event, Actor, Caller, Tool, Status, Error string
		Digest                                    struct {
			Keys  []string
			Bytes int
		} `json:"args_digest"`
	}
	byTool := map[string]row{}
	for _, raw := range got.Events {
		var rec row
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Event != "control_call" || rec.Actor != "control" || !strings.HasPrefix(rec.Caller, "control:") {
			t.Fatalf("not attributed to the control channel: %s", raw)
		}
		byTool[rec.Tool+"/"+rec.Status] = rec
	}
	ok := byTool["control.drain.start/ok"]
	if len(ok.Digest.Keys) != 1 || ok.Digest.Keys[0] != "mode" || ok.Digest.Bytes == 0 || byTool["control.drain.start/error"].Error != "invalid_args" || byTool["control.pair.import/error"].Error != "fingerprint_mismatch" {
		t.Fatalf("%+v", byTool)
	}
	for _, raw := range got.Events {
		if strings.Contains(string(raw), "keep") || strings.Contains(string(raw), "bogus") || strings.Contains(string(raw), "SECRET-FP") {
			t.Fatalf("a request value reached the audit record: %s", raw)
		}
	}
}
