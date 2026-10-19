package computer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

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
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = p.call(ctx, "execute", strings.Repeat("x", 512<<10), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
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
