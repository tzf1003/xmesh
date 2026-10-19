package ci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tarGz builds a tar.gz from name → content; names ending in "/" are directories, a content that
// starts with "->" is a symlink, and a name starting with "!" is executable.
func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, content := range files {
		h := &tar.Header{Name: strings.TrimPrefix(name, "!"), Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		switch {
		case strings.HasSuffix(name, "/"):
			h.Typeflag, h.Mode, h.Size = tar.TypeDir, 0o755, 0
		case strings.HasPrefix(content, "->"):
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, strings.TrimPrefix(content, "->"), 0
		case strings.HasPrefix(name, "!"):
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeSource serves one release and counts what was asked of it.
type fakeSource struct {
	release  Release
	data     []byte
	latest   int
	fetches  int
	failLate error
}

func newFakeSource(t *testing.T, version string, files map[string]string) *fakeSource {
	data := tarGz(t, files)
	sum := sha256.Sum256(data)
	return &fakeSource{
		data:    data,
		release: Release{Version: version, Asset: "actions-runner-linux-x64-" + version + ".tar.gz", URL: "https://example.invalid/r", SHA256: hex.EncodeToString(sum[:])},
	}
}

func (f *fakeSource) Latest(context.Context, string) (Release, error) {
	f.latest++
	if f.failLate != nil {
		return Release{}, f.failLate
	}
	return f.release, nil
}

func (f *fakeSource) Fetch(_ context.Context, _ Release, w io.Writer) error {
	f.fetches++
	_, err := w.Write(f.data)
	return err
}

// conformance: CTR-CTL-15
func Test_CTRCTL15_PackageIsVerifiedCachedAndFallsBack(t *testing.T) {
	src := newFakeSource(t, "2.329.0", map[string]string{"run.sh": "x"})
	cache := &Cache{Dir: t.TempDir(), Source: src, Platform: "linux-x64"}
	ctx := context.Background()

	pkg, err := cache.Ensure(ctx, false)
	if err != nil || pkg.Version != "2.329.0" || src.fetches != 1 {
		t.Fatalf("first Ensure = %+v, %v (fetches %d)", pkg, err, src.fetches)
	}
	if _, err = cache.Ensure(ctx, false); err != nil || src.fetches != 1 || src.latest != 1 {
		t.Fatalf("a cached package must not be fetched again: fetches %d latest %d err %v", src.fetches, src.latest, err)
	}
	if _, err = cache.Ensure(ctx, true); err != nil || src.fetches != 1 {
		t.Fatalf("an up-to-date cache must not download again: fetches %d err %v", src.fetches, err)
	}

	src.failLate = errors.New("offline")
	if pkg, err = cache.Ensure(ctx, true); err != nil || pkg.Version != "2.329.0" {
		t.Fatalf("an unreachable GitHub falls back to the cached package: %+v, %v", pkg, err)
	}

	src.failLate = nil
	next := newFakeSource(t, "2.330.0", map[string]string{"run.sh": "y"})
	next.release.Asset = "actions-runner-linux-x64-2.330.0.tar.gz"
	cache.Source = next
	if pkg, err = cache.Ensure(ctx, true); err != nil || pkg.Version != "2.330.0" || next.fetches != 1 {
		t.Fatalf("a newer release is downloaded: %+v, %v", pkg, err)
	}
	if got, _ := cache.Cached(); got.Version != "2.330.0" {
		t.Errorf("Cached = %+v, want the newest", got)
	}
}

func Test_CTRCTL15_PackageWithWrongChecksumIsRejected(t *testing.T) {
	src := newFakeSource(t, "2.329.0", map[string]string{"run.sh": "x"})
	src.release.SHA256 = strings.Repeat("0", 64)
	cache := &Cache{Dir: t.TempDir(), Source: src, Platform: "linux-x64"}
	if _, err := cache.Ensure(context.Background(), false); !errors.Is(err, ErrNoPackage) || !strings.Contains(err.Error(), "校验和") {
		t.Fatalf("Ensure = %v, want a checksum failure", err)
	}
	entries, _ := os.ReadDir(cache.Dir)
	if len(entries) != 0 {
		t.Errorf("a rejected download left %d files behind", len(entries))
	}
}

func Test_CTRCTL15_GitHubSourceReadsReleaseAndChecksum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/actions/runner/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v2.329.0",
			"body":     fmt.Sprintf("notes\n<!-- BEGIN SHA linux-x64 -->%s<!-- END SHA linux-x64 -->\n<!-- BEGIN SHA osx-arm64 -->%s<!-- END SHA osx-arm64 -->", sum, strings.Repeat("cd", 32)),
			"assets": []map[string]string{
				{"name": "actions-runner-linux-x64-2.329.0.tar.gz", "browser_download_url": "https://example.invalid/linux"},
				{"name": "actions-runner-osx-arm64-2.329.0.tar.gz", "browser_download_url": "https://example.invalid/osx"},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	source := GitHubSource{APIBase: server.URL}
	r, err := source.Latest(context.Background(), "osx-arm64")
	if err != nil || r.Version != "2.329.0" || r.URL != "https://example.invalid/osx" || r.SHA256 != strings.Repeat("cd", 32) {
		t.Fatalf("Latest = %+v, %v", r, err)
	}
	if _, err = source.Latest(context.Background(), "win-x64"); err == nil {
		t.Error("a platform with no asset must fail")
	}
}

func Test_CTRCTL15_PlatformNames(t *testing.T) {
	for _, c := range []struct {
		goos, goarch, want string
		ok                 bool
	}{
		{"linux", "amd64", "linux-x64", true},
		{"darwin", "arm64", "osx-arm64", true},
		{"windows", "amd64", "win-x64", true},
		{"linux", "arm", "linux-arm", true},
		{"windows", "386", "", false},
		{"freebsd", "amd64", "", false},
	} {
		if got, ok := platformOf(c.goos, c.goarch); got != c.want || ok != c.ok {
			t.Errorf("platformOf(%s,%s) = %q,%v", c.goos, c.goarch, got, ok)
		}
	}
}

func Test_CTRCTL15_ExtractRefusesEscapes(t *testing.T) {
	write := func(files map[string]string) string {
		p := filepath.Join(t.TempDir(), "a.tar.gz")
		if err := os.WriteFile(p, tarGz(t, files), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dst := t.TempDir()
	if err := Extract(write(map[string]string{"bin/": "", "!run.sh": "#!/bin/sh\n", "bin/tool": "->run.sh"}), dst); err != nil {
		t.Fatalf("a normal package: %v", err)
	}
	// Windows has no executable bit to keep.
	if info, err := os.Stat(filepath.Join(dst, "run.sh")); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0) {
		t.Errorf("run.sh must stay executable: %v %v", info, err)
	}
	for name, files := range map[string]map[string]string{
		"dot-dot path":  {"../evil": "x"},
		"absolute path": {"/etc/evil": "x"},
		"link outside":  {"link": "->../../outside"},
		"absolute link": {"link": "->/etc/passwd"},
	} {
		if err := Extract(write(files), t.TempDir()); err == nil {
			t.Errorf("%s was extracted", name)
		}
	}
}

func Test_ExtractContainsSymlinkChains(t *testing.T) {
	// a resolves to the root; a/b's lexical parent is a but its real parent is the root.
	archive := filepath.Join(t.TempDir(), "chain.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range []*tar.Header{
		{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0755},
		{Name: "a/b", Typeflag: tar.TypeSymlink, Linkname: "../outside", Mode: 0755},
		{Name: "b/victim", Typeflag: tar.TypeReg, Size: 4, Mode: 0600},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte("evil")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	f.Close()
	parent := t.TempDir()
	dst, outside := filepath.Join(parent, "runner"), filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive, dst); err == nil {
		t.Fatal("chain escaped root")
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "keep" {
		t.Fatalf("outside overwritten: %q %v", got, err)
	}
}

func Test_ExtractReplacesHardlinkedFile(t *testing.T) {
	dst := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, filepath.Join(dst, "run.sh")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "runner.tar.gz")
	if err := os.WriteFile(archive, tarGz(t, map[string]string{"!run.sh": "updated"}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "keep" {
		t.Fatalf("hardlink target changed: %q %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "run.sh")); err != nil || string(got) != "updated" {
		t.Fatalf("install failed: %q %v", got, err)
	}
}

func Test_ExtractRejectsTerminalEscapingSymlink(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "links.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range []*tar.Header{
		{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0755},
		{Name: "a/b", Typeflag: tar.TypeSymlink, Linkname: "../outside", Mode: 0755},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	f.Close()
	if err := Extract(archive, t.TempDir()); err == nil {
		t.Fatal("escaping link accepted at archive end")
	}
}

func Test_ExtractKeepsSafeForwardSymlink(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "forward.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range []*tar.Header{
		{Name: "bin/tool", Typeflag: tar.TypeSymlink, Linkname: "../run.sh", Mode: 0755},
		{Name: "run.sh", Typeflag: tar.TypeReg, Size: 4, Mode: 0755},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte("safe"))
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
	dst := t.TempDir()
	if err := Extract(archive, dst); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "bin", "tool")); err != nil || string(got) != "safe" {
		t.Fatalf("%q %v", got, err)
	}
}

func Test_ExtractPreservesExistingJobWorkspaceLinks(t *testing.T) {
	dst := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(dst, "_work"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dst, "_work", "job-link")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink privilege unavailable")
		}
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "runner.tar.gz")
	if err := os.WriteFile(archive, tarGz(t, map[string]string{"!run.sh": "updated"}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive, dst); err != nil {
		t.Fatal("workspace link blocked reinstall", err)
	}
	if target, err := os.Readlink(link); err != nil || target != outside {
		t.Fatalf("workspace link changed: %q %v", target, err)
	}
}
