// Package winjobtest builds Windows fixtures for tests of the winjob users.
package winjobtest

import (
	"encoding/binary"
	"os"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const reparseTagAppExecLink = 0x8000001B

// AppExecLink creates a file at path that carries the reparse tag of a Store
// application execution alias, like the pwsh.exe under WindowsApps. The link
// cannot be launched; it only has to be recognised. The test is skipped where
// the file system or account refuses to set the reparse point.
func AppExecLink(t testing.TB, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Skip("cannot open the fixture for a reparse point:", err)
	}
	defer windows.CloseHandle(handle)
	// Version 3, then package family name, app user model id and target path.
	var data []byte
	data = binary.LittleEndian.AppendUint32(data, 3)
	for _, text := range []string{"Fixture_8wekyb3d8bbwe", "Fixture_8wekyb3d8bbwe!App", `C:\Fixture\pwsh.exe`} {
		for _, unit := range utf16.Encode([]rune(text + "\x00")) {
			data = binary.LittleEndian.AppendUint16(data, unit)
		}
	}
	buffer := binary.LittleEndian.AppendUint32(nil, reparseTagAppExecLink)
	buffer = binary.LittleEndian.AppendUint16(buffer, uint16(len(data)))
	buffer = binary.LittleEndian.AppendUint16(buffer, 0)
	buffer = append(buffer, data...)
	var returned uint32
	if err := windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil); err != nil {
		t.Skip("cannot set an application execution alias reparse point:", err)
	}
}
