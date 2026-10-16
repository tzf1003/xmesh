package meshadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HeadscaleConfig configures the Headscale Provider. APIKey is the key created
// with `headscale apikeys create`; Headscale has no scopes, so it grants full
// control and must stay in the system keychain.
type HeadscaleConfig struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

type headscale struct {
	r      *rest
	apiKey string
}

// NewHeadscale returns the Provider for Headscale's REST API (/api/v1), verified
// against v0.29.5.
func NewHeadscale(cfg HeadscaleConfig) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("%w: Headscale needs an API key", ErrInvalid)
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("%w: Headscale needs the server URL", ErrInvalid)
	}
	r, err := newRest(cfg.BaseURL, cfg.HTTPClient)
	if err != nil {
		return nil, err
	}
	return &headscale{r: r, apiKey: cfg.APIKey}, nil
}

func (h *headscale) Kind() Kind { return KindHeadscale }

func (h *headscale) hdr() map[string]string {
	return map[string]string{"Authorization": "Bearer " + h.apiKey}
}

// Capabilities probes the policy mode. In file mode GET /policy answers with the
// file and a null updatedAt, and PUT is refused by the server.
func (h *headscale) Capabilities(ctx context.Context) (Capabilities, error) {
	caps := Capabilities{PolicyWrite: true, AtomicPolicyWrite: false, DERPViaPolicy: false}
	rp, err := h.r.do(ctx, call{method: http.MethodGet, path: "/api/v1/policy", header: h.hdr(), op: "get policy",
		okCodes: []int{http.StatusOK, http.StatusInternalServerError}})
	if err != nil {
		return Capabilities{}, err
	}
	if rp.status == http.StatusOK {
		var p struct {
			UpdatedAt *string `json:"updatedAt"`
		}
		if json.Unmarshal(rp.body, &p) == nil && p.UpdatedAt == nil {
			caps.PolicyWrite = false
		}
	}
	return caps, nil
}

type hsNode struct {
	ID        string   `json:"id"`
	NodeKey   string   `json:"nodeKey"`
	Name      string   `json:"name"`
	GivenName string   `json:"givenName"`
	IPs       []string `json:"ipAddresses"`
	Tags      []string `json:"tags"`
	Online    bool     `json:"online"`
	LastSeen  string   `json:"lastSeen"`
	Expiry    string   `json:"expiry"`
	User      struct {
		Name string `json:"name"`
	} `json:"user"`
}

func (n hsNode) device() Device {
	name := n.GivenName
	if name == "" {
		name = n.Name
	}
	return Device{
		ID: n.ID, NodeKey: n.NodeKey, Name: name, Hostname: n.Name, Addresses: n.IPs, Tags: nonNil(n.Tags),
		User: n.User.Name, Online: n.Online, Authorized: true,
		LastSeen: parseTime(n.LastSeen), Expires: parseTime(n.Expiry),
	}
}

func (h *headscale) Devices(ctx context.Context) ([]Device, error) {
	rp, err := h.r.do(ctx, call{method: http.MethodGet, path: "/api/v1/node", header: h.hdr(), op: "list devices"})
	if err != nil {
		return nil, err
	}
	var body struct {
		Nodes []hsNode `json:"nodes"`
	}
	if err := json.Unmarshal(rp.body, &body); err != nil {
		return nil, fmt.Errorf("meshadmin: list devices: decode: %w", err)
	}
	out := make([]Device, 0, len(body.Nodes))
	for _, n := range body.Nodes {
		out = append(out, n.device())
	}
	return out, nil
}

func (h *headscale) SetTags(ctx context.Context, deviceID string, tags []string) (Device, error) {
	if !isDigits(deviceID) {
		return Device{}, fmt.Errorf("%w: Headscale device id %q is not numeric", ErrInvalid, deviceID)
	}
	b, _ := json.Marshal(map[string][]string{"tags": nonNil(tags)})
	rp, err := h.r.do(ctx, call{method: http.MethodPost, path: "/api/v1/node/" + deviceID + "/tags", body: b,
		header: h.hdr(), op: "set tags"})
	if err != nil {
		return Device{}, err
	}
	var body struct {
		Node hsNode `json:"node"`
	}
	if err := json.Unmarshal(rp.body, &body); err != nil {
		return Device{}, fmt.Errorf("meshadmin: set tags: decode: %w", err)
	}
	return body.Node.device(), nil
}

func (h *headscale) userID(ctx context.Context, name string) (string, error) {
	rp, err := h.r.do(ctx, call{method: http.MethodGet, path: "/api/v1/user", header: h.hdr(), op: "list users"})
	if err != nil {
		return "", err
	}
	var body struct {
		Users []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rp.body, &body); err != nil {
		return "", fmt.Errorf("meshadmin: list users: decode: %w", err)
	}
	if name == "" {
		if len(body.Users) == 1 {
			return body.Users[0].ID, nil
		}
		return "", fmt.Errorf("%w: Headscale has %d users; choose one with --user", ErrInvalid, len(body.Users))
	}
	for _, u := range body.Users {
		if u.Name == name {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("%w: Headscale user %q", ErrNotFound, name)
}

func (h *headscale) CreateAuthKey(ctx context.Context, req AuthKeyRequest) (AuthKey, error) {
	uid, err := h.userID(ctx, req.User)
	if err != nil {
		return AuthKey{}, err
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	exp := time.Now().Add(ttl).UTC()
	b, _ := json.Marshal(map[string]any{
		"user": uid, "reusable": req.Reusable, "ephemeral": req.Ephemeral,
		"expiration": exp.Format(time.RFC3339), "aclTags": nonNil(req.Tags),
	})
	rp, err := h.r.do(ctx, call{method: http.MethodPost, path: "/api/v1/preauthkey", body: b, header: h.hdr(), op: "create auth key"})
	if err != nil {
		return AuthKey{}, err
	}
	var body struct {
		Key struct {
			ID         string `json:"id"`
			Key        string `json:"key"`
			Expiration string `json:"expiration"`
		} `json:"preAuthKey"`
	}
	if err := json.Unmarshal(rp.body, &body); err != nil || body.Key.Key == "" {
		return AuthKey{}, fmt.Errorf("meshadmin: create auth key: unexpected response")
	}
	e := parseTime(body.Key.Expiration)
	if e.IsZero() {
		e = exp
	}
	return AuthKey{ID: body.Key.ID, Secret: body.Key.Key, Expires: e, Tags: req.Tags}, nil
}

func headscaleVersion(text, updatedAt string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:]) + "@" + updatedAt
}

func (h *headscale) GetPolicy(ctx context.Context) (PolicyDoc, error) {
	rp, err := h.r.do(ctx, call{method: http.MethodGet, path: "/api/v1/policy", header: h.hdr(), op: "get policy",
		okCodes: []int{http.StatusOK, http.StatusInternalServerError}})
	if err != nil {
		return PolicyDoc{}, err
	}
	if rp.status == http.StatusInternalServerError {
		// Database mode with nothing stored yet answers "acl policy not found".
		if strings.Contains(errorMessage(rp.body), "policy not found") {
			return PolicyDoc{}, nil
		}
		return PolicyDoc{}, &APIError{Op: "get policy", Status: rp.status, Message: errorMessage(rp.body)}
	}
	var p struct {
		Policy    string  `json:"policy"`
		UpdatedAt *string `json:"updatedAt"`
	}
	if err := json.Unmarshal(rp.body, &p); err != nil {
		return PolicyDoc{}, fmt.Errorf("meshadmin: get policy: decode: %w", err)
	}
	at := ""
	if p.UpdatedAt != nil {
		at = *p.UpdatedAt
	}
	return PolicyDoc{Text: p.Policy, Version: headscaleVersion(p.Policy, at), Exists: p.Policy != ""}, nil
}

func (h *headscale) ValidatePolicy(ctx context.Context, text string) error {
	b, _ := json.Marshal(map[string]string{"policy": text})
	_, err := h.r.do(ctx, call{method: http.MethodPost, path: "/api/v1/policy/check", body: b, header: h.hdr(), op: "validate policy"})
	var inv *InvalidError
	var api *APIError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &inv):
		return &ValidationError{Message: inv.Message}
	case errors.As(err, &api):
		return &ValidationError{Message: api.Message}
	}
	return err
}

func (h *headscale) SetPolicy(ctx context.Context, text, version string) (PolicyDoc, error) {
	// Headscale has no conditional write: compare against a fresh read first.
	// The window between the read and the PUT is small but not zero (RUL-21).
	if version != "" {
		cur, err := h.GetPolicy(ctx)
		if err != nil {
			return PolicyDoc{}, err
		}
		if cur.Version != version {
			return PolicyDoc{}, ErrConflict
		}
	}
	b, _ := json.Marshal(map[string]string{"policy": text})
	rp, err := h.r.do(ctx, call{method: http.MethodPut, path: "/api/v1/policy", body: b, header: h.hdr(), op: "set policy"})
	if err != nil {
		var api *APIError
		var inv *InvalidError
		if errors.As(err, &api) {
			if strings.Contains(api.Message, "other than 'database'") {
				return PolicyDoc{}, fmt.Errorf("%w: Headscale runs with policy.mode=file; edit the policy file on the server and reload", ErrUnsupported)
			}
			return PolicyDoc{}, &ValidationError{Message: api.Message}
		}
		if errors.As(err, &inv) {
			return PolicyDoc{}, &ValidationError{Message: inv.Message}
		}
		return PolicyDoc{}, err
	}
	var p struct {
		Policy    string `json:"policy"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := json.Unmarshal(rp.body, &p); err != nil {
		return PolicyDoc{}, fmt.Errorf("meshadmin: set policy: decode: %w", err)
	}
	return PolicyDoc{Text: p.Policy, Version: headscaleVersion(p.Policy, p.UpdatedAt), Exists: true}, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
