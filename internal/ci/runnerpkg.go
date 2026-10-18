package ci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Release is one downloadable runner package for this system.
type Release struct {
	Version string // 2.329.0
	Asset   string // actions-runner-linux-x64-2.329.0.tar.gz
	URL     string
	SHA256  string // lower-case hex
}

// Source finds and fetches the official runner package. The production source reads the
// actions/runner releases; tests supply their own.
type Source interface {
	// Latest returns the newest release for platform (see Platform).
	Latest(ctx context.Context, platform string) (Release, error)
	// Fetch writes the package named by r to w.
	Fetch(ctx context.Context, r Release, w io.Writer) error
}

// Platform names this system the way the runner's release assets do, or reports that GitHub
// publishes no runner for it.
func Platform() (string, bool) {
	return platformOf(runtime.GOOS, runtime.GOARCH)
}

func platformOf(goos, goarch string) (string, bool) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64", "arm": "arm"}[goarch]
	system := map[string]string{"linux": "linux", "darwin": "osx", "windows": "win"}[goos]
	if arch == "" || system == "" {
		return "", false
	}
	switch system + "-" + arch {
	case "linux-x64", "linux-arm64", "linux-arm", "osx-x64", "osx-arm64", "win-x64", "win-arm64":
		return system + "-" + arch, true
	}
	return "", false
}

// archiveExt is the package format of platform: zip on Windows, tar.gz elsewhere.
func archiveExt(platform string) string {
	if strings.HasPrefix(platform, "win-") {
		return ".zip"
	}
	return ".tar.gz"
}

// GitHubSource reads the actions/runner releases from the GitHub API.
type GitHubSource struct {
	Client *http.Client
	// APIBase defaults to https://api.github.com.
	APIBase string
}

func (g GitHubSource) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (g GitHubSource) base() string {
	if g.APIBase != "" {
		return strings.TrimRight(g.APIBase, "/")
	}
	return "https://api.github.com"
}

var shaMarker = regexp.MustCompile(`<!--\s*BEGIN SHA ([a-z0-9-]+)\s*-->([0-9a-fA-F]{64})<!--\s*END SHA`)

func (g GitHubSource) Latest(ctx context.Context, platform string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base()+"/repos/actions/runner/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.client().Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return Release{}, err
	}
	version := strings.TrimPrefix(body.TagName, "v")
	asset := "actions-runner-" + platform + "-" + version + archiveExt(platform)
	var url string
	for _, a := range body.Assets {
		if a.Name == asset {
			url = a.URL
		}
	}
	if url == "" {
		return Release{}, fmt.Errorf("发布 %s 里没有 %s", body.TagName, asset)
	}
	sum := ""
	for _, m := range shaMarker.FindAllStringSubmatch(body.Body, -1) {
		if m[1] == platform {
			sum = strings.ToLower(m[2])
		}
	}
	if sum == "" {
		return Release{}, fmt.Errorf("发布 %s 没有公布 %s 的校验和", body.TagName, platform)
	}
	return Release{Version: version, Asset: asset, URL: url, SHA256: sum}, nil
}

func (g GitHubSource) Fetch(ctx context.Context, r Release, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return err
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载返回 %d", resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// Cache keeps downloaded runner packages so each instance extracts from disk and a node without
// internet keeps working with the last package it fetched.
type Cache struct {
	Dir      string
	Source   Source
	Platform string
}

// Package is a verified runner archive on disk.
type Package struct {
	Path    string
	Version string
}

// Cached returns the newest verified package already on disk.
func (c *Cache) Cached() (Package, bool) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return Package{}, false
	}
	prefix, ext := "actions-runner-"+c.Platform+"-", archiveExt(c.Platform)
	var versions []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		versions = append(versions, strings.TrimSuffix(strings.TrimPrefix(name, prefix), ext))
	}
	if len(versions) == 0 {
		return Package{}, false
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[j], versions[i]) })
	return Package{Path: filepath.Join(c.Dir, prefix+versions[0]+ext), Version: versions[0]}, true
}

// versionLess orders dotted numeric versions.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		var x, y int
		fmt.Sscanf(pa[i], "%d", &x)
		fmt.Sscanf(pb[i], "%d", &y)
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

// ErrNoPackage means no package is cached and none could be fetched.
var ErrNoPackage = errors.New("没有可用的运行器安装包")

// Ensure returns a package for this system. With refresh false a cached package is used as is;
// with refresh true the newest release is fetched when it is newer, and a failure to reach
// GitHub falls back to the cached package.
func (c *Cache) Ensure(ctx context.Context, refresh bool) (Package, error) {
	cached, have := c.Cached()
	if have && !refresh {
		return cached, nil
	}
	release, err := c.Source.Latest(ctx, c.Platform)
	if err != nil {
		if have {
			return cached, nil
		}
		return Package{}, fmt.Errorf("%w: %v", ErrNoPackage, err)
	}
	if have && !versionLess(cached.Version, release.Version) {
		return cached, nil
	}
	pkg, err := c.download(ctx, release)
	if err != nil {
		if have {
			return cached, nil
		}
		return Package{}, fmt.Errorf("%w: %v", ErrNoPackage, err)
	}
	return pkg, nil
}

// download fetches release into a temporary file, checks its SHA-256 and moves it into place.
func (c *Cache) download(ctx context.Context, r Release) (Package, error) {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return Package{}, err
	}
	tmp, err := os.CreateTemp(c.Dir, "download-*")
	if err != nil {
		return Package{}, err
	}
	defer os.Remove(tmp.Name())
	hash := sha256.New()
	if err = c.Source.Fetch(ctx, r, io.MultiWriter(tmp, hash)); err != nil {
		tmp.Close()
		return Package{}, err
	}
	if err = tmp.Close(); err != nil {
		return Package{}, err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != strings.ToLower(r.SHA256) {
		return Package{}, fmt.Errorf("校验和不符: 期望 %s，实际 %s", r.SHA256, got)
	}
	final := filepath.Join(c.Dir, r.Asset)
	if err = os.Rename(tmp.Name(), final); err != nil {
		return Package{}, err
	}
	return Package{Path: final, Version: r.Version}, nil
}
