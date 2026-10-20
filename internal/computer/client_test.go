package computer

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type desktopRoundTripper func(*http.Request) (*http.Response, error)

func (f desktopRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHostStatusRetriesClosedKeepaliveWithoutClaimingStopped(t *testing.T) {
	client := NewClient("")
	defer client.Close()
	calls := 0
	client.http.Transport = desktopRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, io.EOF
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"running":true,"pid":123,"version":"v1","native_pid":124}`))}, nil
	})
	status, err := client.HostStatus(context.Background())
	if err != nil || !status.Running || status.PID != 123 || calls != 2 {
		t.Fatal(status, calls, err)
	}
}

func TestHostStatusDoesNotTreatPersistentEOFAsAbsence(t *testing.T) {
	client := NewClient("")
	defer client.Close()
	calls := 0
	client.http.Transport = desktopRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, io.EOF })
	if _, err := client.HostStatus(context.Background()); err == nil || calls != 2 {
		t.Fatal("unconfirmed endpoint reported stopped", calls, err)
	}
}
