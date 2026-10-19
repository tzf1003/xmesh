//go:build !darwin && !linux && !windows

package fs

import "os"

func atomicCreate(source, target string) error { return os.Link(source, target) }
