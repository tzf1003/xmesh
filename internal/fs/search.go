package fs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	ignore "github.com/sabhiram/go-gitignore"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type ignoreRule struct {
	base  string
	rules []ignoreLine
}
type ignoreLine struct {
	matcher *ignore.GitIgnore
	negate  bool
}

func compileIgnore(base string, data []byte) ignoreRule {
	rule := ignoreRule{base: base}
	for _, line := range strings.Split(string(data), "\n") {
		negate := strings.HasPrefix(line, "!")
		if negate {
			line = line[1:]
			if strings.HasPrefix(line, "!") {
				line = "\\" + line
			}
		}
		rule.rules = append(rule.rules, ignoreLine{ignore.CompileIgnoreLines(line), negate})
	}
	return rule
}

var stop = errors.New("search limit reached")

// walkFiles streams authorized regular entries without following directory links.
func (s *Service) walkFiles(ctx context.Context, root string, include bool, fn func(string, fs.FileInfo) error) error {
	rules := []ignoreRule{}
	// Parent ignore files still apply when a search starts inside a subtree.
	for p := root; ; p = filepath.Dir(p) {
		b, e := os.ReadFile(filepath.Join(p, ".gitignore"))
		if e == nil {
			rules = append([]ignoreRule{compileIgnore(p, b)}, rules...)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if path != root {
			if d.Name() == ".git" {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !include {
				ignored := false
				for _, r := range rules {
					if cwd.Within(path, r.base) {
						rel, _ := filepath.Rel(r.base, path)
						for _, line := range r.rules {
							if line.matcher.MatchesPath(filepath.ToSlash(rel)) {
								ignored = !line.negate
							}
						}
					}
				}
				if ignored {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
		}
		if _, e = s.Resolver.Resolve(path, "", cwd.Existing); e != nil {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root {
				b, e := os.ReadFile(filepath.Join(path, ".gitignore"))
				if e == nil {
					rules = append(rules, compileIgnore(path, b))
				}
			}
			return nil
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return nil
		}
		return fn(path, st)
	})
}
func (s *Service) glob(ctx context.Context, root string, a Args) (Object, error) {
	if a.Pattern == "" || a.Limit < 1 || a.Limit > 10000 {
		return nil, toolerr.Args("pattern/limit")
	}
	if _, e := doublestar.Match(a.Pattern, ""); e != nil {
		return nil, toolerr.Args("pattern")
	}
	type item struct {
		path string
		st   fs.FileInfo
	}
	found := []item{}
	truncated := false
	// Keep at most limit entries while preserving global mtime ordering.
	less := func(i, j int) bool {
		if found[i].st.ModTime().Equal(found[j].st.ModTime()) {
			return found[i].path < found[j].path
		}
		return found[i].st.ModTime().After(found[j].st.ModTime())
	}
	include := a.IncludeIgnored && strings.Contains(filepath.ToSlash(a.Pattern), "/") && !strings.HasPrefix(a.Pattern, "**/")
	e := s.walkFiles(ctx, root, include, func(p string, st fs.FileInfo) error {
		rel, _ := filepath.Rel(root, p)
		ok, _ := doublestar.Match(a.Pattern, filepath.ToSlash(rel))
		if !ok {
			return nil
		}
		found = append(found, item{p, st})
		if len(found) > a.Limit {
			truncated = true
			sort.Slice(found, less)
			found = found[:a.Limit]
		}
		return nil
	})
	if e != nil {
		return nil, toolerr.Internal("fs.glob", e)
	}
	sort.Slice(found, less)
	out := []Object{}
	for _, v := range found {
		out = append(out, Object{"path": v.path, "mtime": Stamp(v.st.ModTime())})
	}
	return Object{"resolved_path": root, "matches": out, "truncated": truncated}, nil
}

// A conservative supported type set is shared by both engines. Unsupported
// types return invalid_args as required by CTR-FS-05 rather than guessing.
var types = map[string][]string{
	"go": {"*.go"}, "rust": {"*.rs"}, "py": {"*.py", "*.pyi"}, "python": {"*.py", "*.pyi"}, "js": {"*.js", "*.jsx", "*.cjs", "*.mjs", "*.vue"}, "ts": {"*.ts", "*.tsx", "*.cts", "*.mts"}, "typescript": {"*.ts", "*.tsx", "*.cts", "*.mts"}, "json": {"*.json", "*.sarif", "composer.lock"}, "yaml": {"*.yaml", "*.yml"}, "toml": {"*.toml", "Cargo.lock"}, "md": {"*.md", "*.markdown", "*.mdown", "*.mdwn", "*.mdx", "*.mkd", "*.mkdn"}, "markdown": {"*.md", "*.markdown", "*.mdown", "*.mdwn", "*.mdx", "*.mkd", "*.mkdn"}, "txt": {"*.txt"}, "html": {"*.html", "*.htm", "*.ejs"}, "css": {"*.css", "*.scss"}, "java": {"*.java", "*.jsp", "*.jspx", "*.properties"}, "swift": {"*.swift"}, "c": {"*.[chH]", "*.[chH].in", "*.cats"}, "sh": {"*.bash", "*.bashrc", "*.csh", "*.cshrc", "*.env", "*.ksh", "*.kshrc", "*.sh", "*.tcsh", "*.zsh", ".bash_login", ".bash_logout", ".bash_profile", ".bashrc", ".cshrc", ".env", ".kshrc", ".login", ".logout", ".profile", ".tcshrc", ".zlogin", ".zlogout", ".zprofile", ".zshenv", ".zshrc", "bash_login", "bash_logout", "bash_profile", "bashrc", "profile", "zlogin", "zlogout", "zprofile", "zshenv", "zshrc"},
}

func filterFile(root, p string, a Args) bool {
	if a.Glob != "" {
		rel, _ := filepath.Rel(root, p)
		pattern := a.Glob
		if !strings.Contains(pattern, "/") {
			rel = filepath.Base(p)
		}
		ok, _ := doublestar.Match(pattern, filepath.ToSlash(rel))
		if !ok {
			return false
		}
	}
	if a.Type != "" {
		for _, pattern := range types[a.Type] {
			if ok, _ := filepath.Match(pattern, filepath.Base(p)); ok {
				return true
			}
		}
		return false
	}
	return true
}

type hit struct {
	line  int
	text  string
	count int
}

func (s *Service) grep(ctx context.Context, root string, a Args) (Object, error) {
	return s.grepEngine(ctx, root, a, false)
}
func (s *Service) grepEngine(ctx context.Context, root string, a Args, pure bool) (Object, error) {
	if a.OutputMode != "files" && a.OutputMode != "content" && a.OutputMode != "count" {
		return nil, toolerr.Args("output_mode")
	}
	if a.Context < 0 || a.Context > 5 || a.HeadLimit < 1 || a.HeadLimit > 2000 {
		return nil, toolerr.Args("context/head_limit")
	}
	pattern := a.Pattern
	if a.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	if a.Multiline {
		pattern = "(?m)" + pattern
	}
	re, e := regexp.Compile(pattern)
	if e != nil {
		return nil, toolerr.Args("pattern")
	}
	if a.Type != "" && types[a.Type] == nil {
		return nil, toolerr.Args("type")
	}
	if a.Glob != "" {
		if _, e := doublestar.Match(a.Glob, ""); e != nil {
			return nil, toolerr.Args("glob")
		}
	}
	rg, _ := exec.LookPath("rg")
	if pure {
		rg = ""
	}
	files := []string{}
	matches := []Object{}
	counts := []Object{}
	total := 0
	truncated := false
	e = s.walkFiles(ctx, root, false, func(p string, st fs.FileInfo) error {
		if !filterFile(root, p, a) {
			return nil
		}
		// Per-file bounds prevent an arbitrarily large input from exhausting memory.
		b, e := readBounded(p, 128<<20)
		if e != nil {
			return e
		}
		if strings.ContainsRune(string(b), 0) {
			return nil
		}
		var hits []hit
		if rg != "" {
			hits, e = rgHits(ctx, rg, p, a, pattern)
		} else {
			hits = goHits(b, re, a.Multiline)
		}
		if e != nil {
			return e
		}
		if len(hits) == 0 {
			return nil
		}
		if total >= a.HeadLimit {
			truncated = true
			return stop
		}
		switch a.OutputMode {
		case "files":
			files = append(files, p)
			total++
		case "count":
			n := 0
			for _, h := range hits {
				if total >= a.HeadLimit {
					truncated = true
					return stop
				}
				n += h.count
			}
			counts = append(counts, Object{"path": p, "count": n})
			total++
		case "content":
			lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			for _, h := range hits {
				m := Object{"path": p, "line": h.line, "text": h.text}
				if a.Context > 0 {
					before := max(0, h.line-1-a.Context)
					after := min(len(lines), h.line+a.Context)
					m["context_before"] = lines[before : h.line-1]
					m["context_after"] = lines[min(h.line, len(lines)):after]
				}
				matches = append(matches, m)
				total++
			}
		}
		return nil
	})
	if e != nil && !errors.Is(e, stop) {
		return nil, toolerr.Internal("fs.grep", e)
	}
	out := Object{"resolved_path": root, "truncated": truncated}
	switch a.OutputMode {
	case "files":
		out["files"] = files
	case "content":
		out["matches"] = matches
	case "count":
		out["counts"] = counts
	}
	return out, nil
}
func goHits(b []byte, re *regexp.Regexp, multiline bool) []hit {
	hits := []hit{}
	if !multiline {
		for i, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			n := len(re.FindAllStringIndex(line, -1))
			if n > 0 {
				hits = append(hits, hit{i + 1, line, n})
			}
		}
		return hits
	}
	for _, loc := range re.FindAllIndex(b, -1) {
		line := strings.Count(string(b[:loc[0]]), "\n") + 1
		start := loc[0]
		for start > 0 && b[start-1] != '\n' {
			start--
		}
		end := loc[1]
		for end < len(b) && b[end] != '\n' {
			end++
		}
		text := strings.TrimSuffix(string(b[start:end]), "\n")
		if len(hits) > 0 && hits[len(hits)-1].line == line {
			hits[len(hits)-1].count++
		} else {
			hits = append(hits, hit{line, text, 1})
		}
	}
	return hits
}
func rgHits(ctx context.Context, rg, path string, a Args, pattern string) ([]hit, error) {
	args := []string{"--json", "--no-config", "--no-ignore", "--hidden", "--color=never"}
	if a.Multiline {
		args = append(args, "--multiline")
	}
	args = append(args, "--regexp", pattern, "--", path)
	cmd := exec.CommandContext(ctx, rg, args...)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 65536), 129<<20)
	hits := []hit{}
	for scan.Scan() {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Line  int `json:"line_number"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
				Submatches []json.RawMessage `json:"submatches"`
			} `json:"data"`
		}
		if e = json.Unmarshal(scan.Bytes(), &event); e != nil {
			break
		}
		if event.Type == "match" {
			hits = append(hits, hit{event.Data.Line, strings.TrimSuffix(event.Data.Lines.Text, "\n"), len(event.Data.Submatches)})
		}
	}
	if e == nil {
		e = scan.Err()
	}
	if e != nil {
		_ = cmd.Process.Kill()
	}
	wait := cmd.Wait()
	if e != nil {
		return nil, e
	}
	if wait != nil {
		var exit *exec.ExitError
		if !errors.As(wait, &exit) || exit.ExitCode() != 1 {
			return nil, wait
		}
	}
	return hits, nil
}
