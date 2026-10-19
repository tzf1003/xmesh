package computer

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type observedNativeWriter struct {
	io.WriteCloser
	started    chan struct{}
	finished   chan struct{}
	startOnce  sync.Once
	finishOnce sync.Once
}

func (w *observedNativeWriter) Write(data []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	defer w.finishOnce.Do(func() { close(w.finished) })
	return w.WriteCloser.Write(data)
}

func TestMain(m *testing.M) {
	if os.Getenv("XMESH_TEST_NATIVE_STALLED_READER") == "1" {
		// Run as a native executor that starts successfully but never consumes
		// its input pipe. The parent must recover without waiting for this sleep.
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestProcessWriteCancellationTerminatesStalledExecutor(t *testing.T) {
	t.Setenv("XMESH_TEST_NATIVE_STALLED_READER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := StartProcess(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	writer := &observedNativeWriter{WriteCloser: p.input, started: make(chan struct{}), finished: make(chan struct{})}
	p.input = writer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- p.call(ctx, "execute", strings.Repeat("x", 512<<10), nil) }()
	select {
	case <-writer.started:
	case <-time.After(10 * time.Second):
		t.Fatal("native pipe write did not start")
	}
	// Start the cancellation budget after serialization and writer admission.
	// Windows race instrumentation can spend the original 150ms before writing.
	select {
	case <-writer.finished:
		t.Fatal("fixture write did not stall")
	case <-time.After(150 * time.Millisecond):
	}
	start := time.Now()
	cancel()
	select {
	case err = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled write did not respect cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("stalled write did not respect cancellation", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatal("stalled write exceeded cancellation budget", elapsed)
	}
	select {
	case <-p.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled native executor survived cancellation")
	}
	ctx, cancelNext := context.WithTimeout(context.Background(), time.Second)
	defer cancelNext()
	if _, err := p.Info(ctx); err == nil {
		t.Fatal("terminated executor accepted a new request")
	}
}
