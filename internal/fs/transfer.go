package fs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

// Chunks keep memory and MCP envelopes bounded independently of file size.
const transferChunkLimit = 1 << 20
const transferIdle = 10 * time.Minute

type fileTransfer struct {
	file                        *os.File
	path, temp, owner, expected string
	upload, force               bool
	offset                      int64
	initial                     os.FileInfo
	digest                      hash.Hash
	touched                     time.Time
}

type Transfers struct {
	mu         sync.Mutex
	sessions   map[string]*fileTransfer
	stop, done chan struct{}
	once       sync.Once
}

func NewTransfers() *Transfers {
	m := &Transfers{sessions: map[string]*fileTransfer{}, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-m.stop:
				return
			case now := <-tick.C:
				m.mu.Lock()
				m.expire(now)
				m.mu.Unlock()
			}
		}
	}()
	return m
}

func (m *Transfers) discard(id string) {
	if s := m.sessions[id]; s != nil {
		s.file.Close()
		if s.temp != "" {
			os.Remove(s.temp)
		}
		delete(m.sessions, id)
	}
}

func (m *Transfers) expire(now time.Time) {
	for id, s := range m.sessions {
		if now.Sub(s.touched) >= transferIdle {
			m.discard(id)
		}
	}
}

func (m *Transfers) Close() {
	m.once.Do(func() {
		close(m.stop)
		<-m.done
		m.mu.Lock()
		defer m.mu.Unlock()
		for id := range m.sessions {
			m.discard(id)
		}
	})
}

func (s *Service) transfer(ctx context.Context, path string, a Args, upload bool) (Object, error) {
	limit, err := s.TransferConfig.Limit()
	if err != nil {
		return nil, toolerr.Internal("fs.transfer.config", err)
	}
	if a.ByteOffset < 0 || a.ChunkSize < 1 || a.ChunkSize > transferChunkLimit {
		return nil, toolerr.Args("byte_offset/chunk_size")
	}
	if err := ctx.Err(); err != nil {
		if m := s.Transfers; m != nil && a.TransferID != "" {
			m.mu.Lock()
			if t := m.sessions[a.TransferID]; t != nil && t.owner == s.Caller && t.path == path && t.upload == upload {
				m.discard(a.TransferID)
			}
			m.mu.Unlock()
		}
		return nil, err
	}
	m := s.Transfers
	if m == nil {
		return nil, toolerr.Internal("fs.transfer", io.ErrClosedPipe)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.stop:
		return nil, toolerr.Internal("fs.transfer", io.ErrClosedPipe)
	default:
	}
	m.expire(time.Now())
	id := a.TransferID
	var chunk []byte
	if upload {
		if len(a.Data) > base64.StdEncoding.EncodedLen(transferChunkLimit) {
			return nil, transferTooLarge(path, int64(base64.StdEncoding.DecodedLen(len(a.Data))), transferChunkLimit)
		}
		chunk, err = base64.StdEncoding.Strict().DecodeString(a.Data)
		if err != nil {
			return nil, toolerr.Args("data")
		}
		if len(chunk) > transferChunkLimit {
			return nil, transferTooLarge(path, int64(len(chunk)), transferChunkLimit)
		}
		decoded, err := hex.DecodeString(a.SHA256)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != a.SHA256 {
			return nil, toolerr.Args("sha256")
		}
	}
	t := m.sessions[id]
	if id != "" {
		if t == nil || t.owner != s.Caller || t.path != path || t.upload != upload {
			return nil, toolerr.Args("transfer_id")
		}
		if upload && (t.expected != a.SHA256 || t.force != a.Force) {
			return nil, toolerr.Args("sha256/force")
		}
	} else {
		if a.ByteOffset != 0 {
			return nil, toolerr.Args("byte_offset")
		}
		count := 0
		for _, t := range m.sessions {
			if t.owner == s.Caller {
				count++
			}
		}
		if len(m.sessions) >= 64 || count >= 8 {
			return nil, toolerr.New("policy_denied", "文件传输会话数已达上限", "rule", "fs.transfer.sessions", "reason", "每个调用方最多 8 个，节点最多 64 个")
		}
		t = &fileTransfer{path: path, owner: s.Caller, upload: upload, force: a.Force, expected: a.SHA256, digest: sha256.New()}
		if upload {
			if err := uploadTarget(path, a.Force); err != nil {
				return nil, err
			}
			t.file, err = os.CreateTemp(filepath.Dir(path), ".xmesh-upload-*")
			if err == nil {
				t.temp = t.file.Name()
			}
		} else {
			var st os.FileInfo
			st, err = os.Stat(path)
			if err == nil && !st.Mode().IsRegular() {
				return nil, toolerr.Path(path, "not_directory")
			}
			if err == nil {
				t.file, err = openRead(path)
			}
			if err == nil {
				t.initial, err = t.file.Stat()
			}
		}
		if err != nil {
			if t.file != nil {
				t.file.Close()
			}
			if t.temp != "" {
				os.Remove(t.temp)
			}
			return nil, toolerr.Internal("fs.transfer.open", err)
		}
		if !upload && !t.initial.Mode().IsRegular() {
			t.file.Close()
			return nil, toolerr.Path(path, "not_directory")
		}
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			t.file.Close()
			if t.temp != "" {
				os.Remove(t.temp)
			}
			return nil, toolerr.Internal("fs.transfer.id", err)
		}
		id = hex.EncodeToString(token[:])
		m.sessions[id] = t
	}
	t.touched = time.Now()
	if a.ByteOffset != t.offset {
		return nil, toolerr.New("invalid_args", "分块偏移不匹配", "field", "byte_offset", "next_offset", t.offset)
	}
	abort := func(err error) (Object, error) { m.discard(id); return nil, err }
	if upload {
		if limit > 0 && (t.offset > limit || int64(len(chunk)) > limit-t.offset) {
			return abort(transferTooLarge(path, t.offset+int64(len(chunk)), limit))
		}
		if _, err := t.file.Write(chunk); err != nil {
			return abort(toolerr.Internal("fs.upload.write", err))
		}
		t.digest.Write(chunk)
		t.offset += int64(len(chunk))
		if err := ctx.Err(); err != nil {
			return abort(err)
		}
		if !a.Final {
			return Object{"resolved_path": path, "transfer_id": id, "next_offset": t.offset, "bytes": t.offset, "complete": false}, nil
		}
		sum := hex.EncodeToString(t.digest.Sum(nil))
		if sum != t.expected {
			return abort(toolerr.New("invalid_args", "上传内容的 SHA-256 校验失败", "field", "sha256"))
		}
		if err := uploadTarget(path, t.force); err != nil {
			return abort(err)
		}
		mode := os.FileMode(0600)
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		if err := t.file.Chmod(mode); err != nil {
			return abort(toolerr.Internal("fs.upload.chmod", err))
		}
		if err := t.file.Sync(); err != nil {
			return abort(toolerr.Internal("fs.upload.sync", err))
		}
		if err := t.file.Close(); err != nil {
			return abort(toolerr.Internal("fs.upload.close", err))
		}
		if err := ctx.Err(); err != nil {
			return abort(err)
		}
		if t.force {
			err = atomicReplace(t.temp, path)
		} else {
			err = atomicCreate(t.temp, path)
		}
		if err != nil {
			if !t.force {
				if _, statErr := os.Lstat(path); os.IsExist(err) || statErr == nil {
					return abort(uploadConflict(path))
				}
			}
			return abort(toolerr.Internal("fs.upload.publish", err))
		}
		m.discard(id)
		return Object{"resolved_path": path, "sha256": sum, "bytes": t.offset, "complete": true}, nil
	}
	if limit > 0 && t.initial.Size() > limit {
		return abort(transferTooLarge(path, t.initial.Size(), limit))
	}
	st, err := t.file.Stat()
	if err != nil {
		return abort(toolerr.Internal("fs.download.stat", err))
	}
	if st.Size() != t.initial.Size() || !st.ModTime().Equal(t.initial.ModTime()) {
		return abort(downloadConflict(path))
	}
	remaining := t.initial.Size() - t.offset
	data := make([]byte, int(min(int64(a.ChunkSize), remaining)))
	if _, err := io.ReadFull(t.file, data); err != nil {
		return abort(downloadConflict(path))
	}
	t.digest.Write(data)
	t.offset += int64(len(data))
	st, err = t.file.Stat()
	if err != nil {
		return abort(toolerr.Internal("fs.download.stat", err))
	}
	if st.Size() != t.initial.Size() || !st.ModTime().Equal(t.initial.ModTime()) {
		return abort(downloadConflict(path))
	}
	if err := ctx.Err(); err != nil {
		return abort(err)
	}
	eof := t.offset == t.initial.Size()
	out := Object{"resolved_path": path, "encoding": "base64", "data": base64.StdEncoding.EncodeToString(data), "sha256": Hash(data), "bytes": len(data), "file_bytes": t.initial.Size(), "next_offset": t.offset, "eof": eof}
	if eof {
		out["file_sha256"] = hex.EncodeToString(t.digest.Sum(nil))
		m.discard(id)
	} else {
		out["transfer_id"] = id
	}
	return out, nil
}

func uploadTarget(path string, force bool) error {
	if st, err := os.Lstat(path); err == nil {
		if !force {
			return uploadConflict(path)
		}
		if !st.Mode().IsRegular() {
			return toolerr.Path(path, "not_directory")
		}
	} else if !os.IsNotExist(err) {
		return toolerr.Internal("fs.upload", err)
	}
	return nil
}

func uploadConflict(path string) *toolerr.Error {
	return toolerr.New("conflict", "上传目标已存在；覆盖需要 force: true", "path", path, "op", "fs.upload")
}
func downloadConflict(path string) *toolerr.Error {
	return toolerr.New("conflict", "下载期间源文件发生变化，请重试", "path", path, "op", "fs.download")
}
func transferTooLarge(path string, size, limit int64) *toolerr.Error {
	return toolerr.New("too_large", "超过文件或分块大小上限", "path", path, "size", size, "limit", limit)
}
