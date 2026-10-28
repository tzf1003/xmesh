// Package mesh provides authenticated peer transport and device discovery.
package mesh

import (
	"context"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/identity"
	"net"
	"net/http"
	"time"
)

const AgentPort = 7433

type Peer struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Address         string          `json:"address"`
	Tags            []string        `json:"tags"`
	Online          bool            `json:"online"`
	OS              string          `json:"os"`
	Relay           string          `json:"relay,omitempty"`
	Direct          bool            `json:"direct"`
	Fingerprint     string          `json:"fingerprint,omitempty"`
	Paths           []device.Path   `json:"paths,omitempty"`
	ActivePath      device.PathKind `json:"active_path,omitempty"`
	ConnectionState string          `json:"connection_state,omitempty"`
	// ActiveDirection says which end dialed the active connection: "outgoing" or "incoming".
	ActiveDirection string `json:"active_direction,omitempty"`
	// PeerQuality is the device's own report of the paths it dials to this end, for display.
	PeerQuality []device.PeerQuality `json:"peer_quality,omitempty"`
	// Links lists the carriers to the device, the one this end dialed first.
	Links       []Link `json:"links,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Link is one carrier to a device and the path it runs on.
type Link struct {
	Kind    device.PathKind `json:"kind"`
	Address string          `json:"address"`
	// Direction says which end dialed it: "outgoing" (this end is the client) or "incoming" (the server).
	Direction string `json:"direction"`
	// Draining marks a carrier about to close because a newer one took its slot.
	Draining bool `json:"draining,omitempty"`
	// Suspect marks a carrier that has gone quiet; its sessions prefer another carrier.
	Suspect  bool      `json:"suspect,omitempty"`
	Sessions int       `json:"sessions"`
	Since    time.Time `json:"since"`
}
type Network interface {
	Listen(context.Context) (net.Listener, error)
	Peers(context.Context) ([]Peer, error)
	Authenticate(*http.Request) (identity.Principal, error)
	HTTPClient(Peer) *http.Client
	Self(context.Context) (identity.Principal, error)
	Close() error
}

// Notifier is implemented by networks that learn about peers from events (LAN announcements and
// goodbyes), so a consumer need not wait for its next poll. Each receive means "Peers may have changed".
// Signals coalesce: a consumer that is busy sees at most one pending signal.
type Notifier interface {
	Changed() <-chan struct{}
}

// Presence is implemented by networks that can say which nodes are online now.
// authoritative is true when every node on the network is listed, so a missing
// node has left; otherwise a missing node is only unobserved (CTR-ID-01).
type Presence interface {
	Online(context.Context) (online map[string]bool, authoritative bool, err error)
}
