//go:build windows

package fs

import (
	"golang.org/x/sys/windows"
	"io/fs"
	"strings"
)

func hidden(path string, d fs.DirEntry) bool {
	if strings.HasPrefix(d.Name(), ".") {
		return true
	}
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return true
	}
	a, e := windows.GetFileAttributes(p)
	return e == nil && a&windows.FILE_ATTRIBUTE_HIDDEN != 0
}
