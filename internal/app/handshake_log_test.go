package app

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func Test_ThrottledHandshakes(t *testing.T) {
	var out bytes.Buffer
	clock := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	w := &throttledHandshakes{out: &out, every: time.Minute, now: func() time.Time { return clock }}
	write := func(line string) {
		t.Helper()
		if n, err := w.Write([]byte(line + "\n")); err != nil || n != len(line)+1 {
			t.Fatalf("Write(%q) = %d, %v", line, n, err)
		}
	}
	const handshake = "http: TLS handshake error from 192.168.31.30:52341: 对端尚未配对"
	write(handshake)
	// Failures within the minute are counted, not written.
	for range 3 {
		write(handshake)
	}
	write("xmesh: 其他日志照常输出")
	if got := strings.Count(out.String(), "TLS handshake error"); got != 1 {
		t.Fatalf("within the minute: %d handshake lines written, want 1", got)
	}
	clock = clock.Add(time.Minute)
	write(handshake)
	text := out.String()
	if got := strings.Count(text, "TLS handshake error"); got != 2 {
		t.Fatalf("after the minute: %d handshake lines written, want 2", got)
	}
	if !strings.Contains(text, "其间另有 3 次 TLS 握手失败") {
		t.Fatalf("summary of the skipped failures missing:\n%s", text)
	}
	if !strings.Contains(text, "xmesh: 其他日志照常输出") {
		t.Fatalf("other lines must pass through:\n%s", text)
	}
}
