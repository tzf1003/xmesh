package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// Fields CTR-AUD-01 allows. A record carrying anything else is withheld from
// the view and counted, so a bug elsewhere cannot leak content through here
// (INV-10).
var auditFields = map[string]bool{"time": true, "event": true, "caller": true, "oauth_client": true, "oauth_session": true, "device": true, "tool": true, "args_digest": true, "status": true, "error": true, "duration_ms": true, "output_sha256": true, "actor": true}
var digestFields = map[string]bool{"keys": true, "resolved_path": true, "cmd": true, "args_len": true, "stdin_sha256": true, "session_id": true, "signal": true, "mode": true, "backend": true, "token": true, "value_len": true, "value_sha256": true, "bytes": true}

const (
	defaultAuditLimit = 100
	maxAuditLimit     = 1000
)

type auditQuery struct {
	since, until                       time.Time
	tool, device, caller, status, kind string
	limit                              int
	cursor                             string
}

// auditList serves GET /v1/audit (CTR-CTL-11): CTR-AUD-01 events, newest first, passed through
// without added fields.
func (s *Server) auditList(w http.ResponseWriter, r *http.Request, _ *call) {
	q := auditQuery{limit: defaultAuditLimit}
	v := r.URL.Query()
	var err error
	for name, dst := range map[string]*time.Time{"since": &q.since, "until": &q.until} {
		if text := v.Get(name); text != "" {
			if *dst, err = time.Parse(time.RFC3339Nano, text); err != nil {
				failWith(w, argError(name, "时间必须是 RFC3339"))
				return
			}
		}
	}
	if text := v.Get("limit"); text != "" {
		if q.limit, err = strconv.Atoi(text); err != nil || q.limit < 1 || q.limit > maxAuditLimit {
			failWith(w, argError("limit", "limit 必须在 1 到 1000 之间"))
			return
		}
	}
	q.tool, q.device, q.caller, q.status, q.kind, q.cursor = v.Get("tool"), v.Get("device"), v.Get("caller"), v.Get("status"), v.Get("event"), v.Get("cursor")
	if q.status != "" && q.status != "ok" && q.status != "error" {
		failWith(w, argError("status", "status 只能是 ok 或 error"))
		return
	}
	out := v1.AuditResponse{Events: []json.RawMessage{}}
	if s.o.AuditDir == "" {
		writeJSON(w, 200, out)
		return
	}
	out.Enabled = true
	records, next, _, err := readAudit(s.o.AuditDir, q)
	var invalid *v1.Error
	switch {
	case errors.As(err, &invalid):
		failWith(w, invalid)
		return
	case os.IsNotExist(err):
		records, next = nil, ""
	case err != nil:
		failWith(w, newError(v1.ErrInternal, "无法读取审计目录"))
		return
	}
	for _, record := range records {
		raw, _ := json.Marshal(record)
		out.Events = append(out.Events, raw)
	}
	if next != "" {
		out.NextCursor = &next
	}
	writeJSON(w, 200, out)
}

// readAudit returns up to q.limit records, newest first. The cursor is
// "<file>:<line>" and resumes strictly before that line.
func readAudit(dir string, q auditQuery) (records []map[string]any, next string, rejected int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", 0, err
	}
	files := []string{}
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".jsonl") {
			files = append(files, ent.Name())
		}
	}
	sort.Strings(files)
	startFile, startLine := "", -1
	if q.cursor != "" {
		name, line, ok := strings.Cut(q.cursor, ":")
		n, convErr := strconv.Atoi(line)
		if !ok || convErr != nil || n < 0 || filepath.Base(name) != name {
			return nil, "", 0, argError("cursor", "游标无效")
		}
		startFile, startLine = name, n
	}
	records = []map[string]any{}
	for i := len(files) - 1; i >= 0; i-- {
		name := files[i]
		if startFile != "" && name > startFile {
			continue
		}
		// A file holds records written during its minute; a record's own time can be
		// slightly earlier (it is stamped before the write), so keep a minute of slack.
		stamp, perr := time.Parse("20060102T1504", strings.TrimSuffix(name, ".jsonl"))
		if perr == nil && ((!q.until.IsZero() && stamp.Add(-time.Minute).After(q.until)) || (!q.since.IsZero() && stamp.Add(2*time.Minute).Before(q.since))) {
			continue
		}
		lines, rerr := readLines(filepath.Join(dir, name))
		if rerr != nil {
			return nil, "", 0, rerr
		}
		from := len(lines) - 1
		if name == startFile {
			from = min(from, startLine-1)
		}
		for n := from; n >= 0; n-- {
			var record map[string]any
			if json.Unmarshal([]byte(lines[n]), &record) != nil || !allowedRecord(record) {
				rejected++
				continue
			}
			if !q.match(record) {
				continue
			}
			if len(records) == q.limit {
				return records, name + ":" + strconv.Itoa(n+1), rejected, nil
			}
			records = append(records, record)
		}
	}
	return records, "", rejected, nil
}
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if text := strings.TrimSpace(sc.Text()); text != "" {
			lines = append(lines, text)
		}
	}
	return lines, sc.Err()
}
func allowedRecord(record map[string]any) bool {
	for key, value := range record {
		if !auditFields[key] {
			return false
		}
		if key == "args_digest" {
			digest, ok := value.(map[string]any)
			if !ok {
				return false
			}
			for k := range digest {
				if !digestFields[k] {
					return false
				}
			}
		}
	}
	return true
}
func (q auditQuery) match(record map[string]any) bool {
	text := func(key string) string { s, _ := record[key].(string); return s }
	if q.tool != "" && !strings.Contains(text("tool"), q.tool) || q.device != "" && text("device") != q.device || q.caller != "" && text("caller") != q.caller || q.status != "" && text("status") != q.status || q.kind != "" && text("event") != q.kind {
		return false
	}
	if q.since.IsZero() && q.until.IsZero() {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, text("time"))
	if err != nil {
		return false
	}
	return (q.since.IsZero() || !at.Before(q.since)) && (q.until.IsZero() || !at.After(q.until))
}
