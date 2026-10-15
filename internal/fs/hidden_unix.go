//go:build !windows

package fs

import (
	"io/fs"
	"strings"
)

func hidden(_ string, d fs.DirEntry) bool { return strings.HasPrefix(d.Name(), ".") }
