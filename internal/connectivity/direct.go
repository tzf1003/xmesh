package connectivity

import (
	"context"
	"net"

	"github.com/tzf1003/xmesh/internal/device"
)

// runDirect serves the public listener. It has no discovery: peers come from pairing and mesh.direct.peers.
// The listener demands a paired client certificate except while an invite is open (see serve).
func (n *Node) runDirect(ctx context.Context, p *provider) error {
	listener, err := net.Listen("tcp", p.cfg.Direct.ListenAddr())
	if err != nil {
		return err
	}
	return n.serve(ctx, device.Direct, p, listener, func(ctx context.Context) { <-ctx.Done() })
}
