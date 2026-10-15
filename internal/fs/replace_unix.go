//go:build !windows

package fs

import "os"

func atomicReplace(source, target string) error { return os.Rename(source, target) }
