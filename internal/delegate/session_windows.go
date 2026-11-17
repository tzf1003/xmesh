package delegate

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code that GetExitCodeProcess reports for a process that has not ended.
const stillActive = 259

// processAlive reports whether a process with pid is still running. A pid above the DWORD range names no process, and
// converting it would name another one.
func processAlive(pid int) bool {
	if pid <= 0 || int64(pid) > math.MaxUint32 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}

// pipePath says whether path names a Windows named pipe, in the \\.\pipe\ namespace. Only a pipe is opened here: an
// ordinary file opened the same way would have its beginning overwritten.
func pipePath(path string) bool {
	return strings.HasPrefix(strings.ToLower(filepath.ToSlash(path)), "//./pipe/")
}

// errNotPipe is the cause of a session endpoint that is not a named pipe.
var errNotPipe = errors.New("delegate: the session endpoint is not a named pipe")

// deliver opens the named pipe at path as a file and writes payload to it. A write to such a pipe has no deadline,
// so the write runs in its own goroutine and is given up after sessionWriteTimeout. The reasons match the Unix form.
func deliver(path string, payload []byte) (string, error) {
	if !pipePath(path) {
		// A path outside the pipe namespace is never opened. A missing one is still a missing socket, as on Unix
		// (CTR-DLG-04).
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return "socket_missing", err
		}
		return "connect_failed", errNotPipe
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "socket_missing", err
		}
		return "connect_failed", err
	}
	defer f.Close()
	done := make(chan error, 1)
	go func() {
		_, err := f.Write(payload)
		done <- err
	}()
	timer := time.NewTimer(sessionWriteTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return "write_failed", err
		}
		return "", nil
	case <-timer.C:
		return "write_timeout", os.ErrDeadlineExceeded
	}
}
