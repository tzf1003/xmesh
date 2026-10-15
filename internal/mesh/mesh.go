// Package mesh provides authenticated peer transport and device discovery.
package mesh

import (
	"context"
	"github.com/tzf1003/xmesh/internal/identity"
	"net"
	"net/http"
)

const AgentPort = 7433

type Peer struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Address string   `json:"address"`
	Tags    []string `json:"tags"`
	Online  bool     `json:"online"`
	OS      string   `json:"os"`
	Relay   string   `json:"relay,omitempty"`
	Direct  bool     `json:"direct"`
}
type Network interface {
	Listen(context.Context) (net.Listener, error)
	Peers(context.Context) ([]Peer, error)
	Authenticate(*http.Request) (identity.Principal, error)
	HTTPClient() *http.Client
	Self(context.Context) (identity.Principal, error)
	Close() error
}
