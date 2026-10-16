//go:build !linux && !darwin && !windows

package control

import (
	"fmt"
	"net"
)

// Other Unix systems have no peer-credential support wired in; the listener then
// rejects every connection rather than rely on file permissions alone.
func peerUID(*net.UnixConn) (uint32, error) {
	return 0, fmt.Errorf("此平台无法核对对端身份")
}
