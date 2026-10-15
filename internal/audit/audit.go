// Package audit writes schema-checked summaries with bounded retention.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tzf1003/xmesh/docs/contracts/schemas"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	mu        sync.Mutex
	dir       string
	retention time.Duration
	schema    *jsonschema.Schema
	io        map[string]ioSummary
	stop      chan struct{}
	done      chan struct{}
	once      sync.Once
}
type ioSummary struct {
	bytes int
	last  time.Time
}

func New(dir string, retention time.Duration) (*Logger, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	if e := os.Chmod(dir, 0700); e != nil {
		return nil, e
	}
	b, _ := schemas.Files.ReadFile("audit-event.schema.json")
	var doc any
	_ = json.Unmarshal(b, &doc)
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if e := c.AddResource("urn:xmesh:audit", doc); e != nil {
		return nil, e
	}
	schema, e := c.Compile("urn:xmesh:audit")
	if e != nil {
		return nil, e
	}
	l := &Logger{dir: dir, retention: retention, schema: schema, io: map[string]ioSummary{}, stop: make(chan struct{}), done: make(chan struct{})}
	if e = l.Prune(time.Now()); e != nil {
		return nil, e
	}
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-l.stop:
				return
			case now := <-ticker.C:
				_ = l.Prune(now)
			}
		}
	}()
	return l, nil
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Digest(raw json.RawMessage, resolved string) map[string]any {
	var args map[string]json.RawMessage
	_ = json.Unmarshal(raw, &args)
	keys := []string{}
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := map[string]any{"keys": keys, "value_len": len(raw), "value_sha256": hash(raw)}
	if resolved != "" {
		out["resolved_path"] = resolved
	}
	if value := args["cmd"]; value != nil {
		var cmd string
		if json.Unmarshal(value, &cmd) == nil {
			out["cmd"] = policy.Base(cmd)
		}
	}
	if value := args["args"]; value != nil {
		var argv []any
		if json.Unmarshal(value, &argv) == nil {
			out["args_len"] = len(argv)
		}
	}
	if value := args["stdin"]; value != nil {
		var text string
		if json.Unmarshal(value, &text) == nil {
			out["stdin_sha256"] = hash([]byte(text))
		}
	}
	if value := args["data"]; value != nil {
		var text string
		if json.Unmarshal(value, &text) == nil {
			out["bytes"] = len(text)
		}
	}
	return out
}
func (l *Logger) Record(caller, client, device, tool string, args json.RawMessage, result any, isError bool, duration time.Duration) error {
	b, e := json.Marshal(result)
	if e != nil {
		return toolerr.Internal("audit.result", e)
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	resolved, _ := out["resolved_path"].(string)
	event := "tool_call"
	if strings.HasPrefix(tool, "pty.") {
		event = strings.Replace(tool, ".", "_", 1)
	}
	if event == "pty_list" || event == "pty_resize" {
		event = "tool_call"
	}
	if tool == "pty.read" {
		data, _ := out["data"].(string)
		if data == "" {
			return nil
		}
		var params map[string]any
		_ = json.Unmarshal(args, &params)
		key := caller + ":" + fmt.Sprint(params["session_id"])
		l.mu.Lock()
		summary := l.io[key]
		summary.bytes += len(data)
		if !summary.last.IsZero() && time.Since(summary.last) < time.Minute {
			l.io[key] = summary
			l.mu.Unlock()
			return nil
		}
		n := summary.bytes
		l.io[key] = ioSummary{last: time.Now()}
		l.mu.Unlock()
		event = "pty_io"
		args, _ = json.Marshal(map[string]any{"bytes": n})
	}
	var code any
	if isError {
		code = out["error"]
		if code == "unauthorized" || code == "unauthenticated" {
			event = "authz_deny"
		}
	}
	status := "ok"
	if isError {
		status = "error"
	}
	record := map[string]any{"time": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "event": event, "caller": caller, "device": device, "tool": tool, "args_digest": Digest(args, resolved), "status": status, "error": code, "duration_ms": max(int64(0), duration.Milliseconds()), "output_sha256": hash(b)}
	if client != "" {
		record["oauth_client"] = client
	}
	return l.Write(record)
}
func (l *Logger) Write(record map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, e := json.Marshal(record)
	if e != nil {
		return toolerr.Internal("audit.encode", e)
	}
	var normalized any
	_ = json.Unmarshal(b, &normalized)
	if e = l.schema.Validate(normalized); e != nil {
		fallback := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "event": "audit_dropped", "caller": "audit/internal", "device": "local", "tool": "audit.encode", "status": "error", "error": "internal", "duration_ms": 0}
		safe, _ := json.Marshal(fallback)
		_ = l.append(safe)
		return toolerr.New("internal", "审计记录包含未允许的字段", "op", "audit.encode")
	}
	return l.append(b)
}
func (l *Logger) append(b []byte) error {
	path := filepath.Join(l.dir, time.Now().UTC().Format("20060102T1504")+".jsonl")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.Write(append(b, '\n')); e != nil {
		return e
	}
	return f.Sync()
}
func (l *Logger) Prune(now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entries, e := os.ReadDir(l.dir)
	if e != nil {
		return e
	}
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".jsonl") || ent.IsDir() {
			continue
		}
		st, e := ent.Info()
		if e != nil {
			return e
		}
		if now.Sub(st.ModTime()) >= l.retention {
			if e = os.Remove(filepath.Join(l.dir, ent.Name())); e != nil {
				return e
			}
		}
	}
	return nil
}
func (l *Logger) Close() { l.once.Do(func() { close(l.stop); <-l.done }) }
