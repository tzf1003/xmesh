// Package onboarding owns the durable configuration workflow shared by the CLI and desktop.
package onboarding

import "time"

const SchemaVersion = 1

type Input struct {
	SchemaVersion int          `json:"schema_version"`
	Purpose       string       `json:"purpose"`
	NodeName      string       `json:"node_name"`
	Workspace     string       `json:"workspace"`
	Permission    string       `json:"permission"`
	Clients       []string     `json:"clients"`
	Autostart     *bool        `json:"autostart,omitempty"`
	Network       NetworkInput `json:"network"`
}

type NetworkInput struct {
	Mode        string `json:"mode"`
	ControlURL  string `json:"control_url,omitempty"`
	AuthMethod  string `json:"auth_method,omitempty"`
	AuthkeyFile string `json:"authkey_file,omitempty"`
}

type Step struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	Code       string `json:"code,omitempty"`
	NextAction string `json:"next_action,omitempty"`
}

type Plan struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	Input         Input          `json:"input"`
	ConfigPath    string         `json:"config_path"`
	Config        string         `json:"config"`
	BeforeHash    string         `json:"before_hash"`
	Endpoint      string         `json:"endpoint"`
	Summary       []string       `json:"summary"`
	ClientChanges []ClientChange `json:"client_changes"`
	Steps         []Step         `json:"steps"`
	CreatedAt     time.Time      `json:"created_at"`
}

type Readiness struct {
	Installed      bool `json:"installed"`
	ServiceReady   bool `json:"service_ready"`
	ClientVerified bool `json:"client_verified"`
}

// ServiceSnapshot is private recovery metadata for an existing registration.
type ServiceSnapshot struct {
	Enabled    string `json:"enabled,omitempty"`
	Installed  bool   `json:"installed"`
	Running    bool   `json:"running"`
	Definition []byte `json:"definition"`
	Path       string `json:"path"`
}

type Job struct {
	SchemaVersion         int              `json:"schema_version"`
	ID                    string           `json:"id"`
	PlanID                string           `json:"plan_id"`
	NodeName              string           `json:"node_name"`
	Purpose               string           `json:"purpose"`
	Status                string           `json:"status"`
	Steps                 []Step           `json:"steps"`
	Readiness             Readiness        `json:"readiness"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
	NextAction            string           `json:"next_action,omitempty"`
	AuthURL               string           `json:"auth_url,omitempty"`
	ControlURL            string           `json:"control_url,omitempty"`
	IngressURL            string           `json:"ingress_url,omitempty"`
	IngressPasswordSet    bool             `json:"ingress_password_set"`
	VerificationTool      string           `json:"verification_tool,omitempty"`
	VerificationPath      string           `json:"verification_path,omitempty"`
	Receipts              []ClientReceipt  `json:"-"`
	ConfigHash            string           `json:"-"`
	ConfigBackup          string           `json:"-"`
	PendingConfigHash     string           `json:"-"`
	PendingRestore        bool             `json:"-"`
	ServiceOwned          bool             `json:"-"`
	ServiceSnapshot       *ServiceSnapshot `json:"-"`
	ServiceStopped        bool             `json:"-"`
	ServiceRestorePending bool             `json:"-"`
}

type NodeInfo struct {
	Name       string `json:"name"`
	ConfigPath string `json:"config_path"`
	Running    bool   `json:"running"`
	Problem    string `json:"problem,omitempty"`
}

type Inspection struct {
	SchemaVersion     int          `json:"schema_version"`
	Platform          string       `json:"platform"`
	Arch              string       `json:"arch"`
	Hostname          string       `json:"hostname"`
	SuggestedNodeName string       `json:"suggested_node_name"`
	Root              string       `json:"root"`
	Clients           []ClientInfo `json:"clients"`
	Nodes             []NodeInfo   `json:"nodes"`
	Jobs              []Job        `json:"jobs"`
}

type Diagnostic struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	NextAction string `json:"next_action,omitempty"`
}

type Diagnostics struct {
	SchemaVersion int          `json:"schema_version"`
	Checks        []Diagnostic `json:"checks"`
}

type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Field      string `json:"field,omitempty"`
	NextAction string `json:"next_action,omitempty"`
}

func (e *Error) Error() string { return e.Message }

func problem(code, message, field string) error {
	return &Error{Code: code, Message: message, Field: field}
}
