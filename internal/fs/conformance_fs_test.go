package fs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

func fail(t *testing.T, s *Service, name string, a Object, want string) *toolerr.Error {
	t.Helper()
	b, _ := json.Marshal(a)
	_, e := s.Call(context.Background(), name, b)
	v, ok := e.(*toolerr.Error)
	if !ok || v.Code != want {
		t.Fatalf("%s %v: got %v, want %s", name, a, e, want)
	}
	return v
}

func writeBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fs.write and fs.edit store exactly the bytes they are given: CRLF, LF and
// mixed line endings stay, a leading UTF-8 BOM is neither added nor removed.
// fs.read refuses BOM and UTF-16 files instead of silently re-encoding them,
// and returns CRLF lines unchanged.
// conformance: CON-TOOL-04
func Test_CONTOOL04_BytesAreNeverRewritten(t *testing.T) {
	s, root := fixture(t)
	cases := map[string]string{
		"crlf":    "one\r\ntwo\r\nthree\r\n",
		"lf":      "one\ntwo\nthree\n",
		"mixed":   "one\r\ntwo\nthree\rfour\r\n",
		"bom":     "\ufeffone\r\ntwo\r\n",
		"no-eol":  "one\ntwo",
		"bom-lf":  "\ufeffone\ntwo\n",
		"cr-only": "one\rtwo\r",
	}
	for name, content := range cases {
		out := call(t, s, "fs.write", Object{"path": name, "content": content})
		if got := readBytes(t, filepath.Join(root, name)); string(got) != content {
			t.Errorf("%s: fs.write stored %q, want %q", name, got, content)
		}
		if out["bytes"] != len(content) || out["sha256"] != Hash([]byte(content)) {
			t.Errorf("%s: write result %v", name, out)
		}
		edited := call(t, s, "fs.edit", Object{"path": name, "old_string": "two", "new_string": "2"})
		want := strings.Replace(content, "two", "2", 1)
		if got := readBytes(t, filepath.Join(root, name)); string(got) != want {
			t.Errorf("%s: fs.edit stored %q, want %q", name, got, want)
		}
		if edited["sha256"] != Hash([]byte(want)) {
			t.Errorf("%s: edit result %v", name, edited)
		}
	}
	// Editing a line ending itself works at byte level and touches nothing else.
	call(t, s, "fs.edit", Object{"path": "crlf", "old_string": "one\r\n", "new_string": "ONE\n"})
	if got := readBytes(t, filepath.Join(root, "crlf")); string(got) != "ONE\n2\r\nthree\r\n" {
		t.Errorf("edited line ending: %q", got)
	}
	// Reading: CRLF is returned as is (only the number prefix is added)...
	crlf := call(t, s, "fs.read", Object{"path": "mixed"})
	if crlf["content"] != "     1|one\r\n     2|2\n     3|three\rfour\r\n" {
		t.Errorf("read of mixed endings: %q", crlf["content"])
	}
	// ...and BOM / UTF-16 files are rejected, never converted.
	for name, content := range map[string][]byte{
		"bom":     []byte("\xef\xbb\xbfhello\n"),
		"utf16le": {0xff, 0xfe, 'h', 0, 'i', 0},
		"utf16be": {0xfe, 0xff, 0, 'h', 0, 'i'},
		"nul":     {'a', 0, 0, 0, 'b'},
	} {
		writeBytes(t, filepath.Join(root, "raw-"+name), content)
		v := fail(t, s, "fs.read", Object{"path": "raw-" + name}, "binary_rejected")
		if v.Fields["hint"] == nil || !strings.Contains(v.Fields["hint"].(string), "exec") {
			t.Errorf("%s: hint = %v", name, v.Fields["hint"])
		}
		if !bytes.Equal(readBytes(t, filepath.Join(root, "raw-"+name)), content) {
			t.Errorf("%s: reading changed the file", name)
		}
	}
}

// A directory is deleted only with recursive: true. Without it the refusal is
// policy_denied under the rule fs.delete.recursive_default_deny and nothing
// below the directory is removed, empty directories included. With it the path
// still goes through the cwd policy: outside allow nothing is removed.
// conformance: CON-TOOL-06
func Test_CONTOOL06_RecursiveDeleteDefaultDeny(t *testing.T) {
	s, root := fixture(t)
	for _, f := range []string{"tree/a.txt", "tree/deep/b.txt", "tree/deep/er/c.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o700); err != nil {
			t.Fatal(err)
		}
		writeBytes(t, filepath.Join(root, f), []byte(f))
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	list := func() []string {
		var out []string
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
			return nil
		})
		sort.Strings(out)
		return out
	}
	before := list()
	for _, target := range []string{"tree", "tree/deep", "empty"} {
		for _, args := range []Object{{"path": target}, {"path": target, "recursive": false}} {
			v := fail(t, s, "fs.delete", args, "policy_denied")
			if v.Fields["rule"] != "fs.delete.recursive_default_deny" {
				t.Errorf("rule = %v", v.Fields["rule"])
			}
			if !reflect.DeepEqual(list(), before) {
				t.Fatalf("a refused delete of %s removed something:\n%v\n%v", target, before, list())
			}
		}
	}
	// Outside the allow root, recursive:true does not help.
	outside := t.TempDir()
	writeBytes(t, filepath.Join(outside, "keep.txt"), []byte("keep"))
	fail(t, s, "fs.delete", Object{"path": outside, "recursive": true}, "invalid_cwd")
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatal("recursive delete outside allow removed a file", err)
	}
	// A denied subtree vetoes the whole delete.
	if err := os.Mkdir(filepath.Join(root, "secret"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeBytes(t, filepath.Join(root, "secret", "s.txt"), []byte("s"))
	before = list()
	fail(t, s, "fs.delete", Object{"path": ".", "recursive": true}, "invalid_cwd")
	if !reflect.DeepEqual(list(), before) {
		t.Fatal("a vetoed recursive delete removed files")
	}
	// recursive:true on an allowed tree removes it and reports the count.
	out := call(t, s, "fs.delete", Object{"path": "tree", "recursive": true})
	if _, err := os.Stat(filepath.Join(root, "tree")); !os.IsNotExist(err) {
		t.Fatal("recursive delete left the directory behind")
	}
	if n, ok := out["removed"].(int); !ok || n < 6 || out["resolved_path"] != filepath.Join(root, "tree") {
		t.Errorf("delete result %v", out)
	}
	// A file needs no flag.
	writeBytes(t, filepath.Join(root, "single.txt"), []byte("x"))
	call(t, s, "fs.delete", Object{"path": "single.txt"})
	if _, err := os.Stat(filepath.Join(root, "single.txt")); !os.IsNotExist(err) {
		t.Fatal("a plain file delete did not remove the file")
	}
}

// fs.read refuses a file larger than 10 MiB with too_large before reading it
// (a sparse file full of NULs would otherwise be binary_rejected); exactly
// 10 MiB is accepted; the limit applies to images as well.
// conformance: CON-TOOL-07
func Test_CONTOOL07_TenMiBLimit(t *testing.T) {
	s, root := fixture(t)
	const limit = 10 << 20
	sparse := func(name string, size int64) {
		f, err := os.Create(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Truncate(size); err != nil {
			t.Skip("sparse files are not available:", err)
		}
		_ = f.Close()
	}
	sparse("over.bin", limit+1)
	v := fail(t, s, "fs.read", Object{"path": "over.bin"}, "too_large")
	if v.Fields["limit"] != int64(limit) || v.Fields["size"] != int64(limit+1) || v.Fields["path"] != filepath.Join(root, "over.bin") {
		t.Errorf("too_large fields: %v", v.Fields)
	}
	if !strings.Contains(v.Message, "exec") {
		t.Errorf("the message must point to exec: %q", v.Message)
	}
	sparse("zeros.bin", limit) // exactly at the limit: passes the size check, then fails as binary
	fail(t, s, "fs.read", Object{"path": "zeros.bin"}, "binary_rejected")

	text := bytes.Repeat([]byte("a"), limit)
	writeBytes(t, filepath.Join(root, "exact.txt"), text)
	out := call(t, s, "fs.read", Object{"path": "exact.txt"})
	if out["truncated"] != false || !strings.HasPrefix(out["content"].(string), "     1|aaa") {
		t.Errorf("a file of exactly 10 MiB must be readable")
	}

	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	writeBytes(t, filepath.Join(root, "small.png"), png)
	img := call(t, s, "fs.read", Object{"path": "small.png"})
	if img["mime"] != "image/png" || img["encoding"] != "base64" || img["data"] != base64.StdEncoding.EncodeToString(png) {
		t.Errorf("image result: %v", img)
	}
	big := append(append([]byte{}, png...), make([]byte, limit)...)
	writeBytes(t, filepath.Join(root, "big.png"), big)
	v = fail(t, s, "fs.read", Object{"path": "big.png"}, "too_large")
	if v.Fields["limit"] != int64(limit) {
		t.Errorf("image limit: %v", v.Fields)
	}
}

// fs.write and fs.edit never write into the target in place: the new content
// is written elsewhere in the directory and swapped in, so a reader that
// already has the file open keeps seeing the complete old content, while a new
// reader sees the complete new content. No temporary file is left behind, on
// success or on failure. A missing parent directory is invalid_cwd with reason
// parent_missing and is not created.
// conformance: CON-TOOL-08
func Test_CONTOOL08_TempFileInSameDirectoryThenRename(t *testing.T) {
	s, root := fixture(t)
	entries := func(dir string) []string {
		list, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, e := range list {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		return names
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := strings.Repeat("old content\n", 1000)
	call(t, s, "fs.write", Object{"path": "dir/f.txt", "content": old})
	if runtime.GOOS != "windows" {
		reader, err := os.Open(filepath.Join(root, "dir", "f.txt"))
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		before, _ := os.Stat(filepath.Join(root, "dir", "f.txt"))
		call(t, s, "fs.write", Object{"path": "dir/f.txt", "content": "new content\n"})
		after, _ := os.Stat(filepath.Join(root, "dir", "f.txt"))
		if os.SameFile(before, after) {
			t.Error("the file was rewritten in place, not replaced by rename")
		}
		held := new(bytes.Buffer)
		if _, err = held.ReadFrom(reader); err != nil || held.String() != old {
			t.Errorf("an open reader saw %d bytes instead of the complete old content (%v)", held.Len(), err)
		}
		call(t, s, "fs.edit", Object{"path": "dir/f.txt", "old_string": "new", "new_string": "newer"})
	}
	if got := readBytes(t, filepath.Join(root, "dir", "f.txt")); runtime.GOOS != "windows" && string(got) != "newer content\n" {
		t.Errorf("content = %q", got)
	}
	if got := entries(filepath.Join(root, "dir")); !reflect.DeepEqual(got, []string{"f.txt"}) {
		t.Errorf("temporary files left after successful writes: %v", got)
	}
	// Failures: stale hash, no match, missing parent.
	fail(t, s, "fs.write", Object{"path": "dir/f.txt", "content": "x", "expected_sha256": strings.Repeat("0", 64)}, "conflict")
	fail(t, s, "fs.edit", Object{"path": "dir/f.txt", "old_string": "absent", "new_string": "x"}, "edit_not_found")
	v := fail(t, s, "fs.write", Object{"path": "no-parent/inner/f.txt", "content": "x"}, "invalid_cwd")
	if v.Fields["reason"] != "parent_missing" {
		t.Errorf("reason = %v", v.Fields["reason"])
	}
	if _, err := os.Stat(filepath.Join(root, "no-parent")); !os.IsNotExist(err) {
		t.Error("fs.write created the missing parent directory")
	}
	v = fail(t, s, "fs.write", Object{"path": "dir/missing-parent/f.txt", "content": "x"}, "invalid_cwd")
	if v.Fields["reason"] != "parent_missing" {
		t.Errorf("reason = %v", v.Fields["reason"])
	}
	if got := entries(root); !reflect.DeepEqual(got, []string{"dir"}) {
		t.Errorf("root = %v", got)
	}
	if got := entries(filepath.Join(root, "dir")); !reflect.DeepEqual(got, []string{"f.txt"}) {
		t.Errorf("temporary files left after failed writes: %v", got)
	}
	// fs.mkdir is the tool that creates directories, and only with parents: true for nested ones.
	v = fail(t, s, "fs.mkdir", Object{"path": "a/b/c"}, "invalid_cwd")
	if v.Fields["reason"] != "parent_missing" {
		t.Errorf("mkdir reason = %v", v.Fields["reason"])
	}
	call(t, s, "fs.mkdir", Object{"path": "a/b/c", "parents": true})
}

// Without replace_all, old_string must match exactly once. No match is
// edit_not_found, several matches are edit_ambiguous with the 1-based line of
// every match, and neither touches the file (content, size and mtime stay).
// conformance: CON-TOOL-09 RUL-10
func Test_CONTOOL09_EditNeedsExactlyOneMatch(t *testing.T) {
	s, root := fixture(t)
	original := "alpha\nbeta alpha\ngamma\nalpha\n"
	call(t, s, "fs.write", Object{"path": "f.txt", "content": original})
	path := filepath.Join(root, "f.txt")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	unchanged := func(label string) {
		t.Helper()
		st, err := os.Stat(path)
		if err != nil || string(readBytes(t, path)) != original || !st.ModTime().Equal(old) {
			t.Errorf("%s: the file was modified (mtime %v)", label, st.ModTime())
		}
	}
	v := fail(t, s, "fs.edit", Object{"path": "f.txt", "old_string": "delta", "new_string": "x"}, "edit_not_found")
	if v.Fields["path"] != path {
		t.Errorf("edit_not_found path = %v", v.Fields["path"])
	}
	unchanged("zero matches")
	v = fail(t, s, "fs.edit", Object{"path": "f.txt", "old_string": "alpha", "new_string": "x"}, "edit_ambiguous")
	if lines, ok := v.Fields["lines"].([]int); !ok || !reflect.DeepEqual(lines, []int{1, 2, 4}) {
		t.Errorf("edit_ambiguous lines = %#v, want [1 2 4]", v.Fields["lines"])
	}
	unchanged("several matches")
	// replace_all with no match is still edit_not_found.
	fail(t, s, "fs.edit", Object{"path": "f.txt", "old_string": "delta", "new_string": "x", "replace_all": true}, "edit_not_found")
	unchanged("replace_all without a match")
	// edit_not_found and conflict are different conditions.
	fail(t, s, "fs.edit", Object{"path": "f.txt", "old_string": "gamma", "new_string": "x", "expected_sha256": strings.Repeat("0", 64)}, "conflict")
	unchanged("stale hash")
	// An empty old_string is not "match everywhere".
	fail(t, s, "fs.edit", Object{"path": "f.txt", "old_string": "", "new_string": "x"}, "invalid_args")
	unchanged("empty old_string")
	// Exactly one match is replaced; matching is literal bytes, not regex.
	fail(t, s, "fs.edit", Object{"path": "f.txt", "old_string": "g.mma", "new_string": "x"}, "edit_not_found")
	out := call(t, s, "fs.edit", Object{"path": "f.txt", "old_string": "gamma", "new_string": ""})
	if out["replacements"] != 1 || string(readBytes(t, path)) != "alpha\nbeta alpha\n\nalpha\n" {
		t.Errorf("single replacement: %v %q", out, readBytes(t, path))
	}
	out = call(t, s, "fs.edit", Object{"path": "f.txt", "old_string": "alpha", "new_string": "A", "replace_all": true})
	if out["replacements"] != 3 || string(readBytes(t, path)) != "A\nbeta A\n\nA\n" {
		t.Errorf("replace_all: %v %q", out, readBytes(t, path))
	}
}

// fs.edit succeeds with resolved_path, the new sha256 and the replacement count.
// conformance: CTR-FS-03
func Test_CTRFS03_SuccessFields(t *testing.T) {
	s, root := fixture(t)
	call(t, s, "fs.write", Object{"path": "f.txt", "content": "one two one\n"})
	out := call(t, s, "fs.edit", Object{"path": "f.txt", "old_string": "two", "new_string": "2"})
	want := "one 2 one\n"
	if out["resolved_path"] != filepath.Join(root, "f.txt") || out["sha256"] != Hash([]byte(want)) || out["replacements"] != 1 {
		t.Errorf("edit result: %v", out)
	}
	hashed := call(t, s, "fs.sha256", Object{"path": "f.txt"})
	if hashed["sha256"] != out["sha256"] || hashed["bytes"] != len(want) {
		t.Errorf("fs.sha256: %v", hashed)
	}
	fail(t, s, "fs.edit", Object{"path": "absent.txt", "old_string": "a", "new_string": "b"}, "invalid_cwd")
}

// fs.glob: ** recursion; newest first with ties broken by path; matches carry
// path and mtime; .gitignore is honoured unless include_ignored; the limit
// defaults to 1000, is capped at 10000, and sets truncated when it cuts results.
// conformance: CTR-FS-04 CON-TOOL-05
func Test_CTRFS04_GlobOrderLimitIgnore(t *testing.T) {
	s, root := fixture(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	files := []struct {
		path string
		age  int // seconds after base
	}{{"a.go", 10}, {"b.go", 30}, {"c.go", 30}, {"sub/d.go", 20}, {"sub/deep/e.go", 40}, {"note.txt", 50}}
	for _, f := range files {
		full := filepath.Join(root, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		writeBytes(t, full, []byte("x"))
		mtime := base.Add(time.Duration(f.age) * time.Second)
		if err := os.Chtimes(full, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	paths := func(out Object) []string {
		got := []string{}
		for _, m := range out["matches"].([]Object) {
			rel, _ := filepath.Rel(root, m["path"].(string))
			got = append(got, filepath.ToSlash(rel))
			if _, err := time.Parse(time.RFC3339Nano, m["mtime"].(string)); err != nil {
				t.Errorf("mtime %v: %v", m["mtime"], err)
			}
		}
		return got
	}
	out := call(t, s, "fs.glob", Object{"pattern": "**/*.go"})
	if want := []string{"sub/deep/e.go", "b.go", "c.go", "sub/d.go", "a.go"}; !reflect.DeepEqual(paths(out), want) || out["truncated"] != false || out["resolved_path"] != root {
		t.Errorf("order: got %v, want %v (%v)", paths(out), want, out)
	}
	out = call(t, s, "fs.glob", Object{"pattern": "*.go"})
	if want := []string{"b.go", "c.go", "a.go"}; !reflect.DeepEqual(paths(out), want) {
		t.Errorf("single level: %v", paths(out))
	}
	out = call(t, s, "fs.glob", Object{"pattern": "**/*.go", "path": "sub"})
	if want := []string{"sub/deep/e.go", "sub/d.go"}; !reflect.DeepEqual(paths(out), want) || out["resolved_path"] != filepath.Join(root, "sub") {
		t.Errorf("path root: %v", paths(out))
	}
	out = call(t, s, "fs.glob", Object{"pattern": "**/*.go", "limit": 2})
	if want := []string{"sub/deep/e.go", "b.go"}; !reflect.DeepEqual(paths(out), want) || out["truncated"] != true {
		t.Errorf("limit: %v truncated=%v", paths(out), out["truncated"])
	}
	fail(t, s, "fs.glob", Object{"pattern": "**/*.go", "limit": 10001}, "invalid_args")
	fail(t, s, "fs.glob", Object{"pattern": "**/*.go", "limit": 0}, "invalid_args")
	fail(t, s, "fs.glob", Object{"pattern": ""}, "invalid_args")
	if out = call(t, s, "fs.glob", Object{"pattern": "**/*.go", "limit": 10000}); out["truncated"] != false {
		t.Error("limit 10000 must be accepted")
	}
	// The default limit is 1000.
	if err := os.Mkdir(filepath.Join(root, "many"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		writeBytes(t, filepath.Join(root, "many", strings.Repeat("0", 4)+string(rune('a'+i%26))+strings.Repeat("x", i/26)+".tmp"), nil)
	}
	out = call(t, s, "fs.glob", Object{"pattern": "many/*.tmp"})
	if len(out["matches"].([]Object)) != 1000 || out["truncated"] != true {
		t.Errorf("default limit: %d matches, truncated=%v", len(out["matches"].([]Object)), out["truncated"])
	}
	// .gitignore.
	writeBytes(t, filepath.Join(root, ".gitignore"), []byte("ignored/\n*.log\n"))
	if err := os.Mkdir(filepath.Join(root, "ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeBytes(t, filepath.Join(root, "ignored", "g.go"), []byte("x"))
	writeBytes(t, filepath.Join(root, "build.log"), []byte("x"))
	if got := paths(call(t, s, "fs.glob", Object{"pattern": "**/g.go"})); len(got) != 0 {
		t.Errorf("an ignored directory was searched: %v", got)
	}
	if got := paths(call(t, s, "fs.glob", Object{"pattern": "*.log"})); len(got) != 0 {
		t.Errorf("an ignored file matched: %v", got)
	}
	if got := paths(call(t, s, "fs.glob", Object{"pattern": "ignored/*.go", "include_ignored": true})); !reflect.DeepEqual(got, []string{"ignored/g.go"}) {
		t.Errorf("include_ignored with an explicit pattern: %v", got)
	}
}

// fs.list, fs.stat and fs.sha256 report the contract's fields.
// conformance: CTR-FS-06
func Test_CTRFS06_ListStatSha256Fields(t *testing.T) {
	s, root := fixture(t)
	for _, d := range []string{"d/sub/deeper", ".hidden-dir"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeBytes(t, filepath.Join(root, "d", "f.txt"), []byte("hello"))
	writeBytes(t, filepath.Join(root, "d", ".dot"), []byte("."))
	writeBytes(t, filepath.Join(root, "d", "sub", "g.txt"), []byte("g"))
	writeBytes(t, filepath.Join(root, "d", "sub", "deeper", "h.txt"), []byte("h"))
	writeBytes(t, filepath.Join(root, ".hidden-dir", "x"), []byte("x"))
	if err := os.Symlink(filepath.Join(root, "d", "sub"), filepath.Join(root, "d", "link")); err != nil {
		t.Log("no symlinks:", err)
	}
	names := func(out Object) map[string]Object {
		got := map[string]Object{}
		for _, e := range out["entries"].([]Object) {
			got[e["name"].(string)] = e
		}
		return got
	}
	out := call(t, s, "fs.list", Object{"path": "d"})
	entries := names(out)
	if out["resolved_path"] != filepath.Join(root, "d") {
		t.Errorf("resolved_path = %v", out["resolved_path"])
	}
	if _, hidden := entries[".dot"]; hidden {
		t.Error("hidden entries must be omitted by default")
	}
	if e := entries["f.txt"]; e == nil || e["type"] != "file" || e["size"] != int64(5) || e["mtime"] == nil {
		t.Errorf("file entry: %v", e)
	}
	if e := entries["sub"]; e == nil || e["type"] != "dir" {
		t.Errorf("dir entry: %v", e)
	}
	if _, deeper := entries["sub/g.txt"]; deeper {
		t.Error("depth 1 listed the content of a subdirectory")
	}
	if e, ok := entries["link"]; ok && e["type"] != "symlink" {
		t.Errorf("symlink entry: %v", e)
	}
	entries = names(call(t, s, "fs.list", Object{"path": "d", "depth": 3, "show_hidden": true}))
	for _, want := range []string{".dot", "sub/g.txt", "sub/deeper", "sub/deeper/h.txt"} {
		if entries[want] == nil {
			t.Errorf("depth 3 with hidden is missing %q: %v", want, reflect.ValueOf(entries).MapKeys())
		}
	}
	for name := range entries {
		if strings.Contains(name, `\`) || strings.HasPrefix(name, "/") {
			t.Errorf("entry name %q must be a relative path with /", name)
		}
	}
	if _, followed := entries["link/g.txt"]; followed {
		t.Error("a directory symlink was followed")
	}
	fail(t, s, "fs.list", Object{"path": "d", "depth": 6}, "invalid_args")
	fail(t, s, "fs.list", Object{"path": "d/f.txt"}, "invalid_cwd")

	stat := call(t, s, "fs.stat", Object{"path": "d/f.txt"})
	if stat["exists"] != true || stat["type"] != "file" || stat["size"] != int64(5) || stat["symlink"] != false || stat["resolved_path"] != filepath.Join(root, "d", "f.txt") {
		t.Errorf("stat: %v", stat)
	}
	if mode, _ := stat["mode"].(string); len(mode) != 4 || strings.Trim(mode, "01234567") != "" {
		t.Errorf("mode must be an octal string: %v", stat["mode"])
	}
	if _, err := time.Parse(time.RFC3339Nano, stat["mtime"].(string)); err != nil {
		t.Errorf("mtime: %v", err)
	}
	missing := call(t, s, "fs.stat", Object{"path": "d/absent"})
	if missing["exists"] != false || missing["type"] != nil || missing["size"] != nil {
		t.Errorf("stat of a missing path: %v", missing)
	}
	hashed := call(t, s, "fs.sha256", Object{"path": "d/f.txt"})
	if hashed["sha256"] != Hash([]byte("hello")) || hashed["bytes"] != 5 || len(hashed["sha256"].(string)) != 64 || hashed["resolved_path"] != filepath.Join(root, "d", "f.txt") {
		t.Errorf("sha256: %v", hashed)
	}
	fail(t, s, "fs.sha256", Object{"path": "d"}, "invalid_cwd")
	fail(t, s, "fs.sha256", Object{"path": "d/absent"}, "invalid_cwd")
}

// fs.mkdir is idempotent for directories and conflicts with files; fs.move
// refuses an existing target unless overwrite is set and reports the target;
// fs.delete removes a file and reports the count, and reports a missing path.
// conformance: CTR-FS-07
func Test_CTRFS07_MkdirMoveDeleteFields(t *testing.T) {
	s, root := fixture(t)
	out := call(t, s, "fs.mkdir", Object{"path": "dir"})
	if out["created"] != true || out["resolved_path"] != filepath.Join(root, "dir") {
		t.Errorf("mkdir: %v", out)
	}
	if out = call(t, s, "fs.mkdir", Object{"path": "dir"}); out["created"] != false {
		t.Errorf("mkdir of an existing directory: %v", out)
	}
	writeBytes(t, filepath.Join(root, "file"), []byte("f"))
	fail(t, s, "fs.mkdir", Object{"path": "file"}, "conflict")
	if v := fail(t, s, "fs.mkdir", Object{"path": "x/y/z"}, "invalid_cwd"); v.Fields["reason"] != "parent_missing" {
		t.Errorf("mkdir reason: %v", v.Fields["reason"])
	}
	if out = call(t, s, "fs.mkdir", Object{"path": "x/y/z", "parents": true}); out["created"] != true {
		t.Errorf("mkdir -p: %v", out)
	}

	writeBytes(t, filepath.Join(root, "src.txt"), []byte("source"))
	writeBytes(t, filepath.Join(root, "dst.txt"), []byte("target"))
	fail(t, s, "fs.move", Object{"from": "src.txt", "to": "dst.txt"}, "conflict")
	if string(readBytes(t, filepath.Join(root, "src.txt"))) != "source" || string(readBytes(t, filepath.Join(root, "dst.txt"))) != "target" {
		t.Error("a refused move changed a file")
	}
	out = call(t, s, "fs.move", Object{"from": "src.txt", "to": "dst.txt", "overwrite": true})
	if out["resolved_path"] != filepath.Join(root, "dst.txt") || string(readBytes(t, filepath.Join(root, "dst.txt"))) != "source" {
		t.Errorf("move with overwrite: %v", out)
	}
	if _, err := os.Stat(filepath.Join(root, "src.txt")); !os.IsNotExist(err) {
		t.Error("the source still exists after the move")
	}
	fail(t, s, "fs.move", Object{"from": "absent", "to": "elsewhere"}, "invalid_cwd")
	fail(t, s, "fs.move", Object{"from": "dst.txt", "to": "nowhere/dst.txt"}, "invalid_cwd")

	out = call(t, s, "fs.delete", Object{"path": "dst.txt"})
	if out["removed"] != 1 || out["resolved_path"] != filepath.Join(root, "dst.txt") {
		t.Errorf("delete: %v", out)
	}
	if v := fail(t, s, "fs.delete", Object{"path": "dst.txt"}, "invalid_cwd"); v.Fields["reason"] != "not_found" {
		t.Errorf("delete of a missing path: %v", v.Fields)
	}
	out = call(t, s, "fs.delete", Object{"path": "x", "recursive": true})
	if out["removed"] != 3 {
		t.Errorf("recursive delete removed %v entries, want 3", out["removed"])
	}
}

// fs.read: line numbers are right-aligned in six columns followed by `|`;
// offset and limit count raw lines; limit defaults to 2000 and is capped at
// 5000; a missing path is not_found and a directory not_directory.
// conformance: CTR-FS-01
func Test_CTRFS01_LinesDefaultsAndPathErrors(t *testing.T) {
	s, root := fixture(t)
	var text strings.Builder
	for i := 1; i <= 2500; i++ {
		text.WriteString("line\n")
	}
	call(t, s, "fs.write", Object{"path": "long.txt", "content": text.String()})
	out := call(t, s, "fs.read", Object{"path": "long.txt"})
	content := out["content"].(string)
	if lines := strings.Count(content, "\n"); lines != 2000 || out["truncated"] != true || !strings.HasPrefix(content, "     1|line\n") || !strings.HasSuffix(content, "  2000|line\n") {
		t.Errorf("default limit: %d lines truncated=%v", strings.Count(content, "\n"), out["truncated"])
	}
	out = call(t, s, "fs.read", Object{"path": "long.txt", "offset": 2400, "limit": 5000})
	if strings.Count(out["content"].(string), "\n") != 101 || out["truncated"] != false || !strings.HasPrefix(out["content"].(string), "  2400|line\n") {
		t.Errorf("offset: truncated=%v", out["truncated"])
	}
	fail(t, s, "fs.read", Object{"path": "long.txt", "limit": 5001}, "invalid_args")
	fail(t, s, "fs.read", Object{"path": "long.txt", "offset": 0}, "invalid_args")
	if v := fail(t, s, "fs.read", Object{"path": "absent"}, "invalid_cwd"); v.Fields["reason"] != "not_found" {
		t.Errorf("reason = %v", v.Fields["reason"])
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if v := fail(t, s, "fs.read", Object{"path": "dir"}, "invalid_cwd"); v.Fields["reason"] != "not_directory" {
		t.Errorf("a directory read: reason = %v", v.Fields["reason"])
	}
	empty := call(t, s, "fs.write", Object{"path": "empty", "content": ""})
	if empty["bytes"] != 0 {
		t.Fatal(empty)
	}
	if out = call(t, s, "fs.read", Object{"path": "empty"}); out["content"] != "" || out["truncated"] != false {
		t.Errorf("empty file: %v", out)
	}
}

// expected_sha256 on fs.write: a missing file is invalid_cwd/not_found and is
// not created; a mismatch is conflict with current_sha256 and mtime and leaves
// the file; success returns resolved_path, the new sha256 and the byte count.
// conformance: CTR-FS-02
func Test_CTRFS02_ExpectedHashAndResultFields(t *testing.T) {
	s, root := fixture(t)
	out := call(t, s, "fs.write", Object{"path": "f", "content": "v1"})
	if out["resolved_path"] != filepath.Join(root, "f") || out["sha256"] != Hash([]byte("v1")) || out["bytes"] != 2 {
		t.Errorf("write result: %v", out)
	}
	if v := fail(t, s, "fs.write", Object{"path": "new", "content": "x", "expected_sha256": Hash([]byte("v1"))}, "invalid_cwd"); v.Fields["reason"] != "not_found" {
		t.Errorf("reason = %v", v.Fields["reason"])
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Error("a write with expected_sha256 created a missing file")
	}
	v := fail(t, s, "fs.write", Object{"path": "f", "content": "v2", "expected_sha256": Hash([]byte("other"))}, "conflict")
	if v.Fields["current_sha256"] != Hash([]byte("v1")) || v.Fields["path"] != filepath.Join(root, "f") {
		t.Errorf("conflict fields: %v", v.Fields)
	}
	if _, err := time.Parse(time.RFC3339Nano, v.Fields["mtime"].(string)); err != nil {
		t.Errorf("conflict mtime: %v", err)
	}
	if string(readBytes(t, filepath.Join(root, "f"))) != "v1" {
		t.Error("a conflicting write changed the file")
	}
	out = call(t, s, "fs.write", Object{"path": "f", "content": "v2", "expected_sha256": Hash([]byte("v1"))})
	if out["sha256"] != Hash([]byte("v2")) || string(readBytes(t, filepath.Join(root, "f"))) != "v2" {
		t.Errorf("write with the right hash: %v", out)
	}
}
