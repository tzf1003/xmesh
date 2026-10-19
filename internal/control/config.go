package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/config"
	"go.yaml.in/yaml/v3"
)

func hasPrefix(path string, prefixes ...string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request, _ *call) {
	path := s.o.Store.Path()
	raw, err := os.ReadFile(path)
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	s.o.Store.Get() // apply a pending reload so effective_etag is current
	restart, _ := s.o.Store.State()
	etag := etagOf(raw)
	out := v1.ConfigResponse{ETag: etag, Content: string(raw), EffectiveETag: "sha256-" + s.o.Store.Digest(), RestartRequired: restart}
	if _, err = config.Parse(raw); err != nil {
		out.LoadError = loadError(err)
	} else {
		out.Valid, out.Parsed = true, parsedOf(raw)
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	writeJSON(w, 200, out)
}

// applySettleTimeout is how long a reload waits for the roles to follow the new generation.
const applySettleTimeout = 10 * time.Second

// maxConfigEdits bounds one PATCH /v1/config; a form edit needs a handful, a whole file goes through PUT.
const maxConfigEdits = 64

// ifMatch returns the ETag a write names, or reports the missing precondition.
func ifMatch(w http.ResponseWriter, r *http.Request) (string, bool) {
	match := r.Header.Get("If-Match")
	if strings.TrimSpace(match) == "" {
		failWith(w, newError(v1.ErrPreconditionNeeded, "修改配置必须带 If-Match"))
		return "", false
	}
	return strings.Trim(strings.TrimSpace(match), `"`), true
}

// readMatchedLocked returns the configuration file bytes when they still carry the ETag match. The
// caller holds configMu. A failure is reported to w and returns false.
func (s *Server) readMatchedLocked(w http.ResponseWriter, match string) ([]byte, bool) {
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return nil, false
	}
	if current := etagOf(old); match != current {
		e := newError(v1.ErrPreconditionFailed, "配置文件已被修改，请重新读取后再保存")
		e.CurrentETag = current
		failWith(w, e)
		return nil, false
	}
	return old, true
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request, _ *call) {
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req v1.ConfigPutRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, ok := s.readMatchedLocked(w, match)
	if !ok {
		return
	}
	s.saveLocked(w, old, []byte(req.Content), req.DryRun)
}

// patchConfig applies the edits to the file on disk and saves the result like PUT. Either every edit
// applies and the result validates, or nothing is written.
func (s *Server) patchConfig(w http.ResponseWriter, r *http.Request, _ *call) {
	match, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req v1.ConfigEditRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if len(req.Edits) == 0 || len(req.Edits) > maxConfigEdits {
		failWith(w, argError("edits", "edits 需要 1 到 64 项"))
		return
	}
	edits := make([]Edit, len(req.Edits))
	for i, item := range req.Edits {
		if _, err := pointer(item.Path); err != nil {
			failWith(w, argError("path", "path 必须是 JSON Pointer，例如 /agent/backends/name"))
			return
		}
		edits[i] = Edit{Path: item.Path, Value: item.Value, Delete: item.Delete}
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, ok := s.readMatchedLocked(w, match)
	if !ok {
		return
	}
	next, err := ApplyEdits(old, edits)
	if err != nil {
		failWith(w, newError(v1.ErrConflict, "配置结构不允许这项修改"))
		return
	}
	// An edit that changes nothing in the parsed document, such as removing an absent key, writes nothing.
	dryRun := req.DryRun || len(diffConfigs(parsedOf(old), parsedOf(next))) == 0
	s.saveLocked(w, old, next, dryRun)
}

// saveLocked validates next like a whole-file write and, unless it is a dry run or changes nothing,
// stores it. The caller holds configMu and has matched old against If-Match.
func (s *Server) saveLocked(w http.ResponseWriter, old, next []byte, dryRun bool) {
	if _, err := config.Parse(next); err != nil {
		e := newError(v1.ErrConfigInvalid, "配置未通过校验，没有写入")
		e.Diagnostics = diagnostics(err)
		failWith(w, e)
		return
	}
	diff := diffConfigs(parsedOf(old), parsedOf(next))
	restart, _ := s.o.Store.State()
	for _, c := range diff {
		restart = restart || c.RestartRequired
	}
	applied := !dryRun && !bytes.Equal(old, next)
	if !applied {
		writeJSON(w, 200, v1.ConfigPutResponse{Applied: false, ETag: etagOf(old), Diff: diff, RestartRequired: restart})
		return
	}
	etag, restart, err := s.storeConfigLocked(next, diff)
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "写入配置文件失败"))
		return
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	writeJSON(w, 200, v1.ConfigPutResponse{Applied: true, ETag: etag, Diff: diff, RestartRequired: restart})
}

// storeConfigLocked replaces the configuration file with next, which the caller has validated and
// diffed against the current content, and announces it on the event stream. The caller holds
// configMu. It returns the new ETag and the store's own restart_required verdict.
func (s *Server) storeConfigLocked(next []byte, diff []v1.ConfigChange) (etag string, restart bool, err error) {
	if err = writeAtomic(s.o.Store.Path(), next); err != nil {
		return "", false, err
	}
	s.digest = sum(next)
	before := s.lastParsed
	s.lastParsed = parsedOf(next)
	s.pruneBackendEnv(before, s.lastParsed)
	// Reload now so restart_required is the store's own verdict, not the prefix heuristic.
	s.o.Store.Get()
	restart, s.failures = s.o.Store.State()
	etag = etagOf(next)
	paths := make([]string, len(diff))
	for i, c := range diff {
		paths[i] = c.Pointer
	}
	_ = s.bus.Publish(v1.EventConfig, v1.ConfigEventData{Change: "written", Actor: v1.ActorControl, ETag: &etag, RestartRequired: restart, ChangedPaths: paths})
	return etag, restart, nil
}

// watchConfigFile announces edits made outside the channel (editor, CLI).
func (s *Server) watchConfigFile() {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	now := s.fileDigest()
	if now == "" || now == s.digest {
		return
	}
	_, _ = s.adoptFileLocked(context.Background(), v1.ActorFile, false)
}

// reloadConfig re-reads the configuration file now. The UI calls it after it has written
// the file by other means, or to see the outcome without waiting for the watcher's next tick.
func (s *Server) reloadConfig(w http.ResponseWriter, r *http.Request, _ *call) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	out, err := s.adoptFileLocked(r.Context(), v1.ActorControl, true)
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	writeJSON(w, 200, out)
}

// adoptFileLocked reads and adopts one store snapshot and announces
// what changed on the event stream. A valid new content becomes the generation in effect
// (restart-only sections keep their running values); invalid content keeps the last good
// one and is reported as load_error. Roles that start, stop or move with the new generation are
// given a short while to do so, and what still failed is reported as apply_errors; retry tries again
// what failed last time. The caller holds configMu.
func (s *Server) adoptFileLocked(ctx context.Context, actor v1.Actor, retry bool) (v1.ConfigReloadResponse, error) {
	snapshot := s.o.Store.RefreshSnapshot()
	if snapshot.ReadError != nil {
		return v1.ConfigReloadResponse{}, snapshot.ReadError
	}
	raw := snapshot.Raw
	changed := sum(raw) != s.digest
	s.digest = sum(raw)
	adopted := snapshot.Adopted
	failures := snapshot.Failures
	failed := failures > s.failures
	s.failures = failures
	pending := snapshot.RestartPaths
	etag := etagOf(raw)
	out := v1.ConfigReloadResponse{Applied: adopted, ETag: etag, EffectiveETag: "sha256-" + snapshot.EffectiveDigest, ChangedPaths: []string{}, RestartRequired: len(pending) > 0, RestartPaths: pending, ApplyErrors: []v1.ApplyError{}}
	if s.o.Applier != nil {
		settle, cancel := context.WithTimeout(ctx, applySettleTimeout)
		if errs := s.o.Applier.Settle(settle, retry); errs != nil {
			out.ApplyErrors = errs
		}
		cancel()
	}
	if _, err := config.Parse(raw); err != nil {
		out.LoadError = loadError(err)
	} else {
		out.Valid = true
	}
	// Content that failed before is not counted again, but it is still a failed reload.
	invalid := out.LoadError != nil
	if !changed && !failed {
		return out, nil
	}
	data := v1.ConfigEventData{Change: "reloaded", Actor: actor, ETag: &etag, RestartRequired: out.RestartRequired, ChangedPaths: []string{}}
	if invalid || failed {
		data.Change = "reload_failed"
	} else if parsed := parsedOf(raw); parsed != nil {
		for _, c := range diffConfigs(s.lastParsed, parsed) {
			out.ChangedPaths = append(out.ChangedPaths, c.Pointer)
		}
		s.lastParsed = parsed
		data.ChangedPaths = out.ChangedPaths
	}
	_ = s.bus.Publish(v1.EventConfig, data)
	return out, nil
}

// parsedOf is the YAML document as the JSON data model, or nil if it does not parse.
func parsedOf(raw []byte) map[string]any {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

var yamlLine = regexp.MustCompile(`line (\d+)`)

// diagnostics turns a config.Parse error into one entry per failing location.
func diagnostics(err error) []v1.Diagnostic {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		out := []v1.Diagnostic{}
		var walk func(u *jsonschema.OutputUnit)
		walk = func(u *jsonschema.OutputUnit) {
			if u.Error != nil && len(out) < 20 {
				out = append(out, v1.Diagnostic{Pointer: u.InstanceLocation, Message: oneLine(u.Error.String())})
			}
			for i := range u.Errors {
				walk(&u.Errors[i])
			}
		}
		walk(ve.DetailedOutput())
		if len(out) > 0 {
			return out
		}
	}
	d := v1.Diagnostic{Message: oneLine(err.Error())}
	if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
		d.Line, _ = strconv.Atoi(m[1])
	}
	return []v1.Diagnostic{d}
}

func loadError(err error) *v1.ConfigLoadError {
	d := diagnostics(err)[0]
	out := &v1.ConfigLoadError{Message: d.Message}
	if d.Pointer != "" {
		out.Message = d.Pointer + ": " + d.Message
	}
	if d.Line > 0 {
		out.Line = &d.Line
	}
	if d.Column > 0 {
		out.Column = &d.Column
	}
	return out
}

func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

// writeAtomic replaces path with data via a temporary file in the same directory,
// keeping the file's mode (0600 for a new one).
func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// diffConfigs lists the RFC 6902 style operations that turn before into after, as the
// contract's ConfigChange rows (CTR-CTL-07). Arrays are compared element by element.
func diffConfigs(before, after map[string]any) []v1.ConfigChange {
	var out []v1.ConfigChange
	if before == nil {
		before = map[string]any{}
	}
	if after == nil {
		after = map[string]any{}
	}
	diffValue("", before, after, &out)
	for i := range out {
		out[i].RestartRequired = restartNeeded(out[i])
		out[i].WeakensSecurity = weakensSecurity(out[i], before, after)
	}
	if out == nil {
		out = []v1.ConfigChange{}
	}
	return out
}

// restartNeeded reports whether the change falls under config.RestartPaths. node.roles is judged by
// the roles that were added or removed: those in config.HotRoles come and go without a restart.
func restartNeeded(c v1.ConfigChange) bool {
	if c.Pointer != "/node/roles" {
		return config.NeedsRestart(c.Pointer)
	}
	var before, after []string
	_ = json.Unmarshal(c.Before, &before)
	_ = json.Unmarshal(c.After, &after)
	return config.RolesNeedRestart(before, after)
}

func escapePointer(key string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(key)
}

func diffValue(ptr string, a, b any, out *[]v1.ConfigChange) {
	raw := func(v any) json.RawMessage { r, _ := json.Marshal(v); return r }
	switch x := a.(type) {
	case map[string]any:
		if y, ok := b.(map[string]any); ok {
			keys := map[string]struct{}{}
			for k := range x {
				keys[k] = struct{}{}
			}
			for k := range y {
				keys[k] = struct{}{}
			}
			for _, k := range sortedKeys(keys) {
				from, inA := x[k]
				to, inB := y[k]
				switch {
				case inA && inB:
					diffValue(ptr+"/"+escapePointer(k), from, to, out)
				case inA:
					*out = append(*out, v1.ConfigChange{Pointer: ptr + "/" + escapePointer(k), Op: "remove", Before: raw(from)})
				default:
					*out = append(*out, v1.ConfigChange{Pointer: ptr + "/" + escapePointer(k), Op: "add", After: raw(to)})
				}
			}
			return
		}
	case []any:
		if y, ok := b.([]any); ok && ptr == "/node/roles" {
			// The roles are a set: which of them need a restart does not depend on their order, so the
			// list is one change rather than one per position.
			if !reflect.DeepEqual(x, y) {
				*out = append(*out, v1.ConfigChange{Pointer: ptr, Op: "replace", Before: raw(a), After: raw(b)})
			}
			return
		}
		if y, ok := b.([]any); ok {
			n := min(len(x), len(y))
			for i := 0; i < n; i++ {
				diffValue(ptr+"/"+strconv.Itoa(i), x[i], y[i], out)
			}
			for i := n; i < len(y); i++ {
				*out = append(*out, v1.ConfigChange{Pointer: ptr + "/" + strconv.Itoa(i), Op: "add", After: raw(y[i])})
			}
			for i := len(x) - 1; i >= n; i-- {
				*out = append(*out, v1.ConfigChange{Pointer: ptr + "/" + strconv.Itoa(i), Op: "remove", Before: raw(x[i])})
			}
			return
		}
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, v1.ConfigChange{Pointer: ptr, Op: "replace", Before: raw(a), After: raw(b)})
	}
}

// weakensSecurity marks changes the UI should confirm (CTR-CTL-07). It is a UI courtesy, not an
// authorization boundary: the kernel writes the change either way, and any process of the same
// user could edit the file directly. It errs towards asking.
func weakensSecurity(c v1.ConfigChange, before, after map[string]any) bool {
	changed := c.Op == "add" || c.Op == "replace"
	switch {
	case c.Pointer == "/agent/policy/allow_dangerous":
		return string(c.After) == "true"
	case hasPrefix(c.Pointer, "/agent/cwd_policy/allow"):
		return changed
	case hasPrefix(c.Pointer, "/agent/cwd_policy/deny_file"):
		return changed
	case hasPrefix(c.Pointer, "/agent/cwd_policy/deny"):
		return c.Op == "remove" || c.Op == "replace"
	case hasPrefix(c.Pointer, "/agent/authz"):
		return changed
	case hasPrefix(c.Pointer, "/gateway/pats"):
		if changed {
			return true
		}
		return len(patsOf(before)) > 0 && len(patsOf(after)) == 0
	case hasPrefix(c.Pointer, "/ingress/listen"):
		return true
	case c.Pointer == "/ingress":
		return changed
	}
	return false
}

func patsOf(doc map[string]any) map[string]any {
	gateway, _ := doc["gateway"].(map[string]any)
	pats, _ := gateway["pats"].(map[string]any)
	return pats
}
