package computer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
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
	if mode := os.Getenv("XMESH_TEST_NATIVE_CAPTURE_TIMEOUT"); mode != "" {
		serveCaptureTimeoutFixture(mode)
		os.Exit(0)
	}
	if os.Getenv("XMESH_TEST_NATIVE_STALLED_READER") == "1" {
		// Run as a native executor that starts successfully but never consumes
		// its input pipe. The parent must recover without waiting for this sleep.
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// This test process speaks only metadata over inherited pipes and exits on EOF.
func serveCaptureTimeoutFixture(mode string) {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var output sync.Mutex
	send := func(value any) { output.Lock(); defer output.Unlock(); _ = encoder.Encode(value) }
	timeout := func(id uint64) {
		send(map[string]any{"id": id, "error": map[string]any{"error": "desktop_unavailable", "message": "fixture capture timeout", "reason": "capture_timeout"}})
	}
	var pending uint64
	var revoked uint64
	firstExecution := true
	for scanner.Scan() {
		var request struct {
			ID     uint64    `json:"id"`
			Method string    `json:"method"`
			Params Execution `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return
		}
		switch request.Method {
		case "info":
			send(map[string]any{"id": request.ID, "result": Info{ProtocolVersion: 1, Epoch: fmt.Sprint(os.Getpid()), State: "ready", Available: true, DesktopID: "fixture", LayoutVersion: "fixture", Capabilities: []string{"screenshot", "pointer", "keyboard", "text"}}})
			if pending != 0 {
				id := pending
				pending = 0
				time.AfterFunc(100*time.Millisecond, func() { timeout(id) })
			}
		case "capture":
			if mode == "after-info" {
				pending = request.ID
			} else {
				timeout(request.ID)
			}
		case "execute":
			if mode != "execution-timeout" {
				continue
			}
			token, _ := strconv.ParseUint(request.Params.Token, 10, 64)
			if firstExecution {
				firstExecution = false
				continue
			}
			if token <= revoked {
				send(map[string]any{"id": request.ID, "error": map[string]string{"error": "lease_required", "message": "revoked token"}})
			} else {
				send(map[string]any{"id": request.ID, "result": ActionResult{State: "executed", Executed: len(request.Params.Actions)}})
			}
		case "release":
			token, _ := strconv.ParseUint(request.Params.Token, 10, 64)
			if token > revoked {
				revoked = token
			}
			send(map[string]any{"id": request.ID, "result": map[string]bool{"released": true}})
		}
	}
}

func TestNativeExecutionDeadlineRetiresManagerLeaseAndAllowsFreshController(t *testing.T) {
	t.Setenv("XMESH_TEST_NATIVE_CAPTURE_TIMEOUT", "execution-timeout")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := StartProcess(executable)
	if err != nil {
		t.Fatal(err)
	}
	m := New(p, Settings{Enabled: true})
	t.Cleanup(func() { _ = m.Close() })
	s := open(t, m, "old-owner", "control")
	out, err := m.Act(context.Background(), "old-owner", ActRequest{SessionRequest: request(s), ActionID: "timeout", Actions: []Action{{Type: "wait", DurationMS: 1}}})
	if err != nil || out.State != "unknown" || p.Exited() {
		t.Fatal("expected unknown execution on a live native process", out, err)
	}
	if len(m.Sessions("old-owner")) != 0 {
		t.Fatal("native error cleanup retained manager controller")
	}
	next := open(t, m, "new-owner", "control")
	if next.Token == s.Token {
		t.Fatal("fenced token reused")
	}
	out, err = m.Act(context.Background(), "new-owner", ActRequest{SessionRequest: request(next), ActionID: "fresh", Actions: []Action{{Type: "wait", DurationMS: 1}}})
	if err != nil || out.State != "executed" || out.Executed != 1 {
		t.Fatal("fresh authority remained poisoned", out, err)
	}
}

func TestCaptureTimeoutRetiresNativeProcessIncludingCancelledCallReplies(t *testing.T) {
	for _, mode := range []string{"immediate", "after-info"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XMESH_TEST_NATIVE_CAPTURE_TIMEOUT", mode)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			p, err := StartProcess(executable)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			before, err := p.Info(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "immediate" {
				_, err = p.Capture(ctx, CaptureRequest{})
				if err == nil {
					t.Fatal("capture timeout was accepted")
				}
			} else {
				writer := &observedNativeWriter{WriteCloser: p.input, started: make(chan struct{}), finished: make(chan struct{})}
				p.input = writer
				callContext, cancelCall := context.WithCancel(ctx)
				defer cancelCall()
				result := make(chan error, 1)
				go func() { _, e := p.Capture(callContext, CaptureRequest{}); result <- e }()
				select {
				case <-writer.finished:
				case <-ctx.Done():
					t.Fatal("capture request was not written")
				}
				// Cancel during response waiting, after the pipe write was admitted.
				time.Sleep(20 * time.Millisecond)
				cancelCall()
				if e := <-result; !errors.Is(e, context.Canceled) {
					t.Fatal(e)
				}
				if _, e := p.Info(ctx); e != nil {
					t.Fatal("cancelled caller retired the child before the late timeout", e)
				}
			}
			select {
			case <-p.exited:
			case <-ctx.Done():
				t.Fatal("timed-out capture left the native child alive")
			}
			next, err := StartProcess(executable)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = next.Close() })
			after, err := next.Info(ctx)
			if err != nil || after.Epoch == before.Epoch {
				t.Fatal("replacement reused desktop authority", before, after, err)
			}
		})
	}
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
