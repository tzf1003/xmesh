package ci

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxExtracted bounds what one runner package may unpack to; the real packages are a few hundred MB.
const maxExtracted = 4 << 30

// Extract unpacks the runner archive (tar.gz or zip, by extension) into dst. Entries that would
// land outside dst, and links that point outside it, are refused.
func Extract(archive, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	if strings.HasSuffix(archive, ".zip") {
		return extractZip(archive, dst)
	}
	return extractTarGz(archive, dst)
}

// inside resolves name below dst and reports whether it stays there.
func inside(dst, name string) (string, error) {
	clean := filepath.Join(dst, filepath.FromSlash(name))
	rel, err := filepath.Rel(dst, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
		return "", fmt.Errorf("压缩包里的路径越界: %q", name)
	}
	return clean, nil
}

func extractTarGz(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := inside(dst, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			total += h.Size
			if total > maxExtracted {
				return fmt.Errorf("压缩包解开后超过 %d 字节", int64(maxExtracted))
			}
			err = writeFile(target, tr, h.FileInfo().Mode().Perm())
		case tar.TypeSymlink:
			err = makeSymlink(dst, target, h.Linkname)
		case tar.TypeLink:
			var from string
			if from, err = inside(dst, h.Linkname); err == nil {
				err = os.Link(from, target)
			}
		default:
			// Devices, fifos and the like have no place in a runner package.
		}
		if err != nil {
			return err
		}
	}
}

func makeSymlink(dst, target, link string) error {
	resolved := link
	if !filepath.IsAbs(link) {
		resolved = filepath.Join(filepath.Dir(target), filepath.FromSlash(link))
	}
	if rel, err := filepath.Rel(dst, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("压缩包里的链接指向目录之外: %q", link)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	_ = os.Remove(target)
	return os.Symlink(link, target)
}

func extractZip(archive, dst string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	var total int64
	for _, f := range zr.File {
		target, err := inside(dst, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err = os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.FileInfo().Mode().IsRegular() {
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > maxExtracted {
			return fmt.Errorf("压缩包解开后超过 %d 字节", int64(maxExtracted))
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(target, rc, f.Mode().Perm())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
