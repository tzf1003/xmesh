// Package cfadmin creates the Cloudflare resources a device needs to join a mesh: a tunnel, its DNS
// name, and an Access application that lets only the mesh's service token reach it. The API token
// stays on the machine that runs the command; the kernel never holds it.
package cfadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Cloudflare v4 API.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

const maxResponse = 1 << 20

// Client is a minimal Cloudflare v4 API client for one account and zone. The token is never printed.
type Client struct {
	BaseURL   string
	Token     string
	AccountID string
	ZoneID    string
	HTTP      *http.Client
}

func (c *Client) String() string { return "cfadmin.Client{token=<redacted>}" }

// APIError is a failed Cloudflare call. Messages come from Cloudflare and never contain the token.
type APIError struct {
	Status   int
	Messages []string
}

func (e *APIError) Error() string {
	if len(e.Messages) == 0 {
		return fmt.Sprintf("Cloudflare API 返回 HTTP %d", e.Status)
	}
	return fmt.Sprintf("Cloudflare API 返回 HTTP %d：%s", e.Status, strings.Join(e.Messages, "；"))
}

// IsNotFound reports whether err is a 404, which cleanup treats as already done.
func IsNotFound(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}

type envelope struct {
	Success bool            `json:"success"`
	Errors  []envelopeError `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type envelopeError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(base, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 Cloudflare API：%w", sanitize(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return errors.New("Cloudflare API 响应无效")
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode >= 300 {
			return &APIError{Status: resp.StatusCode}
		}
		return errors.New("Cloudflare API 响应无效")
	}
	if resp.StatusCode >= 300 || !env.Success {
		e := &APIError{Status: resp.StatusCode}
		for _, item := range env.Errors {
			e.Messages = append(e.Messages, fmt.Sprintf("%d %s", item.Code, item.Message))
		}
		return e
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// sanitize drops the request URL from transport errors so a token in a misconfigured base URL
// cannot reach logs.
func sanitize(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

func (c *Client) account(path string) string { return "/accounts/" + c.AccountID + path }
func (c *Client) zone(path string) string    { return "/zones/" + c.ZoneID + path }

// Tunnel is a remotely configured Cloudflare Tunnel.
type Tunnel struct {
	ID    string
	Name  string
	Token string
}

// CreateTunnel makes a tunnel managed from the dashboard/API, so its routes live at Cloudflare and
// cloudflared needs only the token.
func (c *Client) CreateTunnel(ctx context.Context, name string) (Tunnel, error) {
	var out struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := c.do(ctx, "POST", c.account("/cfd_tunnel"), map[string]any{"name": name, "config_src": "cloudflare"}, &out); err != nil {
		return Tunnel{}, err
	}
	if out.ID == "" {
		return Tunnel{}, errors.New("Cloudflare 未返回隧道 ID")
	}
	t := Tunnel{ID: out.ID, Name: out.Name, Token: out.Token}
	if t.Token == "" {
		if err := c.do(ctx, "GET", c.account("/cfd_tunnel/"+out.ID+"/token"), nil, &t.Token); err != nil {
			_ = c.DeleteTunnel(ctx, out.ID)
			return Tunnel{}, err
		}
	}
	return t, nil
}

// RouteTunnel forwards hostname to a loopback TCP service; every other request gets a 404.
func (c *Client) RouteTunnel(ctx context.Context, id, hostname, service string) error {
	config := map[string]any{"config": map[string]any{"ingress": []map[string]any{
		{"hostname": hostname, "service": service},
		{"service": "http_status:404"},
	}}}
	return c.do(ctx, "PUT", c.account("/cfd_tunnel/"+id+"/configurations"), config, nil)
}

func (c *Client) DeleteTunnel(ctx context.Context, id string) error {
	// Connections must be closed before Cloudflare allows the delete; an absent connection is fine.
	_ = c.do(ctx, "DELETE", c.account("/cfd_tunnel/"+id+"/connections"), nil, nil)
	return c.do(ctx, "DELETE", c.account("/cfd_tunnel/"+id), nil, nil)
}

// CreateCNAME points hostname at the tunnel through Cloudflare's proxy.
func (c *Client) CreateCNAME(ctx context.Context, hostname, tunnelID string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, "POST", c.zone("/dns_records"), map[string]any{"type": "CNAME", "name": hostname, "content": tunnelID + ".cfargotunnel.com", "proxied": true, "ttl": 1}, &out)
	return out.ID, err
}

func (c *Client) DeleteDNS(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", c.zone("/dns_records/"+id), nil, nil)
}

// CreateAccessApp puts hostname behind Cloudflare Access.
func (c *Client) CreateAccessApp(ctx context.Context, name, hostname string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, "POST", c.account("/access/apps"), map[string]any{"name": name, "domain": hostname, "type": "self_hosted", "session_duration": "24h"}, &out)
	return out.ID, err
}

func (c *Client) DeleteAccessApp(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", c.account("/access/apps/"+id), nil, nil)
}

// AllowServiceToken lets any valid service token of the account through the application. Access is only
// a gate: the inner pinned mTLS decides which devices are trusted. Admitting every token, not one,
// keeps devices that were set up separately, each with its own token, able to reach each other.
func (c *Client) AllowServiceToken(ctx context.Context, appID, name string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, "POST", c.account("/access/apps/"+appID+"/policies"), map[string]any{
		"name": name, "decision": "non_identity",
		"include": []map[string]any{{"any_valid_service_token": map[string]any{}}},
	}, &out)
	return out.ID, err
}

// ServiceToken is a Cloudflare Access service token. The secret is only returned at creation.
type ServiceToken struct {
	ID       string
	ClientID string
	Secret   string
}

func (t ServiceToken) String() string {
	return "ServiceToken{id=" + t.ID + " client_id=" + t.ClientID + " secret=<redacted>}"
}
func (t ServiceToken) GoString() string { return t.String() }

func (c *Client) CreateServiceToken(ctx context.Context, name string, duration string) (ServiceToken, error) {
	var out struct {
		ID           string `json:"id"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := c.do(ctx, "POST", c.account("/access/service_tokens"), map[string]any{"name": name, "duration": duration}, &out); err != nil {
		return ServiceToken{}, err
	}
	if out.ID == "" || out.ClientID == "" || out.ClientSecret == "" {
		return ServiceToken{}, errors.New("Cloudflare 未返回完整的服务令牌")
	}
	return ServiceToken{ID: out.ID, ClientID: out.ClientID, Secret: out.ClientSecret}, nil
}

func (c *Client) DeleteServiceToken(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", c.account("/access/service_tokens/"+id), nil, nil)
}

// ServiceTokenExists reports whether Cloudflare still has the service token.
func (c *Client) ServiceTokenExists(ctx context.Context, id string) (bool, error) {
	err := c.do(ctx, "GET", c.account("/access/service_tokens/"+id), nil, nil)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// Verify checks that the token can use the account's tunnels, so a wrong ID or token fails at login.
// It lists tunnels rather than reading the account, which needs Account Settings permission the
// documented token does not carry.
func (c *Client) Verify(ctx context.Context) error {
	var out []json.RawMessage
	return c.do(ctx, "GET", c.account("/cfd_tunnel?per_page=1"), nil, &out)
}
