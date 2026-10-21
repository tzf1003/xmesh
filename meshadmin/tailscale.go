package meshadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2/clientcredentials"
)

// DefaultTailscaleURL is the official API origin.
const DefaultTailscaleURL = "https://api.tailscale.com"

// Scopes requested by OAuth credentials. A "read" credential never asks for a
// write scope, so the control plane itself refuses every mutation.
var (
	TailscaleReadScopes  = []string{"devices:core:read", "policy_file:read", "auth_keys:read"}
	TailscaleWriteScopes = []string{"devices:core", "policy_file", "auth_keys"}
)

// TailscaleConfig configures the official-control-plane Provider. Set either
// Token (API access token) or ClientID+ClientSecret (OAuth client).
type TailscaleConfig struct {
	BaseURL      string // default DefaultTailscaleURL
	Tailnet      string // default "-" (the credential's own tailnet)
	Token        string
	ClientID     string
	ClientSecret string
	Scopes       []string // OAuth only; default TailscaleReadScopes
	HTTPClient   *http.Client
}

type tailscale struct {
	r       *rest
	tailnet string
	token   string
}

// NewTailscale returns the Provider for the official Tailscale API v2.
func NewTailscale(cfg TailscaleConfig) (Provider, error) {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultTailscaleURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	t := &tailscale{tailnet: cfg.Tailnet}
	if t.tailnet == "" {
		t.tailnet = "-"
	}
	switch {
	case cfg.Token != "" && cfg.ClientID != "":
		return nil, fmt.Errorf("%w: set an API token or an OAuth client, not both", ErrInvalid)
	case cfg.Token != "":
		t.token = cfg.Token
	case cfg.ClientID != "" && cfg.ClientSecret != "":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = TailscaleReadScopes
		}
		cc := clientcredentials.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Scopes:       scopes,
			TokenURL:     strings.TrimRight(base, "/") + "/api/v2/oauth/token",
		}
		// The oauth2 transport refreshes tokens against the same origin; the
		// context carries our client so tests and proxies apply to it too.
		ctx := context.WithValue(context.Background(), oauthHTTPClientKey(), hc)
		hc = cc.Client(ctx)
		hc.Timeout = 30 * time.Second
	default:
		return nil, fmt.Errorf("%w: Tailscale needs an API token or an OAuth client id and secret", ErrInvalid)
	}
	r, err := newRest(base, hc)
	if err != nil {
		return nil, err
	}
	t.r = r
	return t, nil
}

func (t *tailscale) Kind() Kind { return KindTailscale }

func (t *tailscale) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{PolicyWrite: true, AtomicPolicyWrite: true, DERPViaPolicy: true}, nil
}

func (t *tailscale) tailnetPath(elem ...string) string {
	p := "/api/v2/tailnet/" + url.PathEscape(t.tailnet)
	for _, e := range elem {
		p += "/" + e
	}
	return p
}

func (t *tailscale) header(extra map[string]string) map[string]string {
	h := map[string]string{}
	if t.token != "" {
		h["Authorization"] = "Bearer " + t.token
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

type tsDevice struct {
	ID                string   `json:"id"`
	NodeID            string   `json:"nodeId"`
	Name              string   `json:"name"`
	Hostname          string   `json:"hostname"`
	Addresses         []string `json:"addresses"`
	Tags              []string `json:"tags"`
	User              string   `json:"user"`
	OS                string   `json:"os"`
	ClientVersion     string   `json:"clientVersion"`
	NodeKey           string   `json:"nodeKey"`
	Authorized        bool     `json:"authorized"`
	KeyExpiryDisabled bool     `json:"keyExpiryDisabled"`
	Connected         *bool    `json:"connectedToControl"`
	LastSeen          string   `json:"lastSeen"`
	Expires           string   `json:"expires"`
}

func (d tsDevice) device() Device {
	out := Device{
		ID: d.ID, NodeKey: d.NodeKey, Name: d.Name, Hostname: d.Hostname,
		Addresses: d.Addresses, Tags: nonNil(d.Tags), User: d.User, OS: d.OS, Version: d.ClientVersion,
		Authorized: d.Authorized, LastSeen: parseTime(d.LastSeen),
	}
	if out.ID == "" {
		out.ID = d.NodeID
	}
	if d.Connected != nil {
		out.Online = *d.Connected
	}
	if !d.KeyExpiryDisabled {
		out.Expires = parseTime(d.Expires)
	}
	return out
}

func (t *tailscale) Devices(ctx context.Context) ([]Device, error) {
	rp, err := t.r.do(ctx, call{
		method: http.MethodGet, path: t.tailnetPath("devices"), query: url.Values{"fields": {"all"}},
		header: t.header(nil), op: "list devices", scope: "devices:core:read",
	})
	if err != nil {
		return nil, err
	}
	var body struct {
		Devices []tsDevice `json:"devices"`
	}
	if err := json.Unmarshal(rp.body, &body); err != nil {
		return nil, fmt.Errorf("meshadmin: list devices: decode: %w", err)
	}
	out := make([]Device, 0, len(body.Devices))
	for _, d := range body.Devices {
		out = append(out, d.device())
	}
	return out, nil
}

func (t *tailscale) SetTags(ctx context.Context, deviceID string, tags []string) (Device, error) {
	b, _ := json.Marshal(map[string][]string{"tags": nonNil(tags)})
	if _, err := t.r.do(ctx, call{
		method: http.MethodPost, path: "/api/v2/device/" + url.PathEscape(deviceID) + "/tags", body: b,
		header: t.header(nil), op: "set tags", scope: "devices:core",
	}); err != nil {
		return Device{}, err
	}
	rp, err := t.r.do(ctx, call{
		method: http.MethodGet, path: "/api/v2/device/" + url.PathEscape(deviceID), query: url.Values{"fields": {"all"}},
		header: t.header(nil), op: "get device", scope: "devices:core:read",
	})
	if err != nil {
		return Device{}, err
	}
	var d tsDevice
	if err := json.Unmarshal(rp.body, &d); err != nil {
		return Device{}, fmt.Errorf("meshadmin: get device: decode: %w", err)
	}
	return d.device(), nil
}

func (t *tailscale) CreateAuthKey(ctx context.Context, req AuthKeyRequest) (AuthKey, error) {
	type create struct {
		Reusable      bool     `json:"reusable"`
		Ephemeral     bool     `json:"ephemeral"`
		Preauthorized bool     `json:"preauthorized"`
		Tags          []string `json:"tags,omitempty"`
	}
	var body struct {
		Capabilities struct {
			Devices struct {
				Create create `json:"create"`
			} `json:"devices"`
		} `json:"capabilities"`
		ExpirySeconds int64  `json:"expirySeconds,omitempty"`
		Description   string `json:"description,omitempty"`
	}
	body.Capabilities.Devices.Create = create{Reusable: req.Reusable, Ephemeral: req.Ephemeral, Preauthorized: true, Tags: req.Tags}
	body.ExpirySeconds = int64(req.TTL / time.Second)
	body.Description = req.Description
	b, _ := json.Marshal(body)
	rp, err := t.r.do(ctx, call{
		method: http.MethodPost, path: t.tailnetPath("keys"), body: b,
		header: t.header(nil), op: "create auth key", scope: "auth_keys",
	})
	if err != nil {
		return AuthKey{}, err
	}
	var k struct {
		ID      string `json:"id"`
		Key     string `json:"key"`
		Expires string `json:"expires"`
	}
	if err := json.Unmarshal(rp.body, &k); err != nil || k.Key == "" {
		return AuthKey{}, fmt.Errorf("meshadmin: create auth key: unexpected response")
	}
	return AuthKey{ID: k.ID, Secret: k.Key, Expires: parseTime(k.Expires), Tags: req.Tags}, nil
}

func (t *tailscale) GetPolicy(ctx context.Context) (PolicyDoc, error) {
	rp, err := t.r.do(ctx, call{
		method: http.MethodGet, path: t.tailnetPath("acl"),
		header: t.header(map[string]string{"Accept": "application/hujson"}),
		op:     "get policy", scope: "policy_file:read",
	})
	if err != nil {
		return PolicyDoc{}, err
	}
	return PolicyDoc{Text: string(rp.body), Version: rp.header.Get("ETag"), Exists: true}, nil
}

func (t *tailscale) ValidatePolicy(ctx context.Context, text string) error {
	rp, err := t.r.do(ctx, call{
		method: http.MethodPost, path: t.tailnetPath("acl", "validate"), body: []byte(text),
		header: t.header(map[string]string{"Content-Type": "application/hujson"}),
		op:     "validate policy", scope: "policy_file:read",
		okCodes: []int{http.StatusOK, http.StatusBadRequest},
	})
	if err != nil {
		return err
	}
	var v struct {
		Message string `json:"message"`
		Data    []struct {
			User   string   `json:"user"`
			Errors []string `json:"errors"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rp.body, &v)
	if rp.status == http.StatusBadRequest && v.Message == "" {
		v.Message = errorMessage(rp.body)
	}
	if v.Message == "" {
		return nil
	}
	msg := v.Message
	for _, d := range v.Data {
		for _, e := range d.Errors {
			msg += "; " + e
		}
	}
	return &ValidationError{Message: msg}
}

func (t *tailscale) SetPolicy(ctx context.Context, text, version string) (PolicyDoc, error) {
	h := map[string]string{"Content-Type": "application/hujson"}
	if version != "" {
		h["If-Match"] = quoteETag(version)
	}
	rp, err := t.r.do(ctx, call{
		method: http.MethodPost, path: t.tailnetPath("acl"), body: []byte(text),
		header: t.header(h), op: "set policy", scope: "policy_file",
	})
	if err != nil {
		return PolicyDoc{}, err
	}
	// The POST response echoes the policy but may not carry the new ETag; read it back.
	if etag := rp.header.Get("ETag"); etag != "" {
		return PolicyDoc{Text: text, Version: etag, Exists: true}, nil
	}
	return t.GetPolicy(ctx)
}

// quoteETag passes a header value through unchanged when it is already a quoted
// or weak entity tag, and quotes a bare token.
func quoteETag(v string) string {
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "W/") {
		return v
	}
	return `"` + v + `"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// parseTime parses RFC 3339; the zero value and pre-epoch placeholders such as
// 0001-01-01 come back as the zero time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 1971 {
		return time.Time{}
	}
	return t
}
