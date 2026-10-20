//go:build darwin || linux

package computer

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/tzf1003/xmesh/internal/localendpoint"
)

func DefaultEndpoint() string { return EndpointForState(DefaultStateDir()) }
func ValidEndpoint(endpoint string) bool {
	return filepath.IsAbs(endpoint) && !strings.ContainsRune(endpoint, 0)
}
func EndpointForState(state string) string {
	return localendpoint.UnixSocketPath(state, "computer", "/tmp", os.Geteuid())
}
func dialDesktop(ctx context.Context, endpoint string) (net.Conn, error) {
	if !ValidEndpoint(endpoint) {
		return nil, fmt.Errorf("desktop endpoint must be absolute")
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
	if err != nil {
		return nil, err
	}
	u, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("desktop endpoint is not local")
	}
	uid, err := peerUID(u)
	if err != nil || uid != uint32(os.Geteuid()) {
		_ = c.Close()
		return nil, fmt.Errorf("desktop endpoint identity mismatch")
	}
	return c, nil
}
