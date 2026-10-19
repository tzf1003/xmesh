package fs

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func atomicCreate(source, target string) error {
	err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		// A hard link also publishes the complete staged file exclusively on
		// older kernels/filesystems. The caller removes the staging name.
		return os.Link(source, target)
	}
	return err
}
