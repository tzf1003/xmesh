package main

import (
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

func taskConsoleText(raw []byte) string { return taskConsoleCodePage(raw, 1) } // CP_OEMCP

func taskConsoleCodePage(raw []byte, codePage uint32) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	const rejectInvalid = 0x8 // MB_ERR_INVALID_CHARS
	n, err := windows.MultiByteToWideChar(codePage, rejectInvalid, &raw[0], int32(len(raw)), nil, 0)
	if err != nil || n == 0 {
		return string(raw) // Preserve invalid input so the XML parser reports it.
	}
	wide := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(codePage, rejectInvalid, &raw[0], int32(len(raw)), &wide[0], n); err != nil {
		return string(raw)
	}
	return string(utf16.Decode(wide))
}
