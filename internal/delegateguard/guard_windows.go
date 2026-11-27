//go:build windows

package delegateguard

import (
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
	"tailscale.com/net/netstat"
)

var (
	modKernel32    = windows.NewLazySystemDLL("kernel32.dll")
	isProcessInJob = modKernel32.NewProc("IsProcessInJob")
)

func supportsTreeSampling() bool { return false }

func selfProcessGroupAndSession(int) (int, uintptr) { return 0, 0 }

// refreshLocked is a no-op on Windows because Job Objects enforce tree membership in the kernel.
func (g *Guard) refreshLocked() []struct{} { return nil }

func connPeerPID(c net.Conn) (int, bool) {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return 0, false
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(f.Fd()), &pid); err != nil || pid == 0 {
		return 0, false
	}
	return int(pid), true
}

func (g *Guard) isDelegatedPID(pid int) bool {
	if pid <= 0 || pid == g.selfPID {
		return false
	}
	g.mu.RLock()
	if _, ok := g.roots[pid]; ok {
		g.mu.RUnlock()
		return true
	}
	handles := make([]uintptr, 0, len(g.handles))
	for h := range g.handles {
		handles = append(handles, h)
	}
	if len(handles) == 0 && len(g.roots) == 0 {
		g.mu.RUnlock()
		return false
	}
	// Check job membership while holding RLock so Finish cannot close any job handle concurrently.
	if len(handles) > 0 {
		proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err == nil {
			defer windows.CloseHandle(proc)
			for _, h := range handles {
				var member int32
				if ok, _, _ := isProcessInJob.Call(uintptr(proc), h, uintptr(unsafe.Pointer(&member))); ok != 0 && member != 0 {
					g.mu.RUnlock()
					return true
				}
			}
		}
	}
	roots := make(map[int]bool, len(g.roots))
	for r := range g.roots {
		roots[r] = true
	}
	g.mu.RUnlock()

	if len(roots) > 0 {
		return isDescendantSnapshotWindows(pid, g.selfPID, roots)
	}
	return false
}

func isDescendantSnapshotWindows(pid, selfPID int, roots map[int]bool) bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	parents := map[int]int{}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		parents[int(entry.ProcessID)] = int(entry.ParentProcessID)
	}
	cur := pid
	for steps := 0; steps < len(parents)+1 && cur > 0 && cur != selfPID; steps++ {
		if roots[cur] {
			return true
		}
		parent, ok := parents[cur]
		if !ok || parent == cur {
			break
		}
		cur = parent
	}
	return false
}

func (g *Guard) isDelegatedTCP(localAddr, remoteAddr string) bool {
	server, client, ok := parseTCPPair(localAddr, remoteAddr)
	if !ok {
		return false
	}
	table, err := netstat.Get()
	if err != nil {
		return false
	}
	for _, entry := range table.Entries {
		l := netip.AddrPortFrom(entry.Local.Addr().Unmap(), entry.Local.Port())
		r := netip.AddrPortFrom(entry.Remote.Addr().Unmap(), entry.Remote.Port())
		if l == client && r == server {
			return g.isDelegatedPID(entry.Pid)
		}
	}
	return false
}
