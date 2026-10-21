// Package fs implements CTR-FS-01..08. Paths always pass through cwd.Resolver.
package fs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type Service struct {
	Resolver       *cwd.Resolver
	Transfers      *Transfers
	Caller         string
	TransferConfig config.FileTransfer
	mu             sync.Mutex
}
type Args struct {
	Path           string `json:"path"`
	CWD            string `json:"cwd"`
	From           string `json:"from"`
	To             string `json:"to"`
	Content        string `json:"content"`
	Data           string `json:"data"`
	SHA256         string `json:"sha256"`
	Force          bool   `json:"force"`
	TransferID     string `json:"transfer_id"`
	ByteOffset     int64  `json:"byte_offset"`
	ChunkSize      int    `json:"chunk_size"`
	Final          bool   `json:"final"`
	Old            string `json:"old_string"`
	New            string `json:"new_string"`
	Expected       string `json:"expected_sha256"`
	Offset         int    `json:"offset"`
	Limit          int    `json:"limit"`
	Depth          int    `json:"depth"`
	ReplaceAll     bool   `json:"replace_all"`
	Recursive      bool   `json:"recursive"`
	Parents        bool   `json:"parents"`
	Overwrite      bool   `json:"overwrite"`
	ShowHidden     bool   `json:"show_hidden"`
	Pattern        string `json:"pattern"`
	Glob           string `json:"glob"`
	Type           string `json:"type"`
	OutputMode     string `json:"output_mode"`
	Context        int    `json:"context"`
	IgnoreCase     bool   `json:"ignore_case"`
	Multiline      bool   `json:"multiline"`
	HeadLimit      int    `json:"head_limit"`
	IncludeIgnored bool   `json:"include_ignored"`
}
type Object = map[string]any

func Hash(b []byte) string     { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func (s *Service) Call(ctx context.Context, name string, raw json.RawMessage) (Object, error) {
	a := Args{Offset: 1, Limit: 2000, Depth: 1, HeadLimit: 200, Final: true, ChunkSize: transferChunkLimit}
	if name == "fs.glob" {
		a.Limit = 1000
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, toolerr.Args("arguments")
	}
	if a.CWD == "" {
		a.CWD = s.Resolver.Default
	}
	base, err := s.Resolver.Directory(a.CWD)
	if err != nil {
		return nil, err
	}
	mode := cwd.Existing
	switch name {
	case "fs.write", "fs.upload", "fs.stat", "fs.mkdir":
		mode = cwd.Create
		if a.Parents && name == "fs.mkdir" {
			mode = cwd.Parents
		}
	case "fs.glob", "fs.grep":
		if a.Path == "" {
			a.Path = base.Path
		}
	case "fs.move":
		a.Path = a.From
	}
	if a.Path == "" {
		return nil, toolerr.Args("path")
	}
	p, err := s.Resolver.Resolve(a.Path, base.Path, mode)
	if err != nil && name == "fs.stat" {
		p, err = s.Resolver.Resolve(a.Path, base.Path, cwd.Parents)
	}
	if err != nil {
		return nil, err
	}
	path := p.Path
	switch name {
	case "fs.download":
		return s.transfer(ctx, path, a, false)
	case "fs.upload":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.transfer(ctx, path, a, true)
	case "fs.read":
		if a.Offset < 1 || a.Limit < 1 || a.Limit > 5000 {
			return nil, toolerr.Args("offset/limit")
		}
		b, err := readBounded(path, 10<<20)
		if err != nil {
			return nil, err
		}
		mime := http.DetectContentType(b)
		out := Object{"resolved_path": path, "truncated": false}
		if mime == "image/png" || mime == "image/jpeg" || mime == "image/gif" || mime == "image/webp" {
			out["encoding"] = "base64"
			out["mime"] = mime
			out["data"] = base64.StdEncoding.EncodeToString(b)
			return out, nil
		}
		if !utf8.Valid(b) || bytes.ContainsRune(b, 0) || bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}) || bytes.HasPrefix(b, []byte{0xff, 0xfe}) || bytes.HasPrefix(b, []byte{0xfe, 0xff}) {
			return nil, toolerr.New("binary_rejected", "文件包含二进制或 BOM", "path", path, "hint", "使用 exec 处理该文件")
		}
		lines := bytes.SplitAfter(b, []byte{'\n'})
		if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
			lines = lines[:len(lines)-1]
		}
		var text strings.Builder
		end := min(len(lines), a.Offset-1+a.Limit)
		for i := a.Offset - 1; i < end; i++ {
			fmt.Fprintf(&text, "%6d|%s", i+1, lines[i])
		}
		out["content"] = text.String()
		out["truncated"] = end < len(lines)
		return out, nil
	case "fs.write", "fs.edit":
		s.mu.Lock()
		defer s.mu.Unlock()
		old, readErr := readBounded(path, 128<<20)
		if readErr != nil && (name == "fs.edit" || a.Expected != "" || !isMissing(readErr)) {
			return nil, readErr
		}
		if a.Expected != "" && Hash(old) != a.Expected {
			return nil, conflict(path, old)
		}
		content := []byte(a.Content)
		replacements := 0
		if name == "fs.edit" {
			if a.Old == "" {
				return nil, toolerr.Args("old_string")
			}
			replacements = bytes.Count(old, []byte(a.Old))
			if replacements == 0 {
				return nil, toolerr.New("edit_not_found", "未找到待替换文本", "path", path)
			}
			if replacements > 1 && !a.ReplaceAll {
				lines := []int{}
				start := 0
				for {
					i := bytes.Index(old[start:], []byte(a.Old))
					if i < 0 {
						break
					}
					i += start
					lines = append(lines, bytes.Count(old[:i], []byte{'\n'})+1)
					start = i + len(a.Old)
				}
				return nil, toolerr.New("edit_ambiguous", "待替换文本有多个匹配", "path", path, "lines", lines)
			}
			content = bytes.ReplaceAll(old, []byte(a.Old), []byte(a.New))
		}
		if err := atomicWrite(path, content, a.Expected); err != nil {
			return nil, err
		}
		out := Object{"resolved_path": path, "sha256": Hash(content), "bytes": len(content)}
		if name == "fs.edit" {
			out["replacements"] = replacements
		}
		return out, nil
	case "fs.sha256":
		b, e := readBounded(path, 128<<20)
		if e != nil {
			return nil, e
		}
		return Object{"resolved_path": path, "sha256": Hash(b), "bytes": len(b)}, nil
	case "fs.stat":
		// Lstat the directory entry to preserve symlink metadata after target validation.
		entry, e := s.entry(a.Path, base.Path, cwd.Parents)
		if e != nil {
			return nil, e
		}
		st, e := os.Lstat(entry)
		if os.IsNotExist(e) {
			return Object{"resolved_path": path, "exists": false}, nil
		}
		if e != nil {
			return nil, toolerr.Internal(name, e)
		}
		return Object{"resolved_path": path, "exists": true, "type": kind(st), "size": st.Size(), "mode": fmt.Sprintf("%04o", st.Mode().Perm()), "symlink": st.Mode()&os.ModeSymlink != 0, "mtime": Stamp(st.ModTime())}, nil
	case "fs.list":
		if a.Depth < 1 || a.Depth > 5 {
			return nil, toolerr.Args("depth")
		}
		st, e := os.Stat(path)
		if e != nil || !st.IsDir() {
			return nil, toolerr.Path(path, "not_directory")
		}
		entries := []Object{}
		e = filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if p == path {
				return nil
			}
			rel, _ := filepath.Rel(path, p)
			level := strings.Count(filepath.ToSlash(rel), "/") + 1
			if !a.ShowHidden && hidden(p, d) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if _, e := s.Resolver.Resolve(p, base.Path, cwd.Existing); e != nil {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			st, e := d.Info()
			if e != nil {
				return e
			}
			entries = append(entries, Object{"name": filepath.ToSlash(rel), "type": kind(st), "size": st.Size(), "mtime": Stamp(st.ModTime())})
			if d.IsDir() && level >= a.Depth {
				return filepath.SkipDir
			}
			return ctx.Err()
		})
		if e != nil {
			return nil, toolerr.Internal(name, e)
		}
		return Object{"resolved_path": path, "entries": entries}, nil
	case "fs.mkdir":
		st, e := os.Stat(path)
		if e == nil {
			if !st.IsDir() {
				return nil, existingTargetConflict(path)
			}
			return Object{"resolved_path": path, "created": false}, nil
		}
		if a.Parents {
			e = os.MkdirAll(path, 0o755)
		} else {
			e = os.Mkdir(path, 0o755)
		}
		if e != nil {
			return nil, toolerr.Internal(name, e)
		}
		return Object{"resolved_path": path, "created": true}, nil
	case "fs.move":
		if a.To == "" {
			return nil, toolerr.Args("to")
		}
		to, e := s.Resolver.Resolve(a.To, base.Path, cwd.Create)
		if e != nil {
			return nil, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, e = os.Lstat(to.Path); e == nil && !a.Overwrite {
			return nil, existingTargetConflict(to.Path)
		}
		source, e := s.entry(a.From, base.Path, cwd.Existing)
		if e != nil {
			return nil, e
		}
		if e = os.Rename(source, to.Path); e != nil {
			return nil, toolerr.Internal(name, e)
		}
		return Object{"resolved_path": to.Path}, nil
	case "fs.delete":
		entry, e := s.entry(a.Path, base.Path, cwd.Existing)
		if e != nil {
			return nil, e
		}
		st, e := os.Lstat(entry)
		if e != nil {
			return nil, toolerr.Internal(name, e)
		}
		if st.IsDir() && !a.Recursive {
			return nil, toolerr.New("policy_denied", "删除目录必须显式允许递归", "rule", "fs.delete.recursive_default_deny", "reason", "recursive 必须为 true")
		}
		// Preflight every descendant before mutation: denied subtrees stay protected.
		targets := []string{entry}
		if st.IsDir() {
			targets = nil
			e = filepath.WalkDir(entry, func(p string, d fs.DirEntry, e error) error {
				if e != nil {
					return e
				}
				if _, e = s.Resolver.Resolve(p, base.Path, cwd.Existing); e != nil {
					return e
				}
				targets = append(targets, p)
				return ctx.Err()
			})
			if e != nil {
				return nil, e
			}
		}
		removed := 0
		for i := len(targets) - 1; i >= 0; i-- {
			if e = os.Remove(targets[i]); e != nil {
				return nil, toolerr.New("internal", "删除过程中发生错误", "op", name, "removed", removed)
			}
			removed++
		}
		return Object{"resolved_path": path, "removed": removed}, nil
	case "fs.glob":
		return s.glob(ctx, path, a)
	case "fs.grep":
		return s.grep(ctx, path, a)
	default:
		return nil, toolerr.New("not_found", "工具不存在", "name", name)
	}
}
func (s *Service) entry(raw, base string, mode cwd.Mode) (string, error) {
	// Resolve the parent independently, retaining the final directory entry.
	if strings.HasPrefix(raw, "@") {
		p, e := s.Resolver.Resolve(raw, base, mode)
		return p.Path, e
	}
	if !filepath.IsAbs(raw) && !strings.HasPrefix(raw, "~") && !strings.Contains(raw, "$HOME") && !strings.Contains(raw, "%USERPROFILE%") {
		raw = filepath.Join(base, raw)
	}
	p, e := s.Resolver.Resolve(filepath.Dir(raw), base, cwd.Parents)
	if e != nil {
		return "", e
	}
	return filepath.Join(p.Path, filepath.Base(raw)), nil
}
func kind(st fs.FileInfo) string {
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		return "symlink"
	case st.IsDir():
		return "dir"
	case st.Mode().IsRegular():
		return "file"
	default:
		return "other"
	}
}
func readBounded(path string, limit int64) ([]byte, error) {
	f, e := openRead(path)
	if e != nil {
		if os.IsNotExist(e) {
			return nil, toolerr.Path(path, "not_found")
		}
		return nil, toolerr.Internal("fs.open", e)
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() {
		return nil, toolerr.Path(path, "not_directory")
	}
	if st.Size() > limit {
		return nil, toolerr.New("too_large", "文件过大，请使用 exec 处理", "path", path, "size", st.Size(), "limit", limit)
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e == nil && int64(len(b)) > limit {
		return nil, toolerr.New("too_large", "文件过大，请使用 exec 处理", "path", path, "size", len(b), "limit", limit)
	}
	return b, e
}
func isMissing(e error) bool {
	v, ok := e.(*toolerr.Error)
	return ok && v.Code == "invalid_cwd" && v.Fields["reason"] == "not_found"
}
func conflict(path string, b []byte) *toolerr.Error {
	e := existingTargetConflict(path)
	e.Fields["current_sha256"] = Hash(b)
	return e
}

// Existence checks report metadata only; a content hash requires reading bytes.
func existingTargetConflict(path string) *toolerr.Error {
	mtime := ""
	if st, e := os.Stat(path); e == nil {
		mtime = Stamp(st.ModTime())
	}
	return toolerr.New("conflict", "目标已存在或内容发生变化", "path", path, "mtime", mtime)
}
func atomicWrite(path string, b []byte, expected string) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".xmesh-write-*")
	if e != nil {
		return toolerr.Internal("fs.write", e)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	mode := fs.FileMode(0o600)
	if st, e := os.Stat(path); e == nil {
		mode = st.Mode().Perm()
	}
	if e = f.Chmod(mode); e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if expected != "" {
		current, e := readBounded(path, 128<<20)
		if e != nil {
			return e
		}
		if Hash(current) != expected {
			return conflict(path, current)
		}
	}
	if e = atomicReplace(tmp, path); e != nil {
		return toolerr.Internal("fs.rename", e)
	}
	return nil
}
