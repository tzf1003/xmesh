// Package control is the node's local control channel (docs/contracts/control.md).
//
// It serves the control API v2 (api/control/v2) as HTTP/1.1 over a Unix domain socket or a
// Windows named pipe, reachable only by the user the kernel runs as. It is deliberately not a TCP
// listener and not an MCP tool: cloud clients and other devices never see it.
package control

import (
	"context"
	"encoding/json"
	"net/http"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// Host is what the control server needs from the running node. The app package
// implements it, so this package imports no role packages.
type Host interface {
	// Status returns the node's own part of StatusResponse: identity, roles, limits,
	// backends, gateway and ingress, plus the warnings the node itself can tell. The server
	// fills control, lifecycle, started_at, pid, drain, paths and peers, and adds the
	// warnings it observes (peer_offline, audit_dropped).
	Status() v2.StatusResponse
	Draining() bool
	Peers(context.Context) ([]v2.Peer, error)
	Sessions() v2.SessionsResponse
	// Drain stops accepting new resource calls; kill also ends PTYs and releases leases and
	// returns how many it ended.
	Drain(kill bool) (closedPTY, releasedLeases int)
	// Resume accepts new resource calls again.
	Resume()
}

type RemoteDrainer interface {
	RemoteDrain(context.Context, v2.DrainRequest) ([]v2.DrainOutcome, error)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", v2.ContentTypeJSON)
	h.Set(v2.HeaderVersion, v2.APIVersion)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes the ControlError body with the HTTP status the contract assigns to the code.
func fail(w http.ResponseWriter, e *v2.Error) {
	if e.Code == v2.ErrUnavailable && e.RetryAfterMS == nil {
		retry := int64(1000)
		e.RetryAfterMS = &retry
	}
	writeJSON(w, e.HTTPStatus(), e)
}

func newError(code v2.ErrorCode, message string) *v2.Error {
	return &v2.Error{Code: code, Message: message}
}

func argError(field, message string) *v2.Error {
	e := newError(v2.ErrInvalidArgs, message)
	e.Field = field
	return e
}
