//go:build !windows

package fs

import "os"

func openRead(path string) (*os.File, error) { return os.Open(path) }
