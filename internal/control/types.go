// Package control is the node's local control channel (docs/design/12-ui-and-control.md).
//
// It serves a JSON/HTTP API over a Unix domain socket or a Windows named pipe,
// reachable only by the user the kernel runs as. It is deliberately not a TCP
// listener and not an MCP tool: cloud clients and other devices never see it.
package control

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

// APIVersion is the major version of the control API, sent as X-Xmesh-Control-Version
// and recorded in control.json. The API only grows within a major version.
const APIVersion = "1"

const VersionHeader = "X-Xmesh-Control-Version"

type Network struct {
	Mode string   `json:"mode"`
	Node string   `json:"node_id"`
	Tags []string `json:"tags"`
}
type Backend struct {
	Name     string `json:"name"`
	Policy   string `json:"policy"`
	Favorite bool   `json:"favorite"`
	State    string `json:"state"`
}
type Workspace struct {
	Alias string `json:"alias"`
	Desc  string `json:"desc,omitempty"`
	Path  string `json:"path,omitempty"`
}

// Status is a superset of sys.info for the node's own operator.
type Status struct {
	Node            string         `json:"node"`
	Roles           []string       `json:"roles"`
	Version         string         `json:"version"`
	ControlAPI      string         `json:"control_api"`
	OS              string         `json:"os"`
	Arch            string         `json:"arch"`
	PID             int            `json:"pid"`
	StartedAt       string         `json:"started_at"`
	Health          string         `json:"health"`
	Draining        bool           `json:"draining"`
	RestartRequired bool           `json:"restart_required"`
	ReloadFailures  uint64         `json:"policy_reload_failures"`
	Warnings        []string       `json:"warnings"`
	Network         *Network       `json:"network,omitempty"`
	Limits          map[string]any `json:"limits,omitempty"`
	Backends        []Backend      `json:"backends"`
	Workspaces      []Workspace    `json:"workspaces"`
}
type Peer struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Address     string   `json:"address,omitempty"`
	Tags        []string `json:"tags"`
	Online      bool     `json:"online"`
	OS          string   `json:"os,omitempty"`
	Direct      bool     `json:"direct"`
	Relay       string   `json:"relay,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
}
type Peers struct {
	Mode  string `json:"mode"`
	Peers []Peer `json:"peers"`
}
type BackendLease struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Owner string `json:"owner,omitempty"`
}
type Sessions struct {
	PTY      []map[string]any `json:"pty"`
	Exec     map[string]int   `json:"exec"`
	Backends []BackendLease   `json:"backends"`
}

// Host is what the control server needs from the running node. The app package
// implements it, so this package imports no role packages.
type Host interface {
	Status() Status
	Peers(context.Context) (Peers, error)
	Sessions() Sessions
	// Drain stops accepting new resource calls; kill also ends PTYs and releases leases.
	Drain(kill bool)
	// Resume accepts new resource calls again.
	Resume()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(VersionHeader, APIVersion)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes a CTR-ERR-01 envelope with an HTTP status that matches its code.
func fail(w http.ResponseWriter, status int, e *toolerr.Error) {
	writeJSON(w, status, e.Object())
}
