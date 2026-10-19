package ci

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
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
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	if strings.HasSuffix(archive, ".zip") {
		err = extractZip(archive, root)
	} else {
		err = extractTarGz(archive, root)
	}
	if err != nil {
		return err
	}
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if err := validateArchiveLink(root, name); err != nil {
				_ = root.Remove(name)
				return err
			}
		}
		return nil
	})
}

// rooted reports whether name starts at a root on any system. A leading slash is rooted on Windows too, where
// filepath.IsAbs would say it is not and joining it to dst would quietly place it inside.
func rooted(name string) bool {
	return strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) || filepath.VolumeName(name) != "" || filepath.IsAbs(name)
}

// inside resolves name below dst and reports whether it stays there.
func inside(dst, name string) (string, error) {
	clean := filepath.Join(dst, filepath.FromSlash(name))
	rel, err := filepath.Rel(dst, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rooted(name) {
		return "", fmt.Errorf("压缩包里的路径越界: %q", name)
	}
	return rel, nil
}

func extractTarGz(archive string, root *os.Root) error {
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
		target, err := inside(".", h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(target, 0o755)
		case tar.TypeReg:
			total += h.Size
			if total > maxExtracted {
				return fmt.Errorf("压缩包解开后超过 %d 字节", int64(maxExtracted))
			}
			err = writeFile(root, target, tr, h.FileInfo().Mode().Perm())
		case tar.TypeSymlink:
			err = makeSymlink(root, target, h.Linkname)
		case tar.TypeLink:
			var from string
			if from, err = inside(".", h.Linkname); err == nil {
				err = root.Link(from, target)
			}
		default:
			// Devices, fifos and the like have no place in a runner package.
		}
		if err != nil {
			return err
		}
	}
}

func makeSymlink(root *os.Root, target, link string) error {
	if rooted(link) {
		return fmt.Errorf("压缩包里的链接是绝对路径: %q", link)
	}
	resolved := filepath.Join(filepath.Dir(target), filepath.FromSlash(link))
	if rel, err := filepath.Rel(".", resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("压缩包里的链接指向目录之外: %q", link)
	}
	if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := root.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return root.Symlink(link, target)
}

func extractZip(archive string, root *os.Root) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	var total int64
	for _, f := range zr.File {
		target, err := inside(".", f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err = root.MkdirAll(target, 0o755); err != nil {
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
		err = writeFile(root, target, rc, f.Mode().Perm())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(root *os.Root, path string, r io.Reader, mode os.FileMode) error {
	if err := root.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	// Replace the entry rather than truncating a pre-existing symlink or hard link.
	if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	out, err := root.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Resolve link components individually so aliases cannot change the meaning of '..'.
// Missing components remain lexical: forward and dangling links stay valid within the root.
func validateArchiveLink(root *os.Root, name string) error {
	pending := strings.Split(filepath.FromSlash(name), string(filepath.Separator))
	var resolved []string
	hops := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return fmt.Errorf("压缩包里的链接指向目录之外: %q", name)
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		current := filepath.Join(append(append([]string{}, resolved...), part)...)
		st, err := root.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			hops++
			if hops > 40 {
				return fmt.Errorf("压缩包里的链接循环: %q", name)
			}
			link, err := root.Readlink(current)
			if err != nil {
				return err
			}
			if rooted(link) {
				return fmt.Errorf("压缩包里的链接是绝对路径: %q", name)
			}
			pending = append(strings.Split(filepath.FromSlash(link), string(filepath.Separator)), pending...)
			continue
		}
		resolved = append(resolved, part)
	}
	return nil
}
