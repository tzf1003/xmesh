package v2

// DelegateState is the node's delegation view behind GET /v2/delegate (API 2.7, control.md). It is read-only: the switch
// and the limits come from the configuration in force, and the runners, live sessions and tasks come from the delegation
// manager. It never carries a prompt, output or reply.
type DelegateState struct {
	Enabled       bool              `json:"enabled"`
	AllowWrite    bool              `json:"allow_write"`
	AllowSessions bool              `json:"allow_sessions"`
	MaxConcurrent int               `json:"max_concurrent"`
	Running       int               `json:"running"`
	Runners       []DelegateRunner  `json:"runners"`
	Sessions      []DelegateSession `json:"sessions"`
	Tasks         []DelegateTask    `json:"tasks"`
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
