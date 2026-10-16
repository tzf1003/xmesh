//go:build !windows

package control

import (
	"context"
	"fmt"
	"net"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

func dial(ctx context.Context, e Endpoint) (net.Conn, error) {
	if e.Transport != v1.TransportUnix {
		return nil, fmt.Errorf("此平台不支持传输 %s", e.Transport)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", e.Address())
}
