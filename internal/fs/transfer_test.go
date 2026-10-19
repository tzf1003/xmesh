package fs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

func uploadArgs(path string, b []byte, force bool) Object {
	return Object{"path": path, "data": base64.StdEncoding.EncodeToString(b), "sha256": Hash(b), "force": force}
}

func uploadAll(t *testing.T, s *Service, path string, b []byte, force bool) Object {
	t.Helper()
	id := ""
	for offset := 0; ; {
		end := min(len(b), offset+transferChunkLimit)
		a := uploadArgs(path, b[offset:end], force)
		a["sha256"] = Hash(b)
		a["byte_offset"], a["transfer_id"], a["final"] = offset, id, end == len(b)
		out := call(t, s, "fs.upload", a)
		if end == len(b) {
			return out
		}
		id = out["transfer_id"].(string)
		offset = end
	}
}

func downloadAll(t *testing.T, s *Service, path string) Object {
	t.Helper()
	var data []byte
	args := Object{"path": path}
	for {
		out := call(t, s, "fs.download", args)
		chunk, err := base64.StdEncoding.DecodeString(out["data"].(string))
		if err != nil || out["sha256"] != Hash(chunk) {
			t.Fatal("invalid download chunk", err)
		}
		data = append(data, chunk...)
		if out["eof"] == true {
			if out["file_sha256"] != Hash(data) {
				t.Fatal("whole-file checksum mismatch")
			}
			return Object{"resolved_path": out["resolved_path"], "data": base64.StdEncoding.EncodeToString(data), "encoding": "base64", "sha256": out["file_sha256"], "bytes": len(data)}
		}
		args["transfer_id"], args["byte_offset"] = out["transfer_id"], out["next_offset"]
	}
}

// conformance: CTR-FS-08 INV-06
func Test_TransferRoundtrip(t *testing.T) {
	s, root := fixture(t)
	for name, b := range map[string][]byte{
		"empty": {}, "binary": {0, 0xff, 0x80, 1, 2},
		"bom-crlf": []byte("\xef\xbb\xbfhello\r\nworld\r\n"),
		"utf16":    {0xff, 0xfe, 'h', 0, 'i', 0},
		"boundary": bytes.Repeat([]byte{0, 1, 0xff, 0x80}, (2*transferChunkLimit+4)/4),
	} {
		t.Run(name, func(t *testing.T) {
			up := uploadAll(t, s, name, b, false)
			down := downloadAll(t, s, name)
			got, err := base64.StdEncoding.DecodeString(down["data"].(string))
			if err != nil || !bytes.Equal(b, got) || !bytes.Equal(b, readBytes(t, filepath.Join(root, name))) {
				t.Fatal("file bytes changed", err)
			}
			for _, out := range []Object{up, down} {
				if out["sha256"] != Hash(b) || fmt.Sprint(out["bytes"]) != fmt.Sprint(len(b)) || out["resolved_path"] != filepath.Join(root, name) {
					t.Fatal("transfer metadata", out["sha256"], out["bytes"], out["resolved_path"])
				}
			}
			if down["encoding"] != "base64" {
				t.Fatal("download encoding", down["encoding"])
			}
		})
	}
	// Explicit cwd and aliases use the same resolver as the other fs tools.
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	s.Resolver.Aliases = map[string]cwd.Workspace{"sub": {Path: sub}}
	a := uploadArgs("file", []byte("alias"), false)
	a["cwd"] = "@sub"
	call(t, s, "fs.upload", a)
	out := call(t, s, "fs.download", Object{"path": "file", "cwd": "@sub"})
	if out["resolved_path"] != filepath.Join(sub, "file") {
		t.Fatal(out)
	}
}

// conformance: CTR-FS-08 INV-03
func Test_UploadConflictAndValidation(t *testing.T) {
	s, root := fixture(t)
	old := []byte("original contents")
	path := filepath.Join(root, "keep")
	writeBytes(t, path, old)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0640); err != nil {
			t.Fatal(err)
		}
	}
	for name, a := range map[string]Object{
		"exists":               uploadArgs("keep", []byte("new"), false),
		"same-contents":        uploadArgs("keep", old, false),
		"bad-base64":           {"path": "keep", "data": "!!!", "sha256": Hash(old), "force": true},
		"noncanonical-padding": {"path": "keep", "data": "Zh==", "sha256": Hash([]byte("f")), "force": true},
		"hash-mismatch":        {"path": "keep", "data": "bmV3", "sha256": Hash(old), "force": true},
		"bad-hash":             {"path": "keep", "data": "", "sha256": strings.Repeat("z", 64), "force": true},
	} {
		t.Run(name, func(t *testing.T) {
			want := "invalid_args"
			if name == "exists" || name == "same-contents" {
				want = "conflict"
			}
			code(t, s, "fs.upload", a, want)
			if !bytes.Equal(readBytes(t, path), old) {
				t.Fatal("rejected upload changed target")
			}
		})
	}
	code(t, s, "fs.upload", Object{"path": "new", "data": "eA==", "sha256": Hash(nil)}, "invalid_args")
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatal("checksum failure created target", err)
	}
	call(t, s, "fs.upload", uploadArgs("keep", []byte("replacement"), true))
	if string(readBytes(t, path)) != "replacement" {
		t.Fatal("forced replacement failed")
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0640 {
			t.Fatal("replacement permissions changed", st, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("staging files remain", entries, err)
	}
}

func Test_TransferPathsLimitsAndCancellation(t *testing.T) {
	s, root := fixture(t)
	for _, name := range []string{"fs.upload", "fs.download"} {
		for _, path := range []string{"secret/file", "missing/child", filepath.Join(t.TempDir(), "outside")} {
			code(t, s, name, uploadArgs(path, nil, false), "invalid_cwd")
		}
	}
	code(t, s, "fs.upload", uploadArgs(root, nil, false), "conflict")
	code(t, s, "fs.upload", uploadArgs(root, nil, true), "invalid_cwd")
	code(t, s, "fs.download", Object{"path": root}, "invalid_cwd")
	code(t, s, "fs.download", Object{"path": "absent"}, "invalid_cwd")
	// Both a decoded overflow within the maximum encoded length and a longer
	// encoded body must be refused before creating a target.
	for _, size := range []int{transferChunkLimit + 1, transferChunkLimit + 4} {
		code(t, s, "fs.upload", uploadArgs("oversize", make([]byte, size), false), "too_large")
	}
	f, err := os.Create(filepath.Join(root, "huge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate((1 << 30) + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	code(t, s, "fs.download", Object{"path": "huge"}, "too_large")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{"fs.upload", "fs.download"} {
		writeBytes(t, filepath.Join(root, "cancel"), []byte("old"))
		raw, _ := json.Marshal(uploadArgs("cancel", []byte("new"), true))
		if _, err := s.Call(ctx, name, raw); !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation ignored", name, err)
		}
		if string(readBytes(t, filepath.Join(root, "cancel"))) != "old" {
			t.Fatal("cancelled transfer changed target")
		}
	}
	entries, _ := filepath.Glob(filepath.Join(root, ".xmesh-write-*"))
	if len(entries) != 0 {
		t.Fatal("staging files remain", entries)
	}
}

func Test_TransferSymlinkPolicy(t *testing.T) {
	s, root := fixture(t)
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "file")
	writeBytes(t, outsideFile, []byte("protected"))
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires a Windows privilege", err)
		}
		t.Fatal(err)
	}
	for _, name := range []string{"fs.upload", "fs.download"} {
		code(t, s, name, uploadArgs("escape/file", []byte("replacement"), true), "invalid_cwd")
	}
	if string(readBytes(t, outsideFile)) != "protected" {
		t.Fatal("escaped upload changed file")
	}
	writeBytes(t, filepath.Join(root, "target"), []byte("old"))
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	code(t, s, "fs.upload", uploadArgs("link", []byte("new"), false), "conflict")
	call(t, s, "fs.upload", uploadArgs("link", []byte("new"), true))
	out := call(t, s, "fs.download", Object{"path": "link"})
	if out["sha256"] != Hash([]byte("new")) || out["resolved_path"] != filepath.Join(root, "target") {
		t.Fatal("resolved transfer bytes", out)
	}
	if st, err := os.Lstat(filepath.Join(root, "link")); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("upload replaced symlink entry", st, err)
	}
}

// Separate Service instances model independent callers and processes. Only the
// atomic publication, rather than their independent mutexes, decides the winner.
func Test_UploadConcurrentCreate(t *testing.T) {
	s, root := fixture(t)
	const writers = 24
	type result struct {
		body []byte
		err  error
	}
	results := make(chan result, writers)
	start := make(chan struct{})
	for i := range writers {
		go func() {
			service := &Service{Resolver: s.Resolver, Transfers: s.Transfers}
			b := []byte(fmt.Sprintf("writer %d", i))
			raw, _ := json.Marshal(uploadArgs("winner", b, false))
			<-start
			_, err := service.Call(context.Background(), "fs.upload", raw)
			results <- result{b, err}
		}()
	}
	close(start)
	winners := 0
	var want []byte
	for range writers {
		r := <-results
		if r.err == nil {
			winners++
			want = r.body
		} else if e, ok := r.err.(*toolerr.Error); !ok || e.Code != "conflict" {
			t.Fatal("unexpected concurrent result", r.err)
		}
	}
	if winners != 1 || !bytes.Equal(readBytes(t, filepath.Join(root, "winner")), want) {
		t.Fatal("exclusive upload failed", winners)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("staging files remain", entries)
	}
}

// conformance: INV-06 CTR-FS-08
func Test_UploadAtomicReaders(t *testing.T) {
	s, root := fixture(t)
	a, b := bytes.Repeat([]byte("a"), 64<<10), bytes.Repeat([]byte("b"), 64<<10)
	call(t, s, "fs.upload", uploadArgs("target", a, false))
	reader, err := openRead(filepath.Join(root, "target"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stop := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			f, err := openRead(filepath.Join(root, "target"))
			if err != nil {
				errCh <- err
				return
			}
			got, err := io.ReadAll(f)
			f.Close()
			if err != nil || (!bytes.Equal(got, a) && !bytes.Equal(got, b)) {
				errCh <- fmt.Errorf("reader observed partial contents (%d bytes): %v", len(got), err)
				return
			}
		}
	})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			raw, _ := json.Marshal(Object{"path": "target"})
			out, err := s.Call(context.Background(), "fs.download", raw)
			if err != nil {
				errCh <- err
				return
			}
			got, err := base64.StdEncoding.DecodeString(out["data"].(string))
			if err != nil || (!bytes.Equal(got, a) && !bytes.Equal(got, b)) || out["sha256"] != Hash(got) {
				errCh <- fmt.Errorf("download observed partial contents: %v", err)
				return
			}
		}
	})
	defer func() {
		close(stop)
		wg.Wait()
		select {
		case err := <-errCh:
			t.Error(err)
		default:
		}
	}()
	for i := range 30 {
		body := b
		if i%2 != 0 {
			body = a
		}
		call(t, s, "fs.upload", uploadArgs("target", body, true))
		out := call(t, s, "fs.download", Object{"path": "target"})
		if out["sha256"] != Hash(body) {
			t.Fatal("download checksum changed")
		}
	}
	old, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(old, a) {
		t.Fatal("existing reader changed", err)
	}
}
