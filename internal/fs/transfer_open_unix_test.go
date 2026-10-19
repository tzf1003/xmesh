//go:build !windows

package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

func Test_DownloadFIFOIsNonblocking(t *testing.T) {
	s, root := fixture(t)
	pipe := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		raw, _ := json.Marshal(Object{"path": "pipe"})
		_, err := s.Call(context.Background(), "fs.download", raw)
		done <- err
	}()
	select {
	case err := <-done:
		if e, ok := err.(*toolerr.Error); !ok || e.Code != "invalid_cwd" {
			t.Fatal("FIFO accepted", err)
		}
	case <-time.After(2 * time.Second):
		// Release an accidentally blocking opener before reporting the regression.
		f, _ := os.OpenFile(pipe, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if f != nil {
			f.Close()
		}
		t.Fatal("download blocked opening a FIFO")
	}
	call(t, s, "fs.upload", uploadArgs("after-pipe", []byte("ok"), false))
}
