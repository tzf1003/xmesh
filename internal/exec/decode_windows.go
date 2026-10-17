//go:build windows

package exec

import (
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// codePageOEM is CP_OEMCP: the code page console programs write when their
// output is redirected to a file or pipe.
const codePageOEM = 1

func systemDecode(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", true
	}
	// A page boundary can cut a double-byte character; the cut byte then maps to
	// the code page's default character, as an invalid UTF-8 tail does.
	n, err := windows.MultiByteToWideChar(codePageOEM, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return "", false
	}
	wide := make([]uint16, n)
	if _, err = windows.MultiByteToWideChar(codePageOEM, 0, &b[0], int32(len(b)), &wide[0], n); err != nil {
		return "", false
	}
	return string(utf16.Decode(wide)), true
}
