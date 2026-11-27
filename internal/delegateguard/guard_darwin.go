//go:build darwin

package delegateguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func selfSessionID(pid int) uintptr {
	all, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return 0
	}
	for _, p := range all {
		if int(p.Proc.P_pid) == pid {
			return p.Eproc.Sess
		}
	}
	return 0
}

func treeSnapshot() ([]procInfo, error) {
	all, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	result := make([]procInfo, 0, len(all))
	for _, p := range all {
		if p.Proc.P_stat == 5 {
			continue
		}
		result = append(result, procInfo{
			pid:    int(p.Proc.P_pid),
			parent: int(p.Eproc.Ppid),
			pgid:   int(p.Eproc.Pgid),
			sid:    p.Eproc.Sess,
			birth:  fmt.Sprintf("%d:%d", p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec),
		})
	}
	return result, nil
}

func connPeerPID(c net.Conn) (int, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var pid int
	var serr error
	if err = raw.Control(func(fd uintptr) {
		pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil || serr != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func (g *Guard) isDelegatedTCP(localAddr, remoteAddr string) bool {
	server, client, ok := parseTCPPair(localAddr, remoteAddr)
	if !ok {
		return false
	}
	lsofPath := "/usr/sbin/lsof"
	if _, err := exec.LookPath(lsofPath); err != nil {
		found, err := exec.LookPath("lsof")
		if err != nil {
			return false
		}
		lsofPath = found
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, lsofPath, "-n", "-P", fmt.Sprintf("-iTCP:%d", server.Port()), "-F", "pn").Output()
	if err != nil && len(out) == 0 {
		return false
	}
	curPID := 0
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			curPID, _ = strconv.Atoi(line[1:])
		case 'n':
			if curPID <= 0 || curPID == g.selfPID {
				continue
			}
			lhs, rhs, hasArrow := strings.Cut(line[1:], "->")
			if !hasArrow {
				continue
			}
			l, err1 := netip.ParseAddrPort(lhs)
			r, err2 := netip.ParseAddrPort(rhs)
			if err1 != nil || err2 != nil {
				continue
			}
			l = netip.AddrPortFrom(l.Addr().Unmap(), l.Port())
			r = netip.AddrPortFrom(r.Addr().Unmap(), r.Port())
			if l == client && r == server {
				return g.isDelegatedPID(curPID)
			}
		}
	}
	return false
}
