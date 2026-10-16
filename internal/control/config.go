package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"go.yaml.in/yaml/v3"
)

// maxConfigBytes bounds a PUT /v1/config body.
const maxConfigBytes = 1 << 20

// Edit changes one value of the configuration file. Path is a JSON Pointer such
// as /agent/policy/allow_dangerous; arrays are replaced as a whole.
type Edit struct {
	Path   string `json:"path"`
	Value  any    `json:"value"`
	Delete bool   `json:"delete"`
}
type Change struct {
	Path      string `json:"path"`
	Before    any    `json:"before"`
	After     any    `json:"after"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// sensitivePrefixes mark changes the UI should confirm. The kernel does not
// block them: any process of the same user can edit the file directly.
var sensitivePrefixes = []string{"/agent/policy", "/agent/cwd_policy", "/agent/authz", "/agent/backends", "/gateway/pats", "/ingress"}

// restartPrefixes mirror config.Store: these sections only apply after a restart.
var restartPrefixes = []string{"/node", "/mesh", "/gateway/listen", "/ingress"}

func hasPrefix(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	path := s.o.Store.Path()
	if path == "" {
		fail(w, 404, toolerr.New("not_found", "此节点没有配置文件", "name", "config"))
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fail(w, 500, toolerr.Internal("config.read", err))
		return
	}
	restart, failures := s.o.Store.State()
	w.Header().Set("ETag", etagOf(raw))
	writeJSON(w, 200, map[string]any{"path": path, "etag": etagOf(raw), "yaml": string(raw), "config": s.o.Store.Get(), "restart_required": restart, "policy_reload_failures": failures})
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	path := s.o.Store.Path()
	if path == "" {
		fail(w, 404, toolerr.New("not_found", "此节点没有配置文件", "name", "config"))
		return
	}
	match := r.Header.Get("If-Match")
	if match == "" {
		fail(w, 428, toolerr.Args("If-Match"))
		return
	}
	var req struct {
		YAML   *string `json:"yaml"`
		Set    []Edit  `json:"set"`
		DryRun bool    `json:"dry_run"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || (req.YAML == nil) == (len(req.Set) == 0) {
		fail(w, 400, toolerr.Args("body"))
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(path)
	if err != nil {
		fail(w, 500, toolerr.Internal("config.read", err))
		return
	}
	if unquote(match) != sum(old) {
		fail(w, 412, toolerr.New("conflict", "配置文件已被修改，请重新读取", "path", path, "current_sha256", sum(old)))
		return
	}
	next := []byte(nil)
	if req.YAML != nil {
		next = []byte(*req.YAML)
	} else if next, err = applyEdits(old, req.Set); err != nil {
		fail(w, 400, toolerr.New("invalid_args", "无法应用修改: "+err.Error(), "field", "set"))
		return
	}
	updated, err := config.Parse(next)
	if err != nil {
		s.audit("control.config.put", "invalid_args", nil, started)
		fail(w, 400, toolerr.New("invalid_args", clip("配置无效: "+err.Error(), 2000), "field", "config"))
		return
	}
	before, err := config.Parse(old)
	if err != nil {
		// The file on disk is already invalid (the node kept the last good generation).
		before = s.o.Store.Get()
	}
	changes := diffConfigs(before, updated)
	keys := make([]string, len(changes))
	for i, c := range changes {
		keys[i] = c.Path
	}
	restart, _ := s.o.Store.State()
	for _, c := range changes {
		restart = restart || hasPrefix(c.Path, restartPrefixes)
	}
	if req.DryRun || bytes.Equal(old, next) {
		writeJSON(w, 200, map[string]any{"applied": false, "etag": etagOf(old), "changes": changes, "restart_required": restart})
		return
	}
	if err = writeAtomic(path, next); err != nil {
		s.audit("control.config.put", "internal", keys, started)
		fail(w, 500, toolerr.Internal("config.write", err))
		return
	}
	s.digest = sum(next)
	// Reload now so restart_required is the store's own verdict, not the heuristic.
	s.o.Store.Get()
	restart, _ = s.o.Store.State()
	s.audit("control.config.put", "", keys, started)
	_ = s.bus.Publish("config", map[string]any{"etag": etagOf(next), "source": "control", "restart_required": restart, "changes": len(changes)})
	w.Header().Set("ETag", etagOf(next))
	writeJSON(w, 200, map[string]any{"applied": true, "etag": etagOf(next), "changes": changes, "restart_required": restart})
}

func clip(text string, n int) string {
	if len(text) > n {
		return text[:n] + "…"
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

// applyEdits changes src through a YAML node tree, so comments and key order of
// everything not edited survive.
func applyEdits(src []byte, edits []Edit) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("配置根必须是映射")
	}
	for _, e := range edits {
		segments, err := pointer(e.Path)
		if err != nil {
			return nil, err
		}
		if err = edit(doc.Content[0], segments, e); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Path, err)
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func pointer(p string) ([]string, error) {
	if !strings.HasPrefix(p, "/") || p == "/" {
		return nil, fmt.Errorf("路径必须是 JSON Pointer: %q", p)
	}
	parts := strings.Split(p[1:], "/")
	for i, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("路径含空段: %q", p)
		}
		parts[i] = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
	}
	return parts, nil
}
func edit(m *yaml.Node, segments []string, e Edit) error {
	key, rest := segments[0], segments[1:]
	index := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			index = i
			break
		}
	}
	if len(rest) == 0 {
		if e.Delete {
			if index < 0 {
				return nil
			}
			m.Content = append(m.Content[:index], m.Content[index+2:]...)
			return nil
		}
		value := &yaml.Node{}
		if err := value.Encode(e.Value); err != nil {
			return err
		}
		if index >= 0 {
			old := m.Content[index+1]
			value.HeadComment, value.LineComment, value.FootComment = old.HeadComment, old.LineComment, old.FootComment
			m.Content[index+1] = value
			return nil
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
		return nil
	}
	if index < 0 {
		if e.Delete {
			return nil
		}
		child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
		return edit(child, rest, e)
	}
	child := m.Content[index+1]
	if child.Kind != yaml.MappingNode {
		return fmt.Errorf("%q 不是映射", key)
	}
	return edit(child, rest, e)
}

// diffConfigs lists every leaf that differs between two parsed configurations.
// Arrays are leaves.
func diffConfigs(before, after *config.Config) []Change {
	a, b := map[string]any{}, map[string]any{}
	flatten("", asJSON(before), a)
	flatten("", asJSON(after), b)
	paths := map[string]struct{}{}
	for p := range a {
		paths[p] = struct{}{}
	}
	for p := range b {
		paths[p] = struct{}{}
	}
	out := []Change{}
	for _, p := range sortedKeys(paths) {
		x, y := a[p], b[p]
		if jsonEqual(x, y) {
			continue
		}
		out = append(out, Change{Path: p, Before: x, After: y, Sensitive: hasPrefix(p, sensitivePrefixes)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
func asJSON(c *config.Config) any {
	raw, _ := json.Marshal(c)
	var v any
	_ = json.Unmarshal(raw, &v)
	return v
}
func flatten(prefix string, v any, out map[string]any) {
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		for k, child := range m {
			flatten(prefix+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(k), child, out)
		}
		return
	}
	out[prefix] = v
}
func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
