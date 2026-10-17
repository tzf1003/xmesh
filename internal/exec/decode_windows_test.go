//go:build windows

package exec

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var wideCharToMultiByte = windows.NewLazySystemDLL("kernel32.dll").NewProc("WideCharToMultiByte")

// The system conversion round-trips text through the OEM code page, which is
// what cmd.exe and Windows PowerShell write when redirected. 文件 is used where
// the code page has it (936 on a Chinese system), else a Latin letter.
func Test_WindowsDecodeOEMRoundTrip(t *testing.T) {
	for _, text := range []string{"文件_中文.txt", "é"} {
		wide, err := windows.UTF16FromString(text)
		if err != nil {
			t.Fatal(err)
		}
		wide = wide[:len(wide)-1]
		var used int32
		n, _, _ := wideCharToMultiByte.Call(codePageOEM, 0, uintptr(unsafe.Pointer(&wide[0])), uintptr(len(wide)), 0, 0, 0, uintptr(unsafe.Pointer(&used)))
		if n == 0 {
			t.Fatal("size query failed")
		}
		raw := make([]byte, n)
		wideCharToMultiByte.Call(codePageOEM, 0, uintptr(unsafe.Pointer(&wide[0])), uintptr(len(wide)), uintptr(unsafe.Pointer(&raw[0])), n, 0, uintptr(unsafe.Pointer(&used)))
		if used != 0 {
			continue // not representable in this code page
		}
		if got := decodeOutput(raw, false); got != text {
			t.Fatalf("%q -> % x -> %q", text, raw, got)
		}
		return
	}
	t.Skip("neither sample text is representable in the OEM code page")
}
