package fs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
)

func Test_TransferLimitsAndUnlimited(t *testing.T) {
	s, root := fixture(t)
	s.TransferConfig.MaxFileSize = "2B"
	b := []byte("abc")
	a := uploadArgs("file", b[:2], false)
	a["sha256"], a["final"] = Hash(b), false
	first := call(t, s, "fs.upload", a)
	a["transfer_id"], a["byte_offset"], a["data"], a["final"] = first["transfer_id"], 2, base64.StdEncoding.EncodeToString(b[2:]), true
	code(t, s, "fs.upload", a, "too_large")
	if _, err := os.Stat(filepath.Join(root, "file")); !os.IsNotExist(err) {
		t.Fatal("over-limit upload published")
	}
	s.TransferConfig.MaxFileSize = "unlimited"
	uploadAll(t, s, "file", b, false)
	s.TransferConfig.MaxFileSize = "2B"
	code(t, s, "fs.download", Object{"path": "file"}, "too_large")
	s.TransferConfig.MaxFileSize = "unlimited"
	if out := downloadAll(t, s, "file"); out["sha256"] != Hash(b) {
		t.Fatal(out)
	}
	// A sparse file above 1 GiB tests the real default boundary with little disk use.
	f, err := os.Create(filepath.Join(root, "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate((1 << 30) + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s.TransferConfig = config.FileTransfer{}
	code(t, s, "fs.download", Object{"path": "sparse"}, "too_large")
	s.TransferConfig.MaxFileSize = "unlimited"
	out := call(t, s, "fs.download", Object{"path": "sparse", "chunk_size": 1})
	if out["file_bytes"] != int64(1<<30+1) || out["bytes"] != 1 || out["eof"] != false {
		t.Fatal("unlimited sparse download", out)
	}
}

func Test_TransferOwnershipOffsetsAndCleanup(t *testing.T) {
	s, root := fixture(t)
	s.Caller = "owner"
	writeBytes(t, filepath.Join(root, "keep"), []byte("old"))
	a := uploadArgs("keep", []byte("ne"), true)
	a["sha256"], a["final"] = Hash([]byte("new")), false
	first := call(t, s, "fs.upload", a)
	if string(readBytes(t, filepath.Join(root, "keep"))) != "old" {
		t.Fatal("partial upload published")
	}
	a["transfer_id"], a["byte_offset"], a["data"], a["final"] = first["transfer_id"], 2, "dw==", true
	s.Caller = "other"
	code(t, s, "fs.upload", a, "invalid_args")
	s.Caller = "owner"
	a["byte_offset"] = 1
	code(t, s, "fs.upload", a, "invalid_args")
	a["byte_offset"] = 2
	call(t, s, "fs.upload", a)
	if string(readBytes(t, filepath.Join(root, "keep"))) != "new" {
		t.Fatal("upload lost bytes")
	}
	code(t, s, "fs.upload", a, "invalid_args")
	// A competing creator wins before the final publication and is preserved.
	a = uploadArgs("race", []byte("a"), false)
	a["final"] = false
	first = call(t, s, "fs.upload", a)
	writeBytes(t, filepath.Join(root, "race"), []byte("competitor"))
	a["transfer_id"], a["byte_offset"], a["data"], a["final"] = first["transfer_id"], 1, "", true
	code(t, s, "fs.upload", a, "conflict")
	if string(readBytes(t, filepath.Join(root, "race"))) != "competitor" {
		t.Fatal("conflict overwritten")
	}
	// Idle stages and handles are bounded and reclaimed.
	for range 8 {
		a = uploadArgs("abandoned", nil, false)
		a["final"] = false
		call(t, s, "fs.upload", a)
	}
	code(t, s, "fs.upload", a, "policy_denied")
	s.Transfers.mu.Lock()
	s.Transfers.expire(time.Now().Add(transferIdle))
	s.Transfers.mu.Unlock()
	entries, _ := filepath.Glob(filepath.Join(root, ".xmesh-upload-*"))
	if len(entries) != 0 {
		t.Fatal("expired stages remain", entries)
	}
	call(t, s, "fs.upload", uploadArgs("after-expiry", nil, false))
}

func Test_TransferFinalChecksumAndLiveLimit(t *testing.T) {
	s, root := fixture(t)
	writeBytes(t, filepath.Join(root, "keep"), []byte("old"))
	a := uploadArgs("keep", []byte("bad"), true)
	a["sha256"], a["final"] = Hash([]byte("good")), false
	first := call(t, s, "fs.upload", a)
	a["transfer_id"], a["byte_offset"], a["data"], a["final"] = first["transfer_id"], 3, "", true
	code(t, s, "fs.upload", a, "invalid_args")
	if string(readBytes(t, filepath.Join(root, "keep"))) != "old" {
		t.Fatal("bad checksum replaced file")
	}
	a = uploadArgs("keep", []byte("new"), true)
	a["final"] = false
	first = call(t, s, "fs.upload", a)
	a["transfer_id"], a["byte_offset"], a["data"], a["final"] = first["transfer_id"], 3, "", true
	s.TransferConfig.MaxFileSize = "2B"
	code(t, s, "fs.upload", a, "too_large")
	if string(readBytes(t, filepath.Join(root, "keep"))) != "old" {
		t.Fatal("new limit did not preserve target")
	}
	entries, _ := filepath.Glob(filepath.Join(root, ".xmesh-upload-*"))
	if len(entries) != 0 {
		t.Fatal("failed stages remain", entries)
	}
}

func Test_TransferCancelledContinuation(t *testing.T) {
	s, root := fixture(t)
	a := uploadArgs("file", []byte("x"), false)
	a["final"] = false
	first := call(t, s, "fs.upload", a)
	a["transfer_id"], a["byte_offset"], a["data"], a["final"] = first["transfer_id"], 1, "", true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(a)
	if _, err := s.Call(ctx, "fs.upload", raw); err != context.Canceled {
		t.Fatal(err)
	}
	entries, _ := filepath.Glob(filepath.Join(root, ".xmesh-upload-*"))
	if len(entries) != 0 {
		t.Fatal("cancelled stage remains", entries)
	}
	code(t, s, "fs.upload", a, "invalid_args")
}

func Test_DownloadChangedSourceAndOwnership(t *testing.T) {
	s, root := fixture(t)
	writeBytes(t, filepath.Join(root, "source"), []byte("abcd"))
	s.Caller = "owner"
	first := call(t, s, "fs.download", Object{"path": "source", "chunk_size": 2})
	a := Object{"path": "source", "transfer_id": first["transfer_id"], "byte_offset": 2, "chunk_size": 2}
	s.Caller = "other"
	code(t, s, "fs.download", a, "invalid_args")
	s.Caller = "owner"
	writeBytes(t, filepath.Join(root, "source"), []byte("changed length"))
	code(t, s, "fs.download", a, "conflict")
	code(t, s, "fs.download", a, "invalid_args")
}

func Test_LargeFileTransfer(t *testing.T) {
	if os.Getenv("XMESH_TEST_LARGE_TRANSFER") != "1" {
		t.Skip("set XMESH_TEST_LARGE_TRANSFER=1 for the 1 GiB roundtrip")
	}
	s, _ := fixture(t)
	chunk := bytes.Repeat([]byte{0, 0xff, '\r', '\n'}, transferChunkLimit/4)
	h := sha256.New()
	for range 1024 {
		h.Write(chunk)
	}
	want := hex.EncodeToString(h.Sum(nil))
	a := uploadArgs("large.bin", chunk, false)
	a["sha256"] = want
	for i := range 1024 {
		a["byte_offset"], a["final"] = int64(i)*transferChunkLimit, i == 1023
		raw, _ := json.Marshal(a)
		out, err := s.Call(context.Background(), "fs.upload", raw)
		if err != nil {
			t.Fatal(i, err)
		}
		if i < 1023 {
			a["transfer_id"] = out["transfer_id"]
		} else if out["sha256"] != want {
			t.Fatal("large upload checksum")
		}
	}
	d := Object{"path": "large.bin"}
	for i := range 1024 {
		out := call(t, s, "fs.download", d)
		got, err := base64.StdEncoding.DecodeString(out["data"].(string))
		if err != nil || !bytes.Equal(chunk, got) {
			t.Fatal("large download chunk", i, err)
		}
		if out["eof"] == true {
			if i != 1023 || out["file_sha256"] != want {
				t.Fatal("large download checksum")
			}
			break
		}
		d["transfer_id"], d["byte_offset"] = out["transfer_id"], out["next_offset"]
	}
}
