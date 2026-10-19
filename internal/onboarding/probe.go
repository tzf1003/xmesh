package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/control"
)

type tokenTransport struct{ token string }

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(copy)
}

func openProbe(ctx context.Context, p *Plan) (*mcp.ClientSession, error) {
	u, e := url.Parse(p.Endpoint)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil {
		return nil, fmt.Errorf("invalid local probe address")
	}
	raw, e := os.ReadFile(filepath.Join(filepath.Dir(p.ConfigPath), "pats", "onboarding.token"))
	if e != nil {
		return nil, e
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "xmesh-setup-probe", Version: "1"}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: p.Endpoint, HTTPClient: &http.Client{Transport: tokenTransport{strings.TrimSpace(string(raw))}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil)
}

// Probe verifies MCP initialise, tools/list and a real read-only tool call.
// It deliberately never calls the client's verification challenge.
func Probe(ctx context.Context, p *Plan) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	s, e := openProbe(ctx, p)
	if e != nil {
		return e
	}
	defer s.Close()
	tools, e := s.ListTools(ctx, &mcp.ListToolsParams{})
	if e != nil || len(tools.Tools) == 0 {
		return fmt.Errorf("MCP tools unavailable")
	}
	r, e := s.CallTool(ctx, &mcp.CallToolParams{Name: "local/sys.info", Arguments: map[string]any{}})
	if e != nil {
		return e
	}
	if !validSystemInfo(r, p.Input.NodeName) {
		return fmt.Errorf("MCP tool failed")
	}
	return nil
}

// ClientEvidence requires a successful nonce-path call from outside the health
// probe. Cloud calls additionally require an authenticated OAuth client. A
// multi-device setup also checks an actual remote device tool through Gateway.
func ClientEvidence(ctx context.Context, p *Plan, j *Job) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ep, e := control.ReadEndpoint(filepath.Dir(p.ConfigPath))
	if e != nil {
		return false, e
	}
	client := control.NewClient(ep)
	found, e := findClientEvidence(ctx, p, j, func(path string, out *v1.AuditResponse) error { return client.Fetch(ctx, path, out) })
	if e != nil {
		return false, e
	}
	if !found {
		return false, nil
	}
	for _, receipt := range j.Receipts {
		if e = VerifyClient(receipt, p.Endpoint); e != nil {
			return false, e
		}
	}
	if p.Input.Purpose == "multi-device" {
		var peers v1.PeersResponse
		if e = client.Fetch(ctx, "/v1/peers", &peers); e != nil {
			return false, e
		}
		s, e := openProbe(ctx, p)
		if e != nil {
			return false, e
		}
		defer s.Close()
		for _, peer := range peers.Peers {
			if peer.Self || !peer.Online {
				continue
			}
			result, err := s.CallTool(ctx, &mcp.CallToolParams{Name: peer.Name + "/sys.info", Arguments: map[string]any{}})
			if err == nil && validSystemInfo(result, peer.Name) {
				return true, nil
			}
		}
		return false, nil
	}
	return true, nil
}

func validSystemInfo(result *mcp.CallToolResult, name string) bool {
	if result == nil || result.IsError {
		return false
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return false
	}
	var info struct {
		Hostname string `json:"hostname"`
	}
	return json.Unmarshal(encoded, &info) == nil && info.Hostname == name
}

func findClientEvidence(ctx context.Context, p *Plan, j *Job, fetch func(string, *v1.AuditResponse) error) (bool, error) {
	since := j.CreatedAt.Truncate(time.Millisecond)
	until := time.Now().UTC()
	q := url.Values{"since": {since.Format(time.RFC3339Nano)}, "until": {until.Format(time.RFC3339Nano)}, "limit": {"1000"}, "tool": {j.VerificationTool}, "device": {p.Input.NodeName}, "status": {"ok"}, "event": {"tool_call"}}
	seen := map[string]bool{}
	for range 8 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var out v1.AuditResponse
		if err := fetch("/v1/audit?"+q.Encode(), &out); err != nil {
			return false, err
		}
		if !out.Enabled {
			return false, nil
		}
		for _, raw := range out.Events {
			var event struct {
				Time        string `json:"time"`
				Event       string `json:"event"`
				Status      string `json:"status"`
				Tool        string `json:"tool"`
				Origin      string `json:"origin"`
				OAuthClient string `json:"oauth_client"`
				Device      string `json:"device"`
				Digest      struct {
					Path string `json:"resolved_path"`
				} `json:"args_digest"`
			}
			if json.Unmarshal(raw, &event) != nil {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, event.Time)
			if err != nil || at.Before(since) || at.After(until) || event.Event != "tool_call" || event.Status != "ok" || event.Origin == "panel" || event.Device != p.Input.NodeName || event.Tool != j.VerificationTool || filepath.Clean(event.Digest.Path) != filepath.Clean(j.VerificationPath) {
				continue
			}
			if p.Input.Purpose == "cloud" && event.OAuthClient == "" {
				continue
			}
			return true, nil
		}
		if out.NextCursor == nil || *out.NextCursor == "" || seen[*out.NextCursor] {
			return false, nil
		}
		seen[*out.NextCursor] = true
		q.Set("cursor", *out.NextCursor)
	}
	return false, nil
}
