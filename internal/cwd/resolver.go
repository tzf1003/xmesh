// Package cwd is the single path expansion and policy boundary (CTR-CWD-01).
package cwd

import (
	"github.com/tzf1003/xmesh/internal/toolerr"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Workspace struct {
	Path string `json:"path" yaml:"path"`
	Desc string `json:"desc,omitempty" yaml:"desc,omitempty"`
}
type Policy struct {
	Allow []string `json:"allow" yaml:"allow"`
	Deny  []string `json:"deny,omitempty" yaml:"deny,omitempty"`
}
type Resolver struct {
	Home, Default string
	Aliases       map[string]Workspace
	Policy        Policy
}
type Result struct {
	Path  string `json:"resolved_path"`
	Alias string `json:"alias,omitempty"`
}
type Mode int

const (
	Existing Mode = iota
	Create
	Parents
)

func New(base string, aliases map[string]Workspace, policy Policy) (*Resolver, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if base == "" {
		base = home
	}
	r := &Resolver{Home: home, Default: home, Aliases: aliases, Policy: policy}
	b, _, err := r.expand(base, home, map[string]bool{})
	if err != nil {
		return nil, err
	}
	r.Default = b
	return r, nil
}
func (r *Resolver) expand(raw, base string, seen map[string]bool) (string, string, error) {
	if strings.ContainsRune(raw, 0) {
		return "", "", toolerr.Args("path")
	}
	alias := ""
	if strings.HasPrefix(raw, "@") {
		alias = raw[1:]
		w, ok := r.Aliases[alias]
		if !ok || seen[alias] {
			return "", "", toolerr.Path(raw, "not_found")
		}
		seen[alias] = true
		p, _, err := r.expand(w.Path, base, seen)
		return p, alias, err
	}
	if raw == "~" {
		raw = r.Home
	} else if strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`) {
		raw = filepath.Join(r.Home, raw[2:])
	}
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "$HOME", r.Home), "%USERPROFILE%", r.Home)
	raw = filepath.FromSlash(raw)
	if raw == "" {
		raw = base
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(base, raw)
	}
	p, err := filepath.Abs(filepath.Clean(raw))
	return p, alias, err
}
func canonical(path string, mode Mode) (string, error) {
	p, err := filepath.EvalSymlinks(path)
	if err == nil {
		return p, nil
	}
	if !os.IsNotExist(err) {
		return "", toolerr.Path(path, "denied")
	}
	// A dangling symlink must never become a writable new file.
	if st, e := os.Lstat(path); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", toolerr.Path(path, "not_found")
	}
	if mode == Existing {
		return "", toolerr.Path(path, "not_found")
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", toolerr.Path(path, "parent_missing")
	}
	pm := Existing
	if mode == Parents {
		pm = Parents
	}
	p, err = canonical(parent, pm)
	if err != nil {
		return "", toolerr.Path(path, "parent_missing")
	}
	if st, e := os.Stat(p); e == nil && !st.IsDir() {
		return "", toolerr.Path(path, "not_directory")
	}
	return filepath.Join(p, filepath.Base(path)), nil
}
func Within(path, root string) bool {
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		root = strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (r *Resolver) Resolve(raw, base string, mode Mode) (Result, error) {
	if base == "" {
		base = r.Default
	}
	p, alias, err := r.expand(raw, base, map[string]bool{})
	if err != nil {
		return Result{}, err
	}
	real, err := canonical(p, mode)
	if err != nil {
		return Result{}, err
	}
	match := func(rules []string) bool {
		for _, rule := range rules {
			q, _, e := r.expand(rule, r.Default, map[string]bool{})
			if e != nil {
				continue
			}
			q, e = canonical(q, Parents)
			if e == nil && Within(real, q) {
				return true
			}
		}
		return false
	}
	if match(r.Policy.Deny) || !match(r.Policy.Allow) {
		reason := "denied"
		if p != real {
			reason = "symlink_escape"
		}
		return Result{}, toolerr.Path(raw, reason)
	}
	return Result{Path: real, Alias: alias}, nil
}
func (r *Resolver) Directory(raw string) (Result, error) {
	p, err := r.Resolve(raw, "", Existing)
	if err != nil {
		return Result{}, err
	}
	st, err := os.Stat(p.Path)
	if err != nil {
		return Result{}, toolerr.Path(raw, "not_found")
	}
	if !st.IsDir() {
		return Result{}, toolerr.Path(raw, "not_directory")
	}
	return p, nil
}
