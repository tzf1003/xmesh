package connectivity

import (
	"context"
	"errors"
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
		budget := 5 * time.Second
		if path.Kind == device.LAN {
			budget = 2 * time.Second
		}
		attempt, stop := context.WithTimeout(ctx, budget)
		client, err := n.httpClient(path.Kind, peer.ID, false)
		if err == nil {
			target := peer
			target.Address = path.Address
			if req.Scope == "device" {
				req.Device = peer.ID
			}
			err = agent.SendDrain(attempt, client, target, req)
			client.CloseIdleConnections()
		}
		stop()
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
