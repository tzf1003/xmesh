//go:build !windows

package fs

import (
	"os"
	"syscall"
)

// A path can become a FIFO between resolution and open. O_NONBLOCK lets the
// caller validate the opened handle's type without waiting for a pipe writer.
func openTransferRead(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
