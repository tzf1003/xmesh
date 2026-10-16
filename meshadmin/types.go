package meshadmin

import (
	"context"
	"time"
)

// Kind names a control plane implementation.
type Kind string

const (
	KindTailscale Kind = "tailscale"
	KindHeadscale Kind = "headscale"
)

// Device is one node as the control plane sees it, normalised across providers.
type Device struct {
	ID         string    `json:"id"`
	NodeKey    string    `json:"node_key,omitempty"`
	Name       string    `json:"name"`
	Hostname   string    `json:"hostname,omitempty"`
	Addresses  []string  `json:"addresses,omitempty"`
	Tags       []string  `json:"tags"`
	User       string    `json:"user,omitempty"`
	OS         string    `json:"os,omitempty"`
	Version    string    `json:"version,omitempty"`
	Online     bool      `json:"online"`
	Authorized bool      `json:"authorized"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
	// Expires is the node key expiry; zero means the key does not expire.
	Expires time.Time `json:"expires,omitempty"`
}

// AuthKeyRequest describes a pre-authorised registration key.
type AuthKeyRequest struct {
	Tags      []string      `json:"tags"`
	Reusable  bool          `json:"reusable"`
	Ephemeral bool          `json:"ephemeral"`
	TTL       time.Duration `json:"ttl"`
	// User selects the Headscale user (name) that owns the key. Tailscale ignores it.
	User        string `json:"user,omitempty"`
	Description string `json:"description,omitempty"`
}

// AuthKey is a created key. Secret is the only copy: the control plane never
// returns it again, and Redacted() must be used wherever it could be logged.
type AuthKey struct {
	ID      string    `json:"id"`
	Secret  string    `json:"secret,omitempty"`
	Expires time.Time `json:"expires"`
	Tags    []string  `json:"tags"`
}

// Redacted returns a copy with the secret removed.
func (k AuthKey) Redacted() AuthKey {
	k.Secret = ""
	return k
}

// PolicyDoc is the tailnet policy as raw HuJSON plus an opaque version used for
// optimistic concurrency. Empty text with Exists=false means no policy is set.
type PolicyDoc struct {
	Text    string `json:"text"`
	Version string `json:"version"`
	Exists  bool   `json:"exists"`
}

// Capabilities states what a configured Provider can do right now.
type Capabilities struct {
	// PolicyWrite is false for Headscale in file mode, where the policy file is
	// owned by the server host.
	PolicyWrite bool `json:"policy_write"`
	// AtomicPolicyWrite is true when the control plane enforces the version
	// check on write (Tailscale If-Match). Headscale checks it client side only.
	AtomicPolicyWrite bool `json:"atomic_policy_write"`
	// DERPViaPolicy is true when DERP regions are written into the policy
	// (Tailscale derpMap); false when they are server configuration (Headscale).
	DERPViaPolicy bool `json:"derp_via_policy"`
}

// Provider is the control-plane client. Implementations: Tailscale and Headscale.
// All methods are safe for concurrent use.
type Provider interface {
	Kind() Kind
	Capabilities(ctx context.Context) (Capabilities, error)

	Devices(ctx context.Context) ([]Device, error)
	SetTags(ctx context.Context, deviceID string, tags []string) (Device, error)
	CreateAuthKey(ctx context.Context, req AuthKeyRequest) (AuthKey, error)

	GetPolicy(ctx context.Context) (PolicyDoc, error)
	ValidatePolicy(ctx context.Context, text string) error
	// SetPolicy writes text if the stored policy still has version. An empty
	// version writes unconditionally (first policy).
	SetPolicy(ctx context.Context, text, version string) (PolicyDoc, error)
}
