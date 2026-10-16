//go:build !windows

package control

import (
	"context"
	"fmt"
	"net"
)

func dial(ctx context.Context, e Endpoint) (net.Conn, error) {
	if e.Transport != "unix" {
		return nil, fmt.Errorf("此平台不支持传输 %s", e.Transport)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", e.Address)
}
