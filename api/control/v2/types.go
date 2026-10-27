package v2

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// ControlEndpointFile is control.json in the state directory (control-endpoint.schema.json).
type ControlEndpointFile struct {
	APIVersion    string       `json:"api_version"`
	KernelVersion string       `json:"kernel_version"`
	Node          string       `json:"node"`
	PID           int          `json:"pid"`
	BootID        string       `json:"boot_id"`
	Transport     Transport    `json:"transport"`
	Endpoint      string       `json:"endpoint"`
	StartedAt     Time         `json:"started_at"`
	Capabilities  []Capability `json:"capabilities"`
}

// Warning is one entry of StatusResponse.Warnings (control-common.schema.json).
type Warning struct {
	Code     WarningCode `json:"code"`
	Severity Severity    `json:"severity"`
	Message  string      `json:"message"`
}

// DrainState is the drain flag of the process (control-drain.schema.json).
type DrainState struct {
	Draining bool       `json:"draining"`
	Mode     *DrainMode `json:"mode"`
	Since    *Time      `json:"since"`
}

type DrainRequest struct {
	Mode      DrainMode `json:"mode,omitempty"`
	Scope     string    `json:"scope,omitempty"`
	Device    string    `json:"device,omitempty"`
	TimeoutMS int64     `json:"timeout_ms,omitempty"`
}

// Drain timeouts are expressed in milliseconds; zero uses the default budget.
const DefaultDrainTimeoutMS int64 = 15_000
const MaxDrainTimeoutMS int64 = (1<<63 - 1) / 1_000_000

type DrainResponse struct {
	Drain          DrainState     `json:"drain"`
	ClosedPTY      int            `json:"closed_pty"`
	ReleasedLeases int            `json:"released_leases"`
	Outcomes       []DrainOutcome `json:"outcomes,omitempty"`
}

type DrainOutcome struct {
	Device string `json:"device"`
	Error  string `json:"error,omitempty"`
}

// StatusResponse is GET /v2/status and the data of a status event (control-status.schema.json).
type StatusResponse struct {
	Control              ControlInfo     `json:"control"`
	Lifecycle            string          `json:"lifecycle"`
	StartedAt            Time            `json:"started_at"`
	PID                  int             `json:"pid"`
	Drain                DrainState      `json:"drain"`
	OS                   string          `json:"os"`
	Arch                 string          `json:"arch"`
	Hostname             string          `json:"hostname"`
	Home                 string          `json:"home"`
	Roles                []string        `json:"roles"`
	Network              NetworkIdentity `json:"network"`
	RestartRequired      bool            `json:"restart_required"`
	PolicyReloadFailures int             `json:"policy_reload_failures"`
	AuthkeyReadable      bool            `json:"authkey_readable"`
	Warnings             []Warning       `json:"warnings"`
	PathSep              string          `json:"path_sep"`
	Shells               []string        `json:"shells"`
	Workspaces           []Workspace     `json:"workspaces"`
	Backends             []Backend       `json:"backends"`
	Limits               Limits          `json:"limits"`
	Paths                Paths           `json:"paths"`
	Gateway              *GatewayInfo    `json:"gateway,omitempty"`
	Ingress              *IngressInfo    `json:"ingress,omitempty"`
	Peers                PeerSummary     `json:"peers"`
	CI                   *CiSummary      `json:"ci,omitempty"`
}

type ControlInfo struct {
	APIVersion    string       `json:"api_version"`
	KernelVersion string       `json:"kernel_version"`
	BootID        string       `json:"boot_id"`
	Capabilities  []Capability `json:"capabilities"`
}

type NetworkIdentity struct {
	Mode   string   `json:"mode"`
	NodeID string   `json:"node_id"`
	Tags   []string `json:"tags"`
}

type Workspace struct {
	Alias string `json:"alias"`
	Path  string `json:"path"`
	Desc  string `json:"desc"`
	Error string `json:"error,omitempty"`
}

type Backend struct {
	Name     string `json:"name"`
	Policy   string `json:"policy"`
	State    string `json:"state"`
	Favorite bool   `json:"favorite"`
	// EnvNames are the names of the environment variables set for a stdio backend; never their values.
	EnvNames []string `json:"env_names,omitempty"`
}

type Limits struct {
	ExecShared         int `json:"exec_shared"`
	ExecExclusive      int `json:"exec_exclusive"`
	ExecSharedInUse    int `json:"exec_shared_in_use"`
	ExecExclusiveInUse int `json:"exec_exclusive_in_use"`
	PTYMaxSessions     int `json:"pty_max_sessions"`
	PTYMaxPerCaller    int `json:"pty_max_per_caller"`
	PTYInUse           int `json:"pty_in_use"`
	PTYActive          int `json:"pty_active"`
	PTYClosed          int `json:"pty_closed"`
	PTYClosedRetainMax int `json:"pty_closed_retain_max"`
}

type Paths struct {
	ConfigFile       string  `json:"config_file"`
	StateDir         string  `json:"state_dir"`
	LogDir           *string `json:"log_dir"`
	AuditDir         *string `json:"audit_dir"`
	ComputerEndpoint string  `json:"computer_endpoint,omitempty"`
}

// GatewayInfo is the gateway block of StatusResponse (CTR-CTL-03, CTR-GW-07).
type GatewayInfo struct {
	Listen    string           `json:"listen"`
	URL       string           `json:"url"`
	Auth      string           `json:"auth"`
	Access    []string         `json:"access"`
	Hosts     []string         `json:"hosts"`
	Addresses []GatewayAddress `json:"addresses"`
}

// GatewayUpdateRequest replaces the listen address and the reach settings of the gateway (CTR-CTL-13).
type GatewayUpdateRequest struct {
	Listen string   `json:"listen"`
	Access []string `json:"access"`
	Hosts  []string `json:"hosts"`
}

// GatewayUpdateResponse echoes what was written. Since API 1.6 the running gateway moves to a new Listen
// itself, so RestartRequired stays false; a move that failed is in ApplyErrors and the gateway is still
// where it was.
type GatewayUpdateResponse struct {
	Listen          string       `json:"listen"`
	Access          []string     `json:"access"`
	Hosts           []string     `json:"hosts"`
	RestartRequired bool         `json:"restart_required"`
	ApplyErrors     []ApplyError `json:"apply_errors"`
}

// GatewayAddress is one address the gateway answers on right now.
type GatewayAddress struct {
	Kind string `json:"kind"`
	Host string `json:"host"`
	URL  string `json:"url"`
}

type IngressInfo struct {
	Listen string  `json:"listen"`
	Issuer *string `json:"issuer"`
}

type PeerSummary struct {
	Total  int `json:"total"`
	Online int `json:"online"`
}

// Peer is one device in GET /v2/peers (control-peers.schema.json).
type Peer struct {
	DeviceID        string       `json:"device_id,omitempty"`
	DisplayName     string       `json:"display_name,omitempty"`
	TrustState      string       `json:"trust_state,omitempty"`
	Paths           []DevicePath `json:"paths,omitempty"`
	ActivePath      string       `json:"active_path,omitempty"`
	ConnectionState string       `json:"connection_state,omitempty"`
	// ActiveDirection is which end dialed the active connection: "outgoing" (本机拨出) or "incoming" (对端拨出).
	ActiveDirection string `json:"active_direction,omitempty"`
	// PeerPaths is the device's own report of the paths it dials to this node, for display only.
	PeerPaths  []PeerPathQuality `json:"peer_paths,omitempty"`
	Name       string            `json:"name"`
	Self       bool              `json:"self"`
	NodeID     string            `json:"node_id"`
	Tags       []string          `json:"tags"`
	Online     bool              `json:"online"`
	Connection string            `json:"connection"`
	DERPRegion *string           `json:"derp_region"`
	Addrs      []string          `json:"addrs"`
	LastSeen   *Time             `json:"last_seen"`
	OS         *string           `json:"os"`
	Version    *string           `json:"version"`
	// Info is what the gateway has read from the device's sys.info. Absent on this node, on offline
	// devices and on a node without a gateway.
	Info *PeerInfo `json:"info,omitempty"`
}

type DevicePath struct {
	Kind       string `json:"kind"`
	Address    string `json:"address"`
	Reachable  bool   `json:"reachable"`
	VerifiedAt *Time  `json:"verified_at"`
	// Direction is who dials this path and measured Quality: always "outgoing" (本机拨出).
	Direction string `json:"direction,omitempty"`
	// Quality is the last few minutes of probe results; omitted until the first probe finishes.
	Quality *PathQuality `json:"quality,omitempty"`
}

// PeerPathQuality is how the device measured one kind of path it dials to this node (对端拨出).
// ReceivedAt is when the report arrived; the report is dropped once it is a minute old.
type PeerPathQuality struct {
	Kind       string      `json:"kind"`
	Quality    PathQuality `json:"quality"`
	ReceivedAt Time        `json:"received_at"`
}

// PathQuality summarises recent measurements of one path: probes, and the heartbeat of the carrier that
// runs on it. Latency is one request/response round trip on an established connection in both cases,
// so the active path and the probed ones compare directly.
type PathQuality struct {
	Samples   int     `json:"samples"`
	LatencyMs float64 `json:"latency_ms"`
	P90Ms     float64 `json:"p90_ms"`
	JitterMs  float64 `json:"jitter_ms"`
	// Loss is the share of failed probes in the window, 0 to 1.
	Loss float64 `json:"loss"`
	// Flaps counts successes followed by a failure in the window.
	Flaps int `json:"flaps"`
	// Score is what smart path selection ranks this path by, lower being better; omitted while the
	// path has too few probes to be scored.
	Score *float64 `json:"score,omitempty"`
}

// PeerInfo is the gateway's background reading of one online device (CTR-CTL-05).
type PeerInfo struct {
	// RefreshedAt is the latest successful read; null until the first one has finished.
	RefreshedAt *Time `json:"refreshed_at"`
	// Error is why the latest read failed; null after a success and while none has finished.
	Error *string `json:"error"`
}

type PeersResponse struct {
	Peers []Peer `json:"peers"`
}

// PtySession, ExecRun and BackendLease are the read-only views in control-sessions.schema.json.
// None of them carries terminal text, arguments, stdin or environment values.
type PtySession struct {
	Kind        string  `json:"kind"`
	ID          string  `json:"id"`
	Name        *string `json:"name"`
	Owner       string  `json:"owner"`
	OAuthClient *string `json:"oauth_client"`
	PID         int     `json:"pid"`
	State       string  `json:"state"`
	IdleS       int     `json:"idle_s"`
	Cols        int     `json:"cols"`
	Rows        int     `json:"rows"`
	Cwd         string  `json:"cwd"`
	CreatedAt   Time    `json:"created_at"`
}

type ExecRun struct {
	Kind        string  `json:"kind"`
	ID          string  `json:"id"`
	Caller      string  `json:"caller"`
	OAuthClient *string `json:"oauth_client"`
	Cmd         string  `json:"cmd"`
	Mode        string  `json:"mode"`
	State       string  `json:"state"`
	StartedAt   Time    `json:"started_at"`
}

type BackendLease struct {
	Kind      string `json:"kind"`
	Resource  string `json:"resource"`
	Owner     string `json:"owner"`
	Token     uint64 `json:"token"`
	ExpiresAt Time   `json:"expires_at"`
}

type SessionsResponse struct {
	PTY             []PtySession     `json:"pty"`
	Exec            []ExecRun        `json:"exec"`
	Leases          []BackendLease   `json:"leases"`
	Executions      []ToolExecution  `json:"executions,omitzero"`
	ExecutionLimits *ExecutionLimits `json:"execution_limits,omitempty"`
}

// ToolExecution is a live call or asynchronous UI task at one receiving layer.
type ToolExecution struct {
	ID          string `json:"id"`
	Caller      string `json:"caller"`
	OAuthClient string `json:"oauth_client,omitempty"`
	Device      string `json:"device"`
	Tool        string `json:"tool"`
	Layer       string `json:"layer"`
	State       string `json:"state"`
	StartedAt   Time   `json:"started_at"`
	UpdatedAt   Time   `json:"updated_at"`
	RunID       string `json:"run_id,omitempty"`
	TaskStatus  string `json:"task_status,omitempty"`
}

type ExecutionLimits struct {
	MaxActive     int    `json:"max_active"`
	Active        int    `json:"active"`
	RejectedTotal uint64 `json:"rejected_total"`
}

// Session is the union of the three views; exactly one field is set. It encodes as that view and
// decodes by its "kind".
type Session struct {
	PTY   *PtySession
	Exec  *ExecRun
	Lease *BackendLease
}

func (s Session) MarshalJSON() ([]byte, error) {
	switch {
	case s.PTY != nil:
		return json.Marshal(s.PTY)
	case s.Exec != nil:
		return json.Marshal(s.Exec)
	case s.Lease != nil:
		return json.Marshal(s.Lease)
	}
	return nil, fmt.Errorf("control: empty Session")
}

func (s *Session) UnmarshalJSON(b []byte) error {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	*s = Session{}
	switch head.Kind {
	case "pty":
		s.PTY = new(PtySession)
		return strictUnmarshal(b, s.PTY)
	case "exec":
		s.Exec = new(ExecRun)
		return strictUnmarshal(b, s.Exec)
	case "lease":
		s.Lease = new(BackendLease)
		return strictUnmarshal(b, s.Lease)
	}
	return fmt.Errorf("control: unknown session kind %q", head.Kind)
}

// ConfigResponse is GET /v2/config (control-config.schema.json).
type ConfigResponse struct {
	ETag            string           `json:"etag"`
	Content         string           `json:"content"`
	Parsed          map[string]any   `json:"parsed"`
	Valid           bool             `json:"valid"`
	LoadError       *ConfigLoadError `json:"load_error"`
	EffectiveETag   string           `json:"effective_etag"`
	RestartRequired bool             `json:"restart_required"`
}

type ConfigLoadError struct {
	Message string `json:"message"`
	Line    *int   `json:"line"`
	Column  *int   `json:"column"`
}

type ConfigPutRequest struct {
	Content string `json:"content"`
	DryRun  bool   `json:"dry_run,omitempty"`
	// Authkey is a write-only Tailnet admission key, stored by the kernel in its private state directory.
	Authkey *string `json:"authkey,omitempty"`
}

type ConfigChange struct {
	Pointer         string          `json:"pointer"`
	Op              string          `json:"op"`
	Before          json.RawMessage `json:"before,omitempty"`
	After           json.RawMessage `json:"after,omitempty"`
	RestartRequired bool            `json:"restart_required"`
	WeakensSecurity bool            `json:"weakens_security"`
}

type ConfigPutResponse struct {
	Applied         bool           `json:"applied"`
	ETag            string         `json:"etag"`
	Diff            []ConfigChange `json:"diff"`
	RestartRequired bool           `json:"restart_required"`
}

// ConfigReloadResponse is the outcome of re-reading the configuration file now. Applied is
// true when this call made the file's content the generation in effect. Invalid content
// keeps the last good generation and sets LoadError.
type ConfigReloadResponse struct {
	Applied         bool             `json:"applied"`
	Valid           bool             `json:"valid"`
	LoadError       *ConfigLoadError `json:"load_error"`
	ETag            string           `json:"etag"`
	EffectiveETag   string           `json:"effective_etag"`
	ChangedPaths    []string         `json:"changed_paths"`
	RestartRequired bool             `json:"restart_required"`
	RestartPaths    []string         `json:"restart_paths"`
	ApplyErrors     []ApplyError     `json:"apply_errors"`
}

// ApplyError is a part of the configuration that is valid but could not be put into effect in the
// running process, for example a gateway.listen whose port is taken. The process keeps running
// as it was and the failure stays reported until a later reload succeeds.
type ApplyError struct {
	Section string `json:"section"`
	Message string `json:"message"`
}

// ConfigEdit is one JSON Pointer edit of the configuration file (ConfigEdit in control-config.schema.json).
type ConfigEdit struct {
	Path   string `json:"path"`
	Value  any    `json:"value,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

// ConfigEditRequest is PATCH /v2/config: the edits apply in order, all or none.
type ConfigEditRequest struct {
	Edits  []ConfigEdit `json:"edits"`
	DryRun bool         `json:"dry_run,omitempty"`
}

// BackendEnvPatch is PATCH /v2/backends/{name}/env: Unset is applied first, then Set.
type BackendEnvPatch struct {
	Set   map[string]string `json:"set,omitempty"`
	Unset []string          `json:"unset,omitempty"`
}

// BackendEnvResponse lists the variable names a backend has after a change. Values are never returned.
type BackendEnvResponse struct {
	Name  string   `json:"name"`
	Names []string `json:"names"`
}

// PairDocument is the public pairing file; it matches mesh.Trusted.
type PairDocument struct {
	Version     int      `json:"version"`
	DeviceID    string   `json:"device_id"`
	PublicKey   []byte   `json:"public_key"`
	Name        string   `json:"name"`
	Fingerprint string   `json:"fingerprint"`
	Tags        []string `json:"tags"`
}

type PairExportResponse struct {
	Pair PairDocument `json:"pair"`
}

type PairImportRequest struct {
	Pair        PairDocument `json:"pair"`
	Fingerprint string       `json:"fingerprint"`
	Tags        []string     `json:"tags,omitempty"`
}

type PairImportResponse struct {
	Name            string   `json:"name"`
	Fingerprint     string   `json:"fingerprint"`
	Tags            []string `json:"tags"`
	RestartRequired bool     `json:"restart_required"`
}

// PairTrustUpdateRequest replaces the roles of an already trusted device. It is served only on the
// local control channel, like import and revoke.
type PairTrustUpdateRequest struct {
	Tags []string `json:"tags"`
}

type PairTrustUpdateResponse struct {
	DeviceID string   `json:"device_id"`
	Tags     []string `json:"tags"`
}

// PairPasswordRequest sets or changes the connection password. The password is write-only: no
// response ever carries it or anything derived from it.
type PairPasswordRequest struct {
	Password string `json:"password"`
}

// PairPasswordState is what the panel shows about the connection password.
type PairPasswordState struct {
	Set            bool  `json:"set"`
	UpdatedAt      *Time `json:"updated_at"`
	FailedAttempts int   `json:"failed_attempts"`
	LockedUntil    *Time `json:"locked_until"`
}

// PairDiscoveredDevice is an unpaired device seen on the LAN.
type PairDiscoveredDevice struct {
	DeviceID    string `json:"device_id"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Address     string `json:"address"`
	SeenAt      Time   `json:"seen_at"`
}

type PairDiscoveredResponse struct {
	Devices []PairDiscoveredDevice `json:"devices"`
}

// PairConnectRequest pairs with a device on the LAN. Address or Fingerprint must be set. When only
// Fingerprint is set the address comes from the discovery list.
type PairConnectRequest struct {
	Path        string `json:"path,omitempty"`
	Address     string `json:"address,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Password    string `json:"password"`
}

type PairConnectResponse struct {
	DeviceID    string   `json:"device_id"`
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Fingerprint string   `json:"fingerprint"`
	Address     string   `json:"address"`
	Tags        []string `json:"tags"`
}

type PatInfo struct {
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	TokenFile  string   `json:"token_file"`
	ModifiedAt *Time    `json:"modified_at"`
}

type PatList struct {
	Pats     []PatInfo `json:"pats"`
	Enforced bool      `json:"enforced"`
}

type PatCreateRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// PatSecret is the only type that carries a token. It exists in the response of create and rotate
// and nowhere else.
type PatSecret struct {
	Pat   PatInfo `json:"pat"`
	Token string  `json:"token"`
}

type IngressClient struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	Scopes                  []string `json:"scopes"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type IngressClientsResponse struct {
	Issuer  *string         `json:"issuer"`
	Clients []IngressClient `json:"clients"`
}

type IngressPasswordRequest struct {
	Password string `json:"password"`
}

type IngressPasswordResponse struct {
	RotatedAt Time `json:"rotated_at"`
}

// AuditQuery is the query string of GET /v2/audit. Empty fields are left out.
type AuditQuery struct {
	Since  string `json:"since,omitempty"`
	Until  string `json:"until,omitempty"`
	Limit  string `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Event  string `json:"event,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Device string `json:"device,omitempty"`
	Caller string `json:"caller,omitempty"`
	Status string `json:"status,omitempty"`
}

// Values returns the query string parameters.
func (q AuditQuery) Values() url.Values {
	v := url.Values{}
	for k, s := range map[string]string{
		"since": q.Since, "until": q.Until, "limit": q.Limit, "cursor": q.Cursor, "event": q.Event,
		"tool": q.Tool, "device": q.Device, "caller": q.Caller, "status": q.Status,
	} {
		if s != "" {
			v.Set(k, s)
		}
	}
	return v
}

// AuditResponse is GET /v2/audit. Every element of Events is a CTR-AUD-01 event passed through
// verbatim, so this package does not restate that schema.
type AuditResponse struct {
	Enabled    bool              `json:"enabled"`
	Events     []json.RawMessage `json:"events"`
	NextCursor *string           `json:"next_cursor"`
}

func strictUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytesReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// CloudflareOffer is what a paired device sends so another can join its Cloudflare mesh: the
// receiver's own tunnel and Access service token, and the sender's hostname. It never carries a
// Cloudflare API token.
type CloudflareOffer struct {
	Hostname     string `json:"hostname"`
	TunnelToken  string `json:"tunnel_token"`
	AccessID     string `json:"access_id"`
	AccessSecret string `json:"access_secret"`
	PeerHostname string `json:"peer_hostname"`
}

// TailnetOffer is a pre-authorised key for the sender's tailnet. ControlURL is empty for
// Tailscale's own service. The key's tags belong to the control plane, not to the device's xmesh tags.
type TailnetOffer struct {
	ControlURL string `json:"control_url,omitempty"`
	AuthKey    string `json:"auth_key"`
	Ephemeral  bool   `json:"ephemeral,omitempty"`
}

// DeviceOffer is one connection method a paired device proposes to another.
type DeviceOffer struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	Cloudflare *CloudflareOffer `json:"cloudflare,omitempty"`
	Tailnet    *TailnetOffer    `json:"tailnet,omitempty"`
}

// OfferSendRequest delivers an offer to a paired device. The device applies nothing until its user accepts.
type OfferSendRequest struct {
	DeviceID string      `json:"device_id"`
	Offer    DeviceOffer `json:"offer"`
}

// OfferCloudflareSummary is the part of a Cloudflare offer that is safe to show: no credentials.
type OfferCloudflareSummary struct {
	Hostname     string `json:"hostname"`
	PeerHostname string `json:"peer_hostname"`
}

// OfferTailnetSummary is the part of a Tailscale offer that is safe to show: no key.
type OfferTailnetSummary struct {
	ControlURL string `json:"control_url,omitempty"`
	Ephemeral  bool   `json:"ephemeral,omitempty"`
}

// OfferSummary is a received offer as the panel shows it. The sender is the device the connection
// proved, and the credentials are never included.
type OfferSummary struct {
	ID         string                  `json:"id"`
	Kind       string                  `json:"kind"`
	FromID     string                  `json:"from_id"`
	FromName   string                  `json:"from_name"`
	ReceivedAt Time                    `json:"received_at"`
	ExpiresAt  Time                    `json:"expires_at"`
	Cloudflare *OfferCloudflareSummary `json:"cloudflare,omitempty"`
	Tailnet    *OfferTailnetSummary    `json:"tailnet,omitempty"`
}

type OffersResponse struct {
	Offers []OfferSummary `json:"offers"`
}

// OfferAcceptResponse reports an applied offer. RestartRequired is true while the running node still
// has to restart to use it.
type OfferAcceptResponse struct {
	Kind            string   `json:"kind"`
	RestartRequired bool     `json:"restart_required"`
	ChangedPaths    []string `json:"changed_paths"`
}

// OfferSendResponse says how an offer reached the device: "delivered" into its inbox, or "queued"
// here when it can only collect the offer over its own connection to this node.
type OfferSendResponse struct {
	Delivery string `json:"delivery"`
}
