package delegate

import (
	"os"
	"strconv"
	"testing"
)

// A pid outside the native range must not stand for a live process: the kernel keeps only its low bits, which may
// name one (CTR-DLG-04).
// conformance: CTR-DLG-04
func Test_CTRDLG04_ProcessAliveRejectsPIDsOutsideTheNativeRange(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("int cannot hold a pid above the 32-bit range")
	}
	self := os.Getpid()
	if !processAlive(self) {
		t.Fatal("the test process is not reported as alive")
	}
	wrapped := int(int64(self) + int64(1)<<32)
	if processAlive(wrapped) {
		t.Fatalf("pid %d is reported as alive, but it lies outside the native pid range", wrapped)
	}
}
