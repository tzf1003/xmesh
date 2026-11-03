package adengine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Size limits for untrusted downloads. The tarball is about 15 MiB; the CLI and
// helpers are a few MiB each.
const (
	maxTarballBytes = 64 << 20
	maxMemberBytes  = 256 << 20
)

// Stager downloads the pinned tarball and installs the files its platform entry
// names. Each file is checked against the manifest before it is kept.
type Stager struct {
	Store  Store
	Client *http.Client
}

func (s Stager) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// Stage makes the manifest release available under Store.VersionDir and returns
// that directory. A version that already verifies is reused. Nothing is switched.
func (s Stager) Stage(ctx context.Context, m *Manifest, key string) (string, error) {
	p, ok := m.Platform(key)
	if !ok {
		return "", fmt.Errorf("agent-desktop %s 没有 %s 平台的产物", m.Version, key)
	}
	if err := s.Store.Ensure(); err != nil {
		return "", err
	}
	dir := s.Store.VersionDir(m.Version)
	if id := IdentifyManaged(s.Store, m.Version, key); id.Found && id.Problem == "" {
		return dir, nil
	}
	tmp, err := os.MkdirTemp(s.Store.Root, ".stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "archive.tgz")
	if err := s.download(ctx, m.Tarball, archive); err != nil {
		return "", err
	}
	files := filepath.Join(tmp, "files")
	if err := os.Mkdir(files, 0o700); err != nil {
		return "", err
	}
	r, err := extract(archive, p, key, files)
	if err != nil {
		return "", err
	}
	r.Version = m.Version
	r.Platform = key
	r.Integrity = m.Tarball.Integrity
	r.InstalledAt = time.Now().UTC()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(files, receiptName), append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Rename(files, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// download fetches the tarball and requires its sha512 to match the manifest.
func (s Stager) download(ctx context.Context, t Tarball, dst string) error {
	want, err := decodeIntegrity(t.Integrity)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("下载 agent-desktop 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 agent-desktop 失败: HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha512.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, maxTarballBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("下载 agent-desktop 失败: %w", err)
	}
	if n > maxTarballBytes {
		return errors.New("agent-desktop 归档超过大小上限")
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return errors.New("agent-desktop 归档 integrity 与清单不一致，拒绝安装")
	}
	return nil
}

// extract copies the platform's binary and helpers out of the tarball. Other
// members are ignored; each copied file must match its pinned SHA-256.
func extract(archive string, p Platform, key, dst string) (Receipt, error) {
	var r Receipt
	wanted := map[string]ReceiptFile{}
	bin := InstallName(p.Binary.Member, key)
	binSHA := strings.ToLower(p.Binary.SHA256)
	wanted[path.Join("package", p.Binary.Member)] = ReceiptFile{Name: bin, SHA256: binSHA}
	r.Binary = ReceiptFile{Name: bin, SHA256: binSHA}
	for _, h := range p.Helpers {
		name := InstallName(h.Member, key)
		sha := strings.ToLower(h.SHA256)
		wanted[path.Join("package", h.Member)] = ReceiptFile{Name: name, SHA256: sha}
		r.Helpers = append(r.Helpers, ReceiptFile{Name: name, SHA256: sha})
	}

	f, err := os.Open(archive)
	if err != nil {
		return r, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return r, fmt.Errorf("agent-desktop 归档不是 gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	done := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return r, fmt.Errorf("读取 agent-desktop 归档失败: %w", err)
		}
		if !hdr.FileInfo().Mode().IsRegular() {
			continue
		}
		file, ok := wanted[path.Clean(hdr.Name)]
		if !ok {
			continue
		}
		if done[file.Name] {
			return r, fmt.Errorf("归档中 %s 重复出现", hdr.Name)
		}
		if err := writeVerified(filepath.Join(dst, file.Name), tr, file.SHA256); err != nil {
			return r, fmt.Errorf("%s: %w", file.Name, err)
		}
		done[file.Name] = true
	}
	for archivePath, file := range wanted {
		if !done[file.Name] {
			return r, fmt.Errorf("归档缺少 %s", archivePath)
		}
	}
	return r, nil
}

// writeVerified streams one member to dst and checks its SHA-256 before the
// caller may use it. A mismatch leaves the file behind for the staging directory
// cleanup to remove.
func writeVerified(dst string, src io.Reader, wantSHA string) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(src, maxMemberBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxMemberBytes {
		return errors.New("文件超过大小上限")
	}
	if hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return errors.New("sha256 与清单不一致，拒绝安装")
	}
	return nil
}
