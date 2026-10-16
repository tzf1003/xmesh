//go:build windows

package control

import (
	"context"
	"fmt"
	"net"

	winio "github.com/tailscale/go-winio"
	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

func dial(ctx context.Context, e Endpoint) (net.Conn, error) {
	if e.Transport != v1.TransportPipe {
		return nil, fmt.Errorf("此平台不支持传输 %s", e.Transport)
	}
	return winio.DialPipeContext(ctx, e.Address())
}
