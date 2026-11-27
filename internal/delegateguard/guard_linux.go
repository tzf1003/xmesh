//go:build linux

package delegateguard

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func selfSessionID(pid int) uintptr {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 4 {
		return 0
	}
	sid, err := strconv.Atoi(fields[3])
	if err != nil || sid <= 0 {
		return 0
	}
	return uintptr(sid)
}

func treeSnapshot() ([]procInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	result := make([]procInfo, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 20 || fields[0] == "Z" {
			continue
		}
		parent, _ := strconv.Atoi(fields[1])
		group, _ := strconv.Atoi(fields[2])
		sid, _ := strconv.Atoi(fields[3])
		result = append(result, procInfo{
			pid:    pid,
			parent: parent,
			pgid:   group,
			sid:    uintptr(max(0, sid)),
			birth:  fields[19],
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
	var cred *unix.Ucred
	var serr error
	if err = raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || serr != nil || cred == nil || cred.Pid <= 0 {
		return 0, false
	}
	return int(cred.Pid), true
}

func (g *Guard) isDelegatedTCP(localAddr, remoteAddr string) bool {
	server, client, ok := parseTCPPair(localAddr, remoteAddr)
	if !ok {
		return false
	}
	inode, ok := findTCPSocketInode(client, server)
	if !ok {
		return false
	}
	target := "socket:[" + inode + "]"

	g.mu.Lock()
	g.refreshLocked()
	pids := make([]int, 0, len(g.pids)+len(g.roots))
	seen := make(map[int]bool, len(g.pids)+len(g.roots))
	for pid := range g.roots {
		if !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	for pid := range g.pids {
		if !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	g.mu.Unlock()

	for _, pid := range pids {
		if pid == g.selfPID {
			continue
		}
		fdDir := fmt.Sprintf("/proc/%d/fd", pid)
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			link, err := os.Readlink(filepath.Join(fdDir, e.Name()))
			if err == nil && link == target {
				return true
			}
		}
	}
	return false
}

func findTCPSocketInode(local, remote netip.AddrPort) (string, bool) {
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			laddr, ok1 := parseProcAddrPort(fields[1])
			raddr, ok2 := parseProcAddrPort(fields[2])
			if !ok1 || !ok2 {
				continue
			}
			if laddr == local && raddr == remote && fields[9] != "0" {
				return fields[9], true
			}
		}
	}
	return "", false
}

func parseProcAddrPort(s string) (netip.AddrPort, bool) {
	ipHex, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	port64, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	raw, err := hex.DecodeString(ipHex)
	if err != nil {
		return netip.AddrPort{}, false
	}
	switch len(raw) {
	case 4:
		v := binary.NativeEndian.Uint32(raw)
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		return netip.AddrPortFrom(netip.AddrFrom4(b), uint16(port64)), true
	case 16:
		var b [16]byte
		for off := 0; off < 16; off += 4 {
			v := binary.NativeEndian.Uint32(raw[off : off+4])
			binary.BigEndian.PutUint32(b[off:off+4], v)
		}
		return netip.AddrPortFrom(netip.AddrFrom16(b).Unmap(), uint16(port64)), true
	default:
		return netip.AddrPort{}, false
	}
}
