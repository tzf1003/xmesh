//go:build linux

package control

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID reads SO_PEERCRED, the credentials the peer had when it connected.
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var serr error
	if err = raw.Control(func(fd uintptr) { cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	if cred == nil {
		return 0, fmt.Errorf("没有对端凭据")
	}
	return cred.Uid, nil
}
