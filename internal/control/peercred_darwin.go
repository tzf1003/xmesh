//go:build darwin

package control

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID reads LOCAL_PEERCRED, the credentials the peer had when it connected.
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Xucred
	var serr error
	if err = raw.Control(func(fd uintptr) { cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED) }); err != nil {
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
