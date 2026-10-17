package exec

import (
	"strings"
	"unicode/utf8"
)

// oemDecode converts bytes written in the system's console code page to UTF-8.
// It reports false where no such conversion exists (every non-Windows host).
var oemDecode = systemDecode

// decodeOutput turns captured command output into text. Valid UTF-8 passes
// through, with invalid bytes replaced as before. On Windows, a console program
// on a non-UTF-8 code page (cmd.exe, Windows PowerShell 5.1 and many tools on a
// Chinese system write GBK) produces bytes that are not UTF-8 at all: those are
// converted from the OEM code page instead of becoming replacement characters.
// The raw bytes stay in the output file behind output_ref.
//
// continued says b starts in the middle of a stream (a later output page), so
// its first bytes may be the tail of a UTF-8 character cut by the page start.
// For the start of a stream they cannot be, and a legacy double-byte character
// must not be mistaken for one.
func decodeOutput(b []byte, continued bool) string {
	if utf8.Valid(trimPartialRune(b, continued)) {
		return strings.ToValidUTF8(string(b), "�")
	}
	if text, ok := oemDecode(b); ok {
		return text
	}
	return strings.ToValidUTF8(string(b), "�")
}

// trimPartialRune drops a UTF-8 sequence that a page boundary cut in two at
// either end, so that a slice of valid UTF-8 is still recognised as such.
func trimPartialRune(b []byte, continued bool) []byte {
	for cut := 0; continued && cut < utf8.UTFMax-1 && len(b) > 0 && b[0]&0xC0 == 0x80; cut++ {
		b = b[1:]
	}
	for n := 1; n < utf8.UTFMax && n <= len(b); n++ {
		c := b[len(b)-n]
		if c&0xC0 == 0x80 {
			continue
		}
		if c >= 0xC0 && !utf8.FullRune(b[len(b)-n:]) {
			return b[:len(b)-n]
		}
		break
	}
	return b
}
