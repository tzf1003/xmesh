//go:build !windows

package delegate

import (
	"errors"
	"math"
	"net"
	"os"
	"syscall"
	"time"
)

// processAlive reports whether a process with pid exists. A process owned by another user still counts. A pid above the
// signed 32-bit range names no process: the kernel reads only its low bits, which may name a live one.
func processAlive(pid int) bool {
	if pid <= 0 || int64(pid) > math.MaxInt32 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// deliver writes payload to the Unix socket at path, with a write deadline of sessionWriteTimeout. A failure is
// reported as one of the reasons of CTR-DLG-04: socket_missing, connect_failed, write_timeout or write_failed.
func deliver(path string, payload []byte) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "socket_missing", err
		}
		return "connect_failed", err
	}
	conn, err := net.DialTimeout("unix", path, sessionWriteTimeout)
	if err != nil {
		return "connect_failed", err
	}
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(sessionWriteTimeout)); err != nil {
		return "write_failed", err
	}
	if _, err := conn.Write(payload); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return "write_timeout", err
		}
		return "write_failed", err
	}
	return "", nil
}
