package fs

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

func fixture(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	r, e := cwd.New(root, nil, cwd.Policy{Allow: []string{root}, Deny: []string{filepath.Join(root, "secret")}})
	if e != nil {
		t.Fatal(e)
	}
	return &Service{Resolver: r}, root
}
func call(t *testing.T, s *Service, name string, a Object) Object {
	t.Helper()
	b, _ := json.Marshal(a)
	out, e := s.Call(context.Background(), name, b)
	if e != nil {
		t.Fatalf("%s: %v", name, e)
	}
	return out
}
func code(t *testing.T, s *Service, name string, a Object, want string) {
	t.Helper()
	b, _ := json.Marshal(a)
	_, e := s.Call(context.Background(), name, b)
	v, ok := e.(*toolerr.Error)
	if !ok || v.Code != want {
		t.Fatalf("%s got %v want %s", name, e, want)
	}
}

// conformance: CTR-FS-01 CON-TOOL-04 CON-TOOL-07
func Test_CTRFS01_ReadBytes(t *testing.T) {
	s, root := fixture(t)
	call(t, s, "fs.write", Object{"path": "a", "content": "one\r\ntwo\r\nthree"})
	o := call(t, s, "fs.read", Object{"path": "a", "offset": 2, "limit": 1})
	if o["content"] != "     2|two\r\n" || o["truncated"] != true {
		t.Fatal(o)
	}
	for _, b := range [][]byte{{0xef, 0xbb, 0xbf, 'x'}, {0xff, 0xfe, 0, 0}, {'a', 0, 'b'}, {0xff}} {
		if e := os.WriteFile(filepath.Join(root, "b"), b, 0600); e != nil {
			t.Fatal(e)
		}
		code(t, s, "fs.read", Object{"path": "b"}, "binary_rejected")
	}
	f, e := os.Create(filepath.Join(root, "huge"))
	if e != nil {
		t.Fatal(e)
	}
	_ = f.Truncate((10 << 20) + 1)
	_ = f.Close()
	code(t, s, "fs.read", Object{"path": "huge"}, "too_large")
}

// conformance: CTR-FS-02 CTR-FS-03 INV-06 CON-TOOL-09 RUL-10
func Test_CTRFS02_03_OptimisticAtomicEdit(t *testing.T) {
	s, root := fixture(t)
	a := call(t, s, "fs.write", Object{"path": "a", "content": "a\r\na\r\n"})
	hash := a["sha256"]
	code(t, s, "fs.write", Object{"path": "new", "content": "x", "expected_sha256": Hash(nil)}, "invalid_cwd")
	code(t, s, "fs.edit", Object{"path": "a", "old_string": "missing", "new_string": "x"}, "edit_not_found")
	code(t, s, "fs.edit", Object{"path": "a", "old_string": "a", "new_string": "x"}, "edit_ambiguous")
	o := call(t, s, "fs.edit", Object{"path": "a", "old_string": "a", "new_string": "b", "replace_all": true, "expected_sha256": hash})
	if o["replacements"] != 2 {
		t.Fatal(o)
	}
	code(t, s, "fs.write", Object{"path": "a", "content": "x", "expected_sha256": hash}, "conflict")
	b, _ := os.ReadFile(filepath.Join(root, "a"))
	if string(b) != "b\r\nb\r\n" {
		t.Fatalf("bytes changed %q", b)
	}
	code(t, s, "fs.write", Object{"path": "missing/a", "content": "x"}, "invalid_cwd")
	first := strings.Repeat("A", 1<<16)
	second := strings.Repeat("B", 1<<16)
	call(t, s, "fs.write", Object{"path": "a", "content": first})
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			b, e := readBounded(filepath.Join(root, "a"), 1<<20)
			if e != nil || (string(b) != first && string(b) != second) {
				t.Errorf("partial atomic read: %d %v", len(b), e)
				return
			}
		}
	}()
	for i := 0; i < 12; i++ {
		call(t, s, "fs.write", Object{"path": "a", "content": second})
		call(t, s, "fs.write", Object{"path": "a", "content": first})
	}
	close(done)
	wg.Wait()
}

// conformance: CTR-FS-06 CTR-FS-07 CON-TOOL-06
func Test_CTRFS06_07_MetadataAndDelete(t *testing.T) {
	s, root := fixture(t)
	if call(t, s, "fs.stat", Object{"path": "missing/deeper"})["exists"] != false {
		t.Fatal("missing")
	}
	code(t, s, "fs.stat", Object{"path": "secret/missing"}, "invalid_cwd")
	call(t, s, "fs.mkdir", Object{"path": "nested/deep", "parents": true})
	call(t, s, "fs.write", Object{"path": "nested/deep/a", "content": "a"})
	code(t, s, "fs.delete", Object{"path": "nested"}, "policy_denied")
	if e := os.Symlink(filepath.Join(root, "nested"), filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	if call(t, s, "fs.stat", Object{"path": "link"})["symlink"] != true {
		t.Fatal("symlink metadata lost")
	}
	call(t, s, "fs.delete", Object{"path": "link"})
	if _, e := os.Stat(filepath.Join(root, "nested/deep/a")); e != nil {
		t.Fatal("symlink target removed", e)
	}
	call(t, s, "fs.move", Object{"from": "nested/deep/a", "to": "b"})
	code(t, s, "fs.move", Object{"from": "b", "to": "b"}, "conflict")
	if e := os.Mkdir(filepath.Join(root, "secret"), 0700); e != nil {
		t.Fatal(e)
	}
	code(t, s, "fs.delete", Object{"path": root, "recursive": true}, "invalid_cwd")
	if _, e := os.Stat(filepath.Join(root, "b")); e != nil {
		t.Fatal("preflight changed file", e)
	}
}

// conformance: CTR-FS-05 CON-IMPL-05
func Test_CTRFS05_SearchEngineParity(t *testing.T) {
	s, root := fixture(t)
	if e := os.Mkdir(filepath.Join(root, "ignored"), 0700); e != nil {
		t.Fatal(e)
	}
	for p, c := range map[string]string{".gitignore": "ignored/\n", "a.go": "before\nhello Hello\nafter\nhello\n", "b.go": "hello\nworld\n", "ignored/c.go": "hello\n"} {
		if e := os.WriteFile(filepath.Join(root, p), []byte(c), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := exec.LookPath("rg"); e != nil {
		t.Skip("rg parity requires ripgrep")
	}
	for _, mode := range []string{"content", "files", "count"} {
		for _, multi := range []bool{false, true} {
			a := Args{Pattern: "hello", Type: "go", OutputMode: mode, HeadLimit: 200, Context: 1, IgnoreCase: true, Multiline: multi}
			if multi {
				a.Pattern = "hello\\nworld"
			}
			fast, e := s.grepEngine(context.Background(), root, a, false)
			if e != nil {
				t.Fatal(e)
			}
			pure, e := s.grepEngine(context.Background(), root, a, true)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(fast, pure) {
				t.Fatalf("%s multi=%v\nrg=%#v\ngo=%#v", mode, multi, fast, pure)
			}
		}
	}
	code(t, s, "fs.grep", Object{"pattern": "(?<=a)b", "output_mode": "content"}, "invalid_args")
	o := call(t, s, "fs.glob", Object{"pattern": "**/*.go", "limit": 1})
	if len(o["matches"].([]Object)) != 1 || o["truncated"] != true {
		t.Fatal(o)
	}
}

// conformance: CTR-FS-05 CON-TOOL-05
func Test_SearchLimitsAndNestedNegation(t *testing.T) {
	s, root := fixture(t)
	if e := os.Mkdir(filepath.Join(root, "child"), 0700); e != nil {
		t.Fatal(e)
	}
	for path, content := range map[string]string{"one.go": "needle\n", ".gitignore": "*.log\n", "child/.gitignore": "!keep.log\n", "child/keep.log": "needle\n", "child/drop.log": "needle\n"} {
		if e := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if o := call(t, s, "fs.glob", Object{"pattern": "*.go", "limit": 1}); o["truncated"] != false {
		t.Fatal("exact glob limit", o)
	}
	for _, mode := range []string{"content", "files", "count"} {
		for _, pure := range []bool{true, false} {
			a := Args{Pattern: "needle", Type: "go", OutputMode: mode, HeadLimit: 1}
			if o, e := s.grepEngine(context.Background(), root, a, pure); e != nil || o["truncated"] != false {
				t.Fatal("exact grep limit", mode, pure, o, e)
			}
			a.Type = ""
			if o, e := s.grepEngine(context.Background(), root, a, pure); e != nil || o["truncated"] != true {
				t.Fatal("overflow grep limit", mode, pure, o, e)
			}
		}
	}
	o := call(t, s, "fs.glob", Object{"pattern": "**/*.log", "limit": 100})
	got := o["matches"].([]Object)
	if len(got) != 1 || got[0]["path"] != filepath.Join(root, "child", "keep.log") {
		t.Fatal("child negation", o)
	}
	for _, name := range []string{".bashrc", ".zshenv", "profile", "setup.ksh", "setup.sh"} {
		if !filterFile(root, filepath.Join(root, name), Args{Type: "sh"}, builtinTypes) {
			t.Fatal("shell type omitted", name)
		}
	}
}

func Test_CTRFS05_TypesMatchRg(t *testing.T) {
	s, root := fixture(t)
	files := map[string]string{"main.cpp": "needle\n", "lib.rs": "needle\n", "Makefile": "needle\n", "api.proto": "needle\n", "notes.txt": "needle\n", "deploy.tf": "needle\n"}
	for p, c := range files {
		if e := os.WriteFile(filepath.Join(root, p), []byte(c), 0600); e != nil {
			t.Fatal(e)
		}
	}
	expect := map[string]string{"cpp": "main.cpp", "rust": "lib.rs", "make": "Makefile", "protobuf": "api.proto", "txt": "notes.txt", "tf": "deploy.tf"}
	_, rgErr := exec.LookPath("rg")
	for typ, want := range expect {
		a := Args{Pattern: "needle", Type: typ, OutputMode: "files", HeadLimit: 10}
		pure, e := s.grepEngine(context.Background(), root, a, true)
		if e != nil {
			t.Fatal(typ, e)
		}
		if got := pure["files"].([]string); len(got) != 1 || filepath.Base(got[0]) != want {
			t.Fatal("pure engine type", typ, got)
		}
		if rgErr == nil {
			fast, e := s.grepEngine(context.Background(), root, a, false)
			if e != nil || !reflect.DeepEqual(fast, pure) {
				t.Fatal("engines disagree on type", typ, fast, pure, e)
			}
		}
	}
	for _, pure := range []bool{true, false} {
		a := Args{Pattern: "needle", Type: "no-such-type", OutputMode: "files", HeadLimit: 10}
		if _, e := s.grepEngine(context.Background(), root, a, pure); e == nil || e.(*toolerr.Error).Code != "invalid_args" {
			t.Fatal("unknown type accepted", pure, e)
		}
	}
	if rgErr != nil {
		return
	}
	// The embedded table is exactly the local rg's, unless rg is a different release.
	rg, _ := exec.LookPath("rg")
	if version, _ := exec.Command(rg, "--version").Output(); !strings.HasPrefix(string(version), "ripgrep 14.1.") {
		t.Skip("embedded type table is pinned to ripgrep 14.1")
	}
	if live := fileTypes(rg); !reflect.DeepEqual(live, builtinTypes) {
		t.Fatal("embedded type table drifted from rg --type-list")
	}
}
