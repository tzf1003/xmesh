//go:build !windows

package privdir

import (
	"fmt"
	"os"
)

// Ensure creates path with mode 0700 and tightens an existing directory to it.
func Ensure(path string) error {
	if e := os.MkdirAll(path, 0700); e != nil {
		return e
	}
	return os.Chmod(path, 0700)
}

// GrantRead has no effect on Unix: access to a state file follows the mode of
// its 0700 directory.
func GrantRead(path string, sids ...string) error { return nil }

// Verify reports whether path is a directory only its owner can use.
func Verify(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("%s is not a 0700 directory (mode %v)", path, info.Mode())
	}
	return nil
}
