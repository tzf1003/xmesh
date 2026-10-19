package connectivity

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

// SendDrain uses authenticated device paths. Drain is idempotent, so a lost
// response may be retried through the other provider without repeating work.
func (n *Node) SendDrain(ctx context.Context, peer mesh.Peer, req agent.DrainRequest) error {
	devices, _ := n.directory.Snapshot(time.Now())
	var paths []device.Path
	for _, d := range devices {
		if d.DeviceID == peer.ID && !d.IdentityConflict {
			paths = d.Paths
			break
		}
	}
	var failures []error
	for _, path := range paths {
		if !path.Reachable {
			continue
		}

		client, err := n.httpClient(path.Kind, peer.ID, false)
		if err == nil {
			// Dial/TLS setup remains bounded by the transport. The response uses
			// the administrator's per-device deadline, including long drain work.
			configureDrainTransport(client.Transport.(*http.Transport), path.Kind)
			target := peer
			target.Address = path.Address
			if req.Scope == "device" {
				req.Device = peer.ID
			}
			err = agent.SendDrain(ctx, client, target, req)
			client.CloseIdleConnections()
		}
		if err == nil {
			return nil
		}
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return errors.New("设备没有已验证的可用路径")
	}
	return errors.Join(failures...)
}

func configureDrainTransport(transport *http.Transport, kind device.PathKind) {
	transport.ResponseHeaderTimeout = 0
	budget := 5 * time.Second
	if kind == device.LAN {
		budget = 2 * time.Second
	}
	dial := transport.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		attempt, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		return dial(attempt, network, address)
	}
}
