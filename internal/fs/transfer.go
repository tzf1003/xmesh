package fs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"strings"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

// The base64 payload and MCP envelope fit within the 16 MiB HTTP body limit.
const transferLimit = 10 << 20

func download(ctx context.Context, path string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Check the type before opening so special files such as FIFOs cannot block.
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, toolerr.Path(path, "not_found")
	}
	if err != nil {
		return nil, toolerr.Internal("fs.download", err)
	}
	if !st.Mode().IsRegular() {
		return nil, toolerr.Path(path, "not_directory")
	}
	f, err := openRead(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, toolerr.Path(path, "not_found")
		}
		return nil, toolerr.Internal("fs.download", err)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, toolerr.Internal("fs.download", err)
	}
	if !before.Mode().IsRegular() {
		return nil, toolerr.Path(path, "not_directory")
	}
	if before.Size() > transferLimit {
		return nil, transferTooLarge(path, before.Size())
	}
	b, err := io.ReadAll(io.LimitReader(f, transferLimit+1))
	if err != nil {
		return nil, toolerr.Internal("fs.download", err)
	}
	if len(b) > transferLimit {
		return nil, transferTooLarge(path, int64(len(b)))
	}
	// Read the same handle again to detect in-place edits during capture. Atomic
	// replacement leaves this handle on the complete original file.
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, toolerr.Internal("fs.download", err)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, transferLimit+1))
	if err != nil {
		return nil, toolerr.Internal("fs.download", err)
	}
	after, err := f.Stat()
	if err != nil {
		return nil, toolerr.Internal("fs.download", err)
	}
	sum := Hash(b)
	if n != int64(len(b)) || before.Size() != int64(len(b)) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || hex.EncodeToString(h.Sum(nil)) != sum {
		return nil, toolerr.New("conflict", "下载期间源文件发生变化，请重试", "path", path, "op", "fs.download")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return Object{"resolved_path": path, "encoding": "base64", "data": base64.StdEncoding.EncodeToString(b), "sha256": sum, "bytes": len(b)}, nil
}

func upload(ctx context.Context, path string, a Args) (Object, error) {
	if len(a.Data) > base64.StdEncoding.EncodedLen(transferLimit) {
		return nil, transferTooLarge(path, int64(base64.StdEncoding.DecodedLen(len(a.Data))))
	}
	b, err := base64.StdEncoding.Strict().DecodeString(a.Data)
	if err != nil {
		return nil, toolerr.Args("data")
	}
	if len(b) > transferLimit {
		return nil, transferTooLarge(path, int64(len(b)))
	}
	if len(a.SHA256) != 64 || a.SHA256 != strings.ToLower(a.SHA256) {
		return nil, toolerr.Args("sha256")
	}
	if _, err := hex.DecodeString(a.SHA256); err != nil {
		return nil, toolerr.Args("sha256")
	}
	if Hash(b) != a.SHA256 {
		return nil, toolerr.New("invalid_args", "上传内容的 SHA-256 校验失败", "field", "sha256")
	}
	if st, err := os.Lstat(path); err == nil {
		if !a.Force {
			return nil, uploadConflict(path)
		}
		if !st.Mode().IsRegular() {
			return nil, toolerr.Path(path, "not_directory")
		}
	} else if !os.IsNotExist(err) {
		return nil, toolerr.Internal("fs.upload", err)
	}
	if err := atomicStore(ctx, path, b, "", !a.Force); err != nil {
		return nil, err
	}
	return Object{"resolved_path": path, "sha256": a.SHA256, "bytes": len(b)}, nil
}

func uploadConflict(path string) *toolerr.Error {
	return toolerr.New("conflict", "上传目标已存在；覆盖需要 force: true", "path", path, "op", "fs.upload")
}

func transferTooLarge(path string, size int64) *toolerr.Error {
	return toolerr.New("too_large", "文件超过单次传输上限", "path", path, "size", size, "limit", transferLimit)
}
