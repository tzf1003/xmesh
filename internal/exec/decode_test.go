package exec

import (
	"strings"
	"testing"
)

func Test_DecodeOutputKeepsValidUTF8(t *testing.T) {
	for _, in := range []string{"", "plain", "文件_中文.txt\r\n", "tail é"} {
		if got := decodeOutput([]byte(in)); got != in {
			t.Fatalf("%q -> %q", in, got)
		}
	}
	// invalid bytes are replaced when no code page conversion applies
	t.Cleanup(func(old func([]byte) (string, bool)) func() { return func() { oemDecode = old } }(oemDecode))
	oemDecode = func([]byte) (string, bool) { return "", false }
	if got := decodeOutput([]byte{'a', 0xff, 'b'}); got != "a�b" {
		t.Fatalf("%q", got)
	}
}

// A page cut through a UTF-8 character is still UTF-8, not a legacy code page.
func Test_DecodeOutputPageBoundaryInsideUTF8(t *testing.T) {
	t.Cleanup(func(old func([]byte) (string, bool)) func() { return func() { oemDecode = old } }(oemDecode))
	oemDecode = func([]byte) (string, bool) { t.Fatal("converted valid UTF-8 as a code page"); return "", false }
	whole := []byte(strings.Repeat("中文", 5))
	for cut := 1; cut < len(whole); cut++ {
		decodeOutput(whole[:cut])
		decodeOutput(whole[cut:])
	}
}

// Bytes that are not UTF-8 go through the system code page conversion.
func Test_DecodeOutputUsesCodePageForLegacyBytes(t *testing.T) {
	t.Cleanup(func(old func([]byte) (string, bool)) func() { return func() { oemDecode = old } }(oemDecode))
	oemDecode = func(b []byte) (string, bool) { return "converted:" + string(b), true }
	if got := decodeOutput([]byte{0xce, 0xc4, 0xbc, 0xfe}); got != "converted:\xce\xc4\xbc\xfe" {
		t.Fatalf("%q", got)
	}
}
