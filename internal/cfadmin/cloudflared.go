package cfadmin

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// maxCloudflaredSize bounds the download; the real binaries are about 40 MB.
const maxCloudflaredSize = 200 << 20

// CloudflaredInstaller downloads the official cloudflared release into a private directory.
// The release's published SHA-256 digest is checked before the file is made executable.
type CloudflaredInstaller struct {
	// API is the release-metadata URL; empty means the official cloudflare/cloudflared latest release.
	API  string
	HTTP *http.Client
	// GOOS and GOARCH default to the running platform.
	GOOS, GOARCH string
}

// cloudflaredDownloadPrefix is where the official release assets live; metadata pointing elsewhere is refused.
const cloudflaredDownloadPrefix = "https://github.com/cloudflare/cloudflared/releases/download/"

const cloudflaredLatest = "https://api.github.com/repos/cloudflare/cloudflared/releases/latest"

// CloudflaredBinaryName is the file name of the installed executable.
func CloudflaredBinaryName(goos string) string {
	if goos == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// FindCloudflared returns the configured or PATH cloudflared, else the one installed under dir.
func FindCloudflared(configured, dir string) (string, error) {
	if p, err := exec.LookPath(cmpOr(configured, "cloudflared")); err == nil {
		return p, nil
	}
	if dir != "" {
		p := filepath.Join(dir, CloudflaredBinaryName(runtime.GOOS))
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("找不到 cloudflared")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// assetName maps a platform to the file name cloudflare publishes for it.
func assetName(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64", "linux/arm64", "linux/arm":
		return "cloudflared-linux-" + goarch, nil
	case "darwin/amd64", "darwin/arm64":
		return "cloudflared-" + goos + "-" + goarch + ".tgz", nil
	case "windows/amd64":
		return "cloudflared-windows-amd64.exe", nil
	}
	return "", fmt.Errorf("没有适用于 %s/%s 的 cloudflared 官方发布，请手动安装", goos, goarch)
}

// Install downloads cloudflared into dir and returns its path. It does nothing when dir already holds one.
func (in CloudflaredInstaller) Install(ctx context.Context, dir string) (string, error) {
	goos, goarch := cmpOr(in.GOOS, runtime.GOOS), cmpOr(in.GOARCH, runtime.GOARCH)
	dest := filepath.Join(dir, CloudflaredBinaryName(goos))
	if info, err := os.Stat(dest); err == nil && !info.IsDir() {
		return dest, nil
	}
	name, err := assetName(goos, goarch)
	if err != nil {
		return "", err
	}
	client := in.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := getJSON(ctx, client, cmpOr(in.API, cloudflaredLatest), &rel); err != nil {
		return "", fmt.Errorf("查询 cloudflared 最新版本失败：%w", err)
	}
	var url, digest string
	for _, a := range rel.Assets {
		if a.Name == name {
			url, digest = a.URL, a.Digest
		}
	}
	if in.API == "" && !strings.HasPrefix(url, cloudflaredDownloadPrefix) {
		return "", fmt.Errorf("cloudflared 发布给出的下载地址不在官方仓库下，已拒绝：%s", url)
	}
	want, ok := strings.CutPrefix(digest, "sha256:")
	if url == "" || !ok || len(want) != 64 {
		return "", fmt.Errorf("cloudflared 发布 %s 里没有可校验的 %s", rel.TagName, name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	raw, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(raw.Name())
	sum, err := download(ctx, client, url, raw)
	_ = raw.Close()
	if err != nil {
		return "", fmt.Errorf("下载 cloudflared 失败：%w", err)
	}
	if sum != want {
		return "", fmt.Errorf("cloudflared 下载文件的 SHA-256 与发布不符，已丢弃")
	}
	exe := raw.Name()
	if strings.HasSuffix(name, ".tgz") {
		exe = raw.Name() + ".bin"
		defer os.Remove(exe)
		if err := extractCloudflared(raw.Name(), exe); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(exe, 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(exe, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// download streams url into w and returns the hex SHA-256 of what was written.
func download(ctx context.Context, client *http.Client, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxCloudflaredSize+1))
	if err != nil {
		return "", err
	}
	if n > maxCloudflaredSize {
		return "", errors.New("文件过大")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractCloudflared copies the cloudflared member out of a macOS .tgz.
func extractCloudflared(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("压缩包里没有 cloudflared")
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "cloudflared" {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, maxCloudflaredSize))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	}
}
