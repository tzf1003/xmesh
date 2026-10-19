package control

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type deadlineTransport struct{ budget time.Duration }

func (d *deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	deadline, _ := r.Context().Deadline()
	d.budget = time.Until(deadline)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
}
func TestClientTimeoutCanFollowWholeFanoutContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for _, timeout := range []time.Duration{0, 30 * time.Second, 45 * time.Second} {
		transport := &deadlineTransport{}
		client := &Client{HTTP: &http.Client{Transport: transport}, RequestTimeout: timeout}
		if _, err := client.Do(ctx, "POST", "/v2/drain", nil, nil); err != nil {
			t.Fatal(err)
		}
		expected := timeout
		if expected == 0 {
			expected = time.Minute
		}
		if transport.budget > expected || transport.budget < expected-time.Second {
			t.Fatal(timeout, transport.budget)
		}
	}
}
