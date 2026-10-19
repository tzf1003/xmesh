//go:build windows

package control

import (
	"context"
	"fmt"
	"net"

	winio "github.com/tailscale/go-winio"
	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

func dial(ctx context.Context, e Endpoint) (net.Conn, error) {
	if e.Transport != v2.TransportPipe {
		return nil, fmt.Errorf("此平台不支持传输 %s", e.Transport)
	}
	return winio.DialPipeContext(ctx, e.Address())
}
