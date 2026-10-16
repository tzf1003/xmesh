package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// Client talks to one node's control channel. The URL host is a placeholder;
// every connection goes to the endpoint's socket or pipe.
type Client struct {
	HTTP *http.Client
}

func NewClient(e Endpoint) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx, e) },
		// A reused connection would outlive a restarted kernel.
		DisableKeepAlives: true,
	}
	return &Client{HTTP: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Response is a decoded reply. Body holds the raw JSON.
type Response struct {
	Status int
	Header http.Header
	Body   json.RawMessage
}

// Error returns the ControlError body as a *v1.Error when the status is not 2xx.
func (r *Response) Error() error {
	if r.Status/100 == 2 {
		return nil
	}
	var e v1.Error
	if json.Unmarshal(r.Body, &e) == nil && e.Code != "" {
		return &e
	}
	return fmt.Errorf("控制通道返回 HTTP %d", r.Status)
}

// Do sends method path with an optional JSON body and headers.
func (c *Client) Do(ctx context.Context, method, path string, body any, headers map[string]string) (*Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://xmesh-control"+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	return &Response{Status: res.StatusCode, Header: res.Header, Body: raw}, nil
}

// Stream opens a long-lived request (GET /v1/events). The caller closes Body.
func (c *Client) Stream(ctx context.Context, path string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://xmesh-control"+path, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.HTTP.Do(req)
}

// Fetch is Do for the common GET-and-decode case.
func (c *Client) Fetch(ctx context.Context, path string, out any) error {
	r, err := c.Do(ctx, "GET", path, nil, nil)
	if err != nil {
		return err
	}
	if err = r.Error(); err != nil {
		return err
	}
	return json.Unmarshal(r.Body, out)
}
