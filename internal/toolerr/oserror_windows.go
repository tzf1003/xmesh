//go:build windows

package toolerr

import (
	"errors"
	"syscall"
)

// osFields reports the Win32 error code behind an internal failure, so a caller
// can tell "access denied" from "file not found" without the node log.
func osFields(err error) []any {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return nil
	}
	return []any{"win32_error", int(errno), "reason", errno.Error()}
}
