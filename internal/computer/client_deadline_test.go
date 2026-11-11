package computer

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A caller's deadline is not a lost executor. Manager.Info relies on this to keep
// the binding when a background sweep runs out of time, so the client must
// surface the deadline itself rather than mapping it to executor_disconnected.
func TestClientReportsCallerDeadlineAsDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	defer close(release)

	addr := server.Listener.Addr().String()
	c := NewClient("")
	c.http = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}}}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var out Info
	err := c.call(ctx, "info", map[string]int{"protocol_version": ProtocolVersion}, &out)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline surfaced as %v; want context.DeadlineExceeded", err)
	}
}
