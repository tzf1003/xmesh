package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/config"
	"go.yaml.in/yaml/v3"
)

// restartPrefixes mirror config.Store: these sections only apply after a restart.
var restartPrefixes = []string{"/node", "/mesh", "/gateway/listen", "/ingress"}

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

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request, _ *call) {
	path := s.o.Store.Path()
	match := r.Header.Get("If-Match")
	if strings.TrimSpace(match) == "" {
		failWith(w, newError(v1.ErrPreconditionNeeded, "修改配置必须带 If-Match"))
		return
	}
	var req v1.ConfigPutRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(path)
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	if current := etagOf(old); strings.Trim(strings.TrimSpace(match), `"`) != current {
		e := newError(v1.ErrPreconditionFailed, "配置文件已被修改，请重新读取后再保存")
		e.CurrentETag = current
		failWith(w, e)
		return
	}
	next := []byte(req.Content)
	if _, err = config.Parse(next); err != nil {
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
	applied := !req.DryRun && !bytes.Equal(old, next)
	if !applied {
		writeJSON(w, 200, v1.ConfigPutResponse{Applied: false, ETag: etagOf(old), Diff: diff, RestartRequired: restart})
		return
	}
	if err = writeAtomic(path, next); err != nil {
		failWith(w, newError(v1.ErrInternal, "写入配置文件失败"))
		return
	}
	s.digest = sum(next)
	s.lastParsed = parsedOf(next)
	// Reload now so restart_required is the store's own verdict, not the prefix heuristic.
	s.o.Store.Get()
	restart, s.failures = s.o.Store.State()
	etag := etagOf(next)
	paths := make([]string, len(diff))
	for i, c := range diff {
		paths[i] = c.Pointer
	}
	_ = s.bus.Publish(v1.EventConfig, v1.ConfigEventData{Change: "written", Actor: v1.ActorControl, ETag: &etag, RestartRequired: restart, ChangedPaths: paths})
	w.Header().Set("ETag", `"`+etag+`"`)
	writeJSON(w, 200, v1.ConfigPutResponse{Applied: true, ETag: etag, Diff: diff, RestartRequired: restart})
}

// watchConfigFile announces edits made outside the channel (editor, CLI).
func (s *Server) watchConfigFile() {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	now := s.fileDigest()
	if now == "" || now == s.digest {
		return
	}
	s.digest = now
	raw, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		return
	}
	etag := etagOf(raw)
	s.o.Store.Get()
	restart, failures := s.o.Store.State()
	data := v1.ConfigEventData{Change: "reloaded", Actor: v1.ActorFile, ETag: &etag, RestartRequired: restart, ChangedPaths: []string{}}
	if failures > s.failures {
		data.Change = "reload_failed"
	} else if parsed := parsedOf(raw); parsed != nil {
		for _, c := range diffConfigs(s.lastParsed, parsed) {
			data.ChangedPaths = append(data.ChangedPaths, c.Pointer)
		}
		s.lastParsed = parsed
	}
	s.failures = failures
	_ = s.bus.Publish(v1.EventConfig, data)
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
		out[i].RestartRequired = hasPrefix(out[i].Pointer, restartPrefixes...)
		out[i].WeakensSecurity = weakensSecurity(out[i], before, after)
	}
	if out == nil {
		out = []v1.ConfigChange{}
	}
	return out
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
