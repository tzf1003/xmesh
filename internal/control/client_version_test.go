package control

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestClientVersionMismatchIsRecognizable(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Xmesh-Control-Version", "1.8")
		w.WriteHeader(404)
	}))
	defer s.Close()
	client := &Client{HTTP: &http.Client{Transport: redirectTestTransport{target: s.URL}}, node: "legacy"}
	if _, err := client.Do(t.Context(), "GET", "/v2/status", nil, nil); !errors.Is(err, ErrVersionMismatch) {
		t.Fatal("version mismatch cannot be distinguished from offline", err)
	}
}

type redirectTestTransport struct{ target string }

func (t redirectTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	url, err := url.Parse(t.target + r.URL.Path)
	if err != nil {
		return nil, err
	}
	req.URL = url
	return http.DefaultTransport.RoundTrip(req)
}
