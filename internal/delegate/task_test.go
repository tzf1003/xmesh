package delegate

import (
	"strings"
	"testing"
	"unsafe"
)

// The text of a cut event is a copy: it must not keep the longer line it came from alive in memory (CTR-DLG-03).
// conformance: CTR-DLG-03
func Test_CTRDLG03_ClippedTextIsACopyOfTheLine(t *testing.T) {
	line := strings.Repeat("x", maxEventText+1)
	got, cut := clip(line)
	if !cut || len(got) != maxEventText {
		t.Fatalf("clip kept %d bytes, cut %v; want %d bytes and cut", len(got), cut, maxEventText)
	}
	if unsafe.StringData(got) == unsafe.StringData(line) {
		t.Fatal("clip returned a view of the longer line, which keeps all of it alive")
	}
}
