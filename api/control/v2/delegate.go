package v2

// DelegateState is the node's delegation view behind GET /v2/delegate (API 2.7, 2.8, control.md). It is read-only: the switch
// and the limits come from the configuration in force, and the runners, live sessions and tasks come from the delegation
// manager. It never carries a prompt, output or reply.
type DelegateState struct {
	Enabled             bool              `json:"enabled"`
	AllowWrite          bool              `json:"allow_write"`
	AllowSessions       bool              `json:"allow_sessions"`
	AllowSessionReplies bool              `json:"allow_session_replies"`
	MaxConcurrent       int               `json:"max_concurrent"`
	Running             int               `json:"running"`
	PendingReplies      int               `json:"pending_replies"`
	Runners             []DelegateRunner  `json:"runners"`
	Sessions            []DelegateSession `json:"sessions"`
	Tasks               []DelegateTask    `json:"tasks"`
}

// DelegateRunner is one way to start a local Agent CLI (CTR-DLG-05). Path is set when Available is true.
type DelegateRunner struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
}

// DelegateSession is a live Claude Code session that accepts injected messages (CTR-DLG-04).
type DelegateSession struct {
	SessionID string `json:"session_id"`
	Runner    string `json:"runner"`
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Tier      string `json:"tier"`
}

// DelegateTask is one task of any caller (CTR-DLG-03). The control channel belongs to the node's own user, so unlike
// delegate.list it is not filtered by caller. It carries no prompt, output or reply.
type DelegateTask struct {
	TaskID     string `json:"task_id"`
	Caller     string `json:"caller"`
	Runner     string `json:"runner"`
	State      string `json:"state"`
	CWD        string `json:"cwd"`
	StartedAt  Time   `json:"started_at"`
	DurationMS int64  `json:"duration_ms"`
	SessionID  string `json:"session_id,omitempty"`
}

// DelegateReplyState is the local confirmation state of an explicit live-session reply draft (CTR-DLG-07).
type DelegateReplyState string

const (
	DelegateReplyPending   DelegateReplyState = "pending"
	DelegateReplyApproved  DelegateReplyState = "approved"
	DelegateReplyRejected  DelegateReplyState = "rejected"
	DelegateReplyExpired   DelegateReplyState = "expired"
	DelegateReplyCancelled DelegateReplyState = "cancelled"
)

// DelegateReplySummary is the metadata-only row of GET /v2/delegate/replies (API 2.8, CTR-DLG-07).
// It never carries the reply body.
type DelegateReplySummary struct {
	ReplyID   string             `json:"reply_id"`
	TaskID    string             `json:"task_id"`
	MessageID string             `json:"message_id"`
	SessionID string             `json:"session_id"`
	Caller    string             `json:"caller"`
	State     DelegateReplyState `json:"state"`
	Bytes     int                `json:"bytes"`
	Previewed bool               `json:"previewed"`
	CreatedAt Time               `json:"created_at"`
	ExpiresAt Time               `json:"expires_at"`
	DecidedAt *Time              `json:"decided_at,omitempty"`
}

// DelegateRepliesQuery filters GET /v2/delegate/replies by reply state.
type DelegateRepliesQuery struct {
	State DelegateReplyState `json:"state,omitempty"`
}

// DelegateRepliesResponse is the metadata-only reply confirmation queue behind GET /v2/delegate/replies.
type DelegateRepliesResponse struct {
	Replies []DelegateReplySummary `json:"replies"`
}

// DelegateReplyDetail is the per-item preview response behind GET /v2/delegate/replies/{reply_id}.
// Body is populated only while State is "pending".
type DelegateReplyDetail struct {
	ReplyID    string             `json:"reply_id"`
	TaskID     string             `json:"task_id"`
	MessageID  string             `json:"message_id"`
	SessionID  string             `json:"session_id"`
	Caller     string             `json:"caller"`
	State      DelegateReplyState `json:"state"`
	Bytes      int                `json:"bytes"`
	Previewed  bool               `json:"previewed"`
	Body       string             `json:"body,omitempty"`
	BodySHA256 string             `json:"body_sha256"`
	CreatedAt  Time               `json:"created_at"`
	ExpiresAt  Time               `json:"expires_at"`
	DecidedAt  *Time              `json:"decided_at,omitempty"`
}

// DelegateReplyCreateRequest is the body of POST /v2/delegate/replies, used by the live-session reply adapter.
type DelegateReplyCreateRequest struct {
	MessageID string `json:"message_id"`
	SessionID string `json:"session_id,omitempty"`
	Body      string `json:"body"`
}

// DelegateReplyApproveRequest is the optional body of POST /v2/delegate/replies/{reply_id}/approve.
type DelegateReplyApproveRequest struct {
	BodySHA256 string `json:"body_sha256,omitempty"`
}
