package v1

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
	Mode DrainMode `json:"mode,omitempty"`
}

type DrainResponse struct {
	Drain          DrainState `json:"drain"`
	ClosedPTY      int        `json:"closed_pty"`
	ReleasedLeases int        `json:"released_leases"`
}

// StatusResponse is GET /v1/status and the data of a status event (control-status.schema.json).
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
	ConfigFile string  `json:"config_file"`
	StateDir   string  `json:"state_dir"`
	LogDir     *string `json:"log_dir"`
	AuditDir   *string `json:"audit_dir"`
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

// GatewayUpdateResponse echoes what was written. RestartRequired is true when Listen changed.
type GatewayUpdateResponse struct {
	Listen          string   `json:"listen"`
	Access          []string `json:"access"`
	Hosts           []string `json:"hosts"`
	RestartRequired bool     `json:"restart_required"`
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

// Peer is one device in GET /v1/peers (control-peers.schema.json).
type Peer struct {
	Name       string   `json:"name"`
	Self       bool     `json:"self"`
	NodeID     string   `json:"node_id"`
	Tags       []string `json:"tags"`
	Online     bool     `json:"online"`
	Connection string   `json:"connection"`
	DERPRegion *string  `json:"derp_region"`
	Addrs      []string `json:"addrs"`
	LastSeen   *Time    `json:"last_seen"`
	OS         *string  `json:"os"`
	Version    *string  `json:"version"`
	// Info is what the gateway has read from the device's sys.info. Absent on this node, on offline
	// devices and on a node without a gateway.
	Info *PeerInfo `json:"info,omitempty"`
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
	PTY    []PtySession   `json:"pty"`
	Exec   []ExecRun      `json:"exec"`
	Leases []BackendLease `json:"leases"`
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

// ConfigResponse is GET /v1/config (control-config.schema.json).
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
}

// ConfigEdit is one JSON Pointer edit of the configuration file (ConfigEdit in control-config.schema.json).
type ConfigEdit struct {
	Path   string `json:"path"`
	Value  any    `json:"value,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

// ConfigEditRequest is PATCH /v1/config: the edits apply in order, all or none.
type ConfigEditRequest struct {
	Edits  []ConfigEdit `json:"edits"`
	DryRun bool         `json:"dry_run,omitempty"`
}

// BackendEnvPatch is PATCH /v1/backends/{name}/env: Unset is applied first, then Set.
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
	Name           string   `json:"name"`
	Fingerprint    string   `json:"fingerprint"`
	CertificatePEM string   `json:"certificate_pem"`
	Tags           []string `json:"tags"`
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
	Address     string `json:"address,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Password    string `json:"password"`
}

type PairConnectResponse struct {
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

// AuditQuery is the query string of GET /v1/audit. Empty fields are left out.
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

// AuditResponse is GET /v1/audit. Every element of Events is a CTR-AUD-01 event passed through
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
