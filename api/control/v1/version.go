package v1

// APIVersion is the control API version this package implements: major.minor. A minor bump only
// adds fields, routes, enum values and capabilities. A major bump changes the path prefix.
const APIVersion = "1.4"

const (
	// HeaderVersion carries APIVersion on every response and in control.json.
	HeaderVersion = "X-Xmesh-Control-Version"

	ContentTypeJSON = "application/json; charset=utf-8"
	ContentTypeSSE  = "text/event-stream"

	// MaxRequestBytes is the body limit for every route except PUT /v1/config.
	MaxRequestBytes = 64 << 10
	// MaxConfigBytes is the body limit for PUT /v1/config.
	MaxConfigBytes = 1 << 20

	// EventRingSize is how many events the kernel keeps for Last-Event-ID resume.
	EventRingSize = 1024
	// HeartbeatSeconds is the interval of the SSE comment line sent when the stream is idle.
	HeartbeatSeconds = 15
	// Heartbeat is the SSE comment line itself.
	Heartbeat = ": ping\n\n"
	// StreamIdleTimeoutSeconds is how long a client waits without any bytes before it treats the
	// stream as dead and reconnects with Last-Event-ID.
	StreamIdleTimeoutSeconds = 45
)

// ControlFileName is the file in the state directory that advertises the endpoint.
const ControlFileName = "control.json"

// Capability names a group of routes a kernel has implemented.
type Capability string

const (
	CapStatus      Capability = "status"
	CapEvents      Capability = "events"
	CapPeers       Capability = "peers"
	CapSessions    Capability = "sessions"
	CapDrain       Capability = "drain"
	CapConfigRead  Capability = "config.read"
	CapConfigWrite Capability = "config.write"
	CapPair        Capability = "pair"
	CapPats        Capability = "pats"
	CapGateway     Capability = "gateway"
	CapIngress     Capability = "ingress"
	CapAudit       Capability = "audit"
)

// Severity of a Warning.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// WarningCode is the stable code of a Warning. Unknown codes must be treated as WarnUnclassified.
type WarningCode string

const (
	WarnRestartRequired    WarningCode = "restart_required"
	WarnPolicyReloadFailed WarningCode = "policy_reload_failed"
	WarnAllowDangerous     WarningCode = "allow_dangerous"
	WarnTailnetNotReady    WarningCode = "tailnet_not_ready"
	WarnAuthkeyUnreadable  WarningCode = "authkey_unreadable"
	WarnPeerOffline        WarningCode = "peer_offline"
	WarnAuditDropped       WarningCode = "audit_dropped"
	WarnQuotaExhausted     WarningCode = "quota_exhausted"
	WarnSandboxUnavailable WarningCode = "sandbox_unavailable"
	WarnFirewallBlocked    WarningCode = "firewall_blocked"
	WarnUnclassified       WarningCode = "unclassified"
)

// DrainMode is keep or kill, as in CTR-GW-05.
type DrainMode string

const (
	DrainKeep DrainMode = "keep"
	DrainKill DrainMode = "kill"
)

// Actor says where a configuration change came from.
type Actor string

const (
	ActorFile    Actor = "file"
	ActorControl Actor = "control"
)

// EventType is the "type" of a stream event, which is also the SSE event field.
type EventType string

const (
	EventHello     EventType = "hello"
	EventReset     EventType = "reset"
	EventStatus    EventType = "status"
	EventPeer      EventType = "peer"
	EventCall      EventType = "call"
	EventSession   EventType = "session"
	EventWarning   EventType = "warning"
	EventConfig    EventType = "config"
	EventLifecycle EventType = "lifecycle"
)

// Transport is how the endpoint in control.json is reached.
type Transport string

const (
	TransportUnix Transport = "unix"
	TransportPipe Transport = "pipe"
)
