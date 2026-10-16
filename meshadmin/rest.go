package meshadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 8 << 20

// rest is the shared JSON-over-HTTP helper. It refuses to send credentials over
// plain HTTP to a non-loopback host and never follows redirects, so a bearer
// token cannot be forwarded to another origin.
type rest struct {
	base *url.URL
	hc   *http.Client
}

func newRest(rawURL string, hc *http.Client) (*rest, error) {
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: control plane URL %q", ErrInvalid, rawURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: control plane URL must not carry userinfo, query or fragment", ErrInvalid)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("%w: refusing to send credentials over plain HTTP to %s", ErrInvalid, u.Hostname())
		}
	default:
		return nil, fmt.Errorf("%w: control plane URL scheme %q", ErrInvalid, u.Scheme)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &rest{base: u, hc: &c}, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type call struct {
	method  string
	path    string // joined onto the base URL
	query   url.Values
	header  map[string]string
	body    []byte
	op      string // human label used in errors
	scope   string // OAuth scope hint for 401/403
	okCodes []int  // defaults to 2xx
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (r *rest) do(ctx context.Context, c call) (reply, error) {
	u := *r.base
	u.Path = strings.TrimRight(u.Path, "/") + c.path
	u.RawPath = ""
	if c.query != nil {
		u.RawQuery = c.query.Encode()
	}
	var body io.Reader
	if c.body != nil {
		body = bytes.NewReader(c.body)
	}
	req, err := http.NewRequestWithContext(ctx, c.method, u.String(), body)
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	if c.body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		// url.Error embeds the URL, which never contains secrets here.
		return reply{}, fmt.Errorf("meshadmin: %s: %w", c.op, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return reply{}, fmt.Errorf("meshadmin: %s: read response: %w", c.op, err)
	}
	if len(b) > maxResponseBytes {
		return reply{}, fmt.Errorf("meshadmin: %s: response exceeds %d bytes", c.op, maxResponseBytes)
	}
	rp := reply{status: resp.StatusCode, header: resp.Header, body: b}
	if c.accepts(resp.StatusCode) {
		return rp, nil
	}
	return rp, classify(c, rp)
}

func (c call) accepts(status int) bool {
	if len(c.okCodes) == 0 {
		return status >= 200 && status < 300
	}
	for _, s := range c.okCodes {
		if s == status {
			return true
		}
	}
	return false
}

// classify maps a failed response to a sentinel-wrapping error.
func classify(c call, r reply) error {
	msg := errorMessage(r.body)
	switch r.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &ScopeError{Op: c.op, Scope: c.scope, Status: r.status}
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s: %s", ErrNotFound, c.op, msg)
	case http.StatusPreconditionFailed, http.StatusConflict:
		return fmt.Errorf("%w: %s", ErrConflict, c.op)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return &InvalidError{Op: c.op, Message: msg}
	}
	return &APIError{Op: c.op, Status: r.status, Message: msg}
}

// errorMessage extracts a short message from the common error body shapes
// ({"message": ...}, {"error": ...}) and falls back to a trimmed body.
func errorMessage(b []byte) string {
	var m struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(b, &m) == nil {
		if m.Message != "" {
			return m.Message
		}
		if m.Error != "" {
			return m.Error
		}
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
