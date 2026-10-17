package app

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// handshakeLogEvery is the least time between two failed TLS handshakes that are written out.
const handshakeLogEvery = time.Minute

// newHandshakeLog is the error log of the HTTP servers. The standard library logs every failed TLS
// handshake, and a device that is not paired with this node retries every few seconds, so its
// failures bury the rest of the log. Here at most one such line is written per handshakeLogEvery;
// the failures in between are counted and reported with the next line.
func newHandshakeLog() *log.Logger {
	return log.New(&throttledHandshakes{out: os.Stderr, every: handshakeLogEvery, now: time.Now}, "", log.LstdFlags)
}

// throttledHandshakes passes every line through except failed TLS handshakes, which it throttles.
type throttledHandshakes struct {
	out   io.Writer
	every time.Duration
	now   func() time.Time

	mu      sync.Mutex
	last    time.Time
	skipped int
}

func (t *throttledHandshakes) Write(p []byte) (int, error) {
	if !strings.Contains(string(p), "TLS handshake error") {
		return t.out.Write(p)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if !t.last.IsZero() && now.Sub(t.last) < t.every {
		t.skipped++
		return len(p), nil
	}
	t.last = now
	if _, err := t.out.Write(p); err != nil {
		return 0, err
	}
	if t.skipped > 0 {
		fmt.Fprintf(t.out, "%s 其间另有 %d 次 TLS 握手失败未逐条记录\n", now.Format("2006/01/02 15:04:05"), t.skipped)
		t.skipped = 0
	}
	return len(p), nil
}
