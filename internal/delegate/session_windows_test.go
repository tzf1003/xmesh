package delegate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// deliver opens the endpoint that a session record names. An ordinary file must never be opened this way: writing to it
// would overwrite its beginning (CTR-DLG-04).
// conformance: CTR-DLG-04
func Test_CTRDLG04_DeliverRefusesAFileThatIsNotAPipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keep.txt")
	before := []byte("an ordinary file, not a named pipe")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	reason, err := deliver(path, []byte("injected"))
	if err == nil || reason != "connect_failed" {
		t.Fatalf("deliver returned %q, %v; want connect_failed", reason, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("the ordinary file was overwritten: %q", after)
	}
}

// conformance: CTR-DLG-04
func Test_CTRDLG04_PipePathsAreInThePipeNamespace(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{`\\.\pipe\claude-1234`, true},
		{`\\.\PIPE\claude-1234`, true},
		{`//./pipe/claude-1234`, true},
		{`C:\Users\me\session.json`, false},
		{`\\server\share\pipe`, false},
		{`pipe\claude-1234`, false},
	}
	for _, c := range cases {
		if got := pipePath(c.path); got != c.want {
			t.Errorf("pipePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
