package computer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

// Client connects only to the authenticated local desktop broker. It never dials TCP.
type Client struct {
	endpoint  string
	owner     string
	http      *http.Client
	transport *http.Transport
	once      sync.Once
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func NewClient(endpoint string) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint()
	}
	c := &Client{endpoint: endpoint, owner: rand.Text(), stop: make(chan struct{}), done: make(chan struct{})}
	c.transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dialDesktop(ctx, endpoint) }, MaxConnsPerHost: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	c.http = &http.Client{Transport: c.transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c
}

func DefaultStateDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "xmesh", "computer")
}

func (c *Client) call(ctx context.Context, method string, in, out any) error {
	err := c.request(ctx, method, in, out)
	var network *url.Error
	if errors.As(err, &network) {
		return toolerr.New("desktop_unavailable", "用户桌面执行器未连接", "reason", "executor_disconnected")
	}
	return err
}

func (c *Client) request(ctx context.Context, method string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://desktop/v1/"+method, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Xmesh-Computer-Owner", c.owner)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(b) > 8<<20 {
		return toolerr.New("desktop_unavailable", "执行器响应无效", "reason", "response_size")
	}
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Code    string `json:"error"`
			Message string `json:"message"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal(b, &body) != nil || body.Code == "" {
			return toolerr.New("desktop_unavailable", "执行器请求失败", "reason", "invalid_response")
		}
		return toolerr.New(body.Code, body.Message, "reason", body.Reason)
	}
	if out == nil {
		return nil
	}
	if err = json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("desktop response: %w", err)
	}
	return nil
}

// HostStatus is a local lifecycle probe. It does not bind the desktop, start a
// native process or acquire the kernel's heartbeat ownership.
type HostStatus struct {
	Running   bool   `json:"running"`
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	NativePID int    `json:"native_pid"`
}

func (c *Client) HostStatus(ctx context.Context) (HostStatus, error) {
	var status HostStatus
	err := c.request(ctx, "status", nil, &status)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return HostStatus{}, nil
	}
	return status, err
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	err := c.call(ctx, "info", map[string]int{"protocol_version": ProtocolVersion}, &out)
	if err == nil {
		c.once.Do(func() { go c.heartbeat() })
	}
	return out, err
}
func (c *Client) heartbeat() {
	defer close(c.done)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = c.call(ctx, "heartbeat", nil, nil)
			cancel()
		}
	}
}
func (c *Client) Capture(ctx context.Context, r CaptureRequest) (Frame, error) {
	var out struct {
		Frame
		Image []byte `json:"image"`
	}
	err := c.call(ctx, "capture", r, &out)
	out.Frame.Image = out.Image
	return out.Frame, err
}
func (c *Client) Execute(ctx context.Context, r Execution) (ActionResult, error) {
	var out ActionResult
	err := c.call(ctx, "execute", r, &out)
	return out, err
}
func (c *Client) Release(ctx context.Context, r Execution) error {
	return c.call(ctx, "release", r, nil)
}

// Authorize is a local setup operation and is never registered as an MCP tool.
func (c *Client) Authorize(ctx context.Context) error { return c.call(ctx, "authorize", nil, nil) }
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.once.Do(func() { close(c.done) })
		close(c.stop)
		<-c.done
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.call(ctx, "disconnect", nil, nil)
		c.transport.CloseIdleConnections()
	})
	return nil
}
