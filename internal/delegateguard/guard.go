// Package delegateguard tracks active delegated task process trees and identifies
// local connections originating from them so delegated tasks cannot re-delegate.
package delegateguard

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"
)

type contextKey struct{}

type peerContext struct {
	pid       int
	hasPID    bool
	delegated bool
}

type trackedRoot struct {
	pid    int
	birth  string
	handle uintptr
}

// Guard tracks active delegated process trees (including any exec/pty children
// spawned on behalf of a delegated caller) and answers whether a local PID,
// socket connection, or HTTP request belongs to a delegated task.
type Guard struct {
	mu         sync.RWMutex
	starting   int
	startCh    chan struct{}
	roots      map[int]*trackedRoot
	pids       map[int]string
	pgids      map[int]bool
	sids       map[uintptr]bool
	handles    map[uintptr]int
	sampling   bool
	stopSample chan struct{}

	selfPID  int
	selfPGID int
	selfSID  uintptr
}

// New creates a new Guard for one node/Agent instance.
func New() *Guard {
	pid := os.Getpid()
	pgid, sid := selfProcessGroupAndSession(pid)
	return &Guard{
		startCh:  make(chan struct{}),
		roots:    map[int]*trackedRoot{},
		pids:     map[int]string{},
		pgids:    map[int]bool{},
		sids:     map[uintptr]bool{},
		handles:  map[uintptr]int{},
		selfPID:  pid,
		selfPGID: pgid,
		selfSID:  sid,
	}
}

// Active reports whether any delegated process tree is currently registered or starting.
func (g *Guard) Active() bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.starting > 0 || len(g.roots) > 0 || len(g.handles) > 0
}

// BeginStart marks that a delegated process is about to be spawned so concurrent
// local connection checks wait briefly for Track to register its PID/job.
func (g *Guard) BeginStart() func() {
	if g == nil {
		return func() {}
	}
	g.mu.Lock()
	g.starting++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if g.starting > 0 {
				g.starting--
			}
			if g.starting == 0 {
				close(g.startCh)
				g.startCh = make(chan struct{})
			}
			g.mu.Unlock()
		})
	}
}

func (g *Guard) waitStarting(limit time.Duration) {
	if g == nil {
		return
	}
	g.mu.RLock()
	if g.starting == 0 {
		g.mu.RUnlock()
		return
	}
	ch := g.startCh
	g.mu.RUnlock()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
	}
}

// Track registers a delegated root process (and optional platform handle such as
// a Windows Job Object handle). The returned untrack function must be called
// before the handle is closed and after the process tree has finished.
func (g *Guard) Track(pid int, handle uintptr) func() {
	if g == nil || (pid <= 0 && handle == 0) {
		return func() {}
	}
	g.mu.Lock()
	if handle != 0 {
		g.handles[handle]++
	}
	if pid > 0 && pid != g.selfPID {
		root := &trackedRoot{pid: pid, handle: handle}
		g.roots[pid] = root
		g.refreshLocked()
		g.ensureSamplerLocked()
	}
	g.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if handle != 0 {
				if g.handles[handle]--; g.handles[handle] <= 0 {
					delete(g.handles, handle)
				}
			}
			if pid > 0 {
				delete(g.roots, pid)
			}
			if len(g.roots) == 0 {
				g.pids = map[int]string{}
				g.pgids = map[int]bool{}
				g.sids = map[uintptr]bool{}
				if g.sampling && g.stopSample != nil {
					close(g.stopSample)
					g.sampling = false
					g.stopSample = nil
				}
			}
			g.mu.Unlock()
		})
	}
}

func (g *Guard) ensureSamplerLocked() {
	if g.sampling || !supportsTreeSampling() {
		return
	}
	stop := make(chan struct{})
	g.sampling = true
	g.stopSample = stop
	go func() {
		ticker := time.NewTicker(15 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				g.mu.Lock()
				if len(g.roots) == 0 {
					g.mu.Unlock()
					return
				}
				g.refreshLocked()
				g.mu.Unlock()
			}
		}
	}()
}

// IsDelegatedPID reports whether pid belongs to any active delegated process tree.
func (g *Guard) IsDelegatedPID(pid int) bool {
	if g == nil || pid <= 0 || pid == g.selfPID {
		return false
	}
	g.waitStarting(250 * time.Millisecond)
	g.mu.RLock()
	active := len(g.roots) > 0 || len(g.handles) > 0
	g.mu.RUnlock()
	if !active {
		return false
	}
	return g.isDelegatedPID(pid)
}

// IsDelegatedConn reports whether c originates from an active delegated process tree.
func (g *Guard) IsDelegatedConn(c net.Conn) bool {
	if g == nil || c == nil {
		return false
	}
	g.waitStarting(250 * time.Millisecond)
	g.mu.RLock()
	active := len(g.roots) > 0 || len(g.handles) > 0
	g.mu.RUnlock()
	if !active {
		return false
	}
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	if pid, ok := connPeerPID(c); ok {
		return g.isDelegatedPID(pid)
	}
	if tcp, ok := c.(*net.TCPConn); ok {
		return g.isDelegatedTCP(tcp.LocalAddr().String(), tcp.RemoteAddr().String())
	}
	return false
}

// ConnContext returns an http.Server ConnContext hook that binds peer process
// identity (and delegation status) to every accepted connection.
func ConnContext(g *Guard) func(context.Context, net.Conn) context.Context {
	return func(ctx context.Context, c net.Conn) context.Context {
		if g == nil || c == nil {
			return ctx
		}
		if tc, ok := c.(*tls.Conn); ok {
			c = tc.NetConn()
		}
		info := peerContext{}
		if pid, ok := connPeerPID(c); ok {
			info.pid = pid
			info.hasPID = true
			if g.IsDelegatedPID(pid) {
				info.delegated = true
			}
		} else if g.IsDelegatedConn(c) {
			info.delegated = true
		}
		return context.WithValue(ctx, contextKey{}, info)
	}
}

// FromContext reports whether ConnContext marked the connection as delegated.
func FromContext(ctx context.Context) bool {
	info, ok := ctx.Value(contextKey{}).(peerContext)
	return ok && info.delegated
}

// IsDelegatedHTTPRequest reports whether r arrived from a delegated process tree.
// It uses ConnContext metadata when available and falls back to live socket
// inspection (for servers that do not configure ConnContext, such as httptest).
func IsDelegatedHTTPRequest(g *Guard, r *http.Request) bool {
	if g == nil || r == nil {
		return false
	}
	if info, ok := r.Context().Value(contextKey{}).(peerContext); ok {
		if info.delegated {
			return true
		}
		if info.hasPID {
			return g.IsDelegatedPID(info.pid)
		}
		// If ConnContext ran on a TCP conn while no delegate tasks existed, it could not
		// have come from a delegated task, unless another check is needed; fall through
		// only if not already ruled out? Wait: if a TCP conn was opened while g.Active()
		// was true, IsDelegatedConn already caught it. Still, checking live TCP when
		// !info.hasPID and g.Active() is safe because non-delegated connections won't
		// match any delegated PID.
	}
	g.waitStarting(250 * time.Millisecond)
	g.mu.RLock()
	active := len(g.roots) > 0 || len(g.handles) > 0
	g.mu.RUnlock()
	if !active {
		return false
	}
	localAddr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if localAddr == nil {
		return false
	}
	return g.isDelegatedTCP(localAddr.String(), r.RemoteAddr)
}

func parseTCPPair(localAddr, remoteAddr string) (server, client netip.AddrPort, ok bool) {
	s, err1 := netip.ParseAddrPort(localAddr)
	c, err2 := netip.ParseAddrPort(remoteAddr)
	if err1 != nil || err2 != nil {
		return netip.AddrPort{}, netip.AddrPort{}, false
	}
	s = netip.AddrPortFrom(s.Addr().Unmap(), s.Port())
	c = netip.AddrPortFrom(c.Addr().Unmap(), c.Port())
	if !isLocalIP(c.Addr()) && !isLocalIP(s.Addr()) && c.Addr() != s.Addr() {
		return netip.AddrPort{}, netip.AddrPort{}, false
	}
	return s, c, true
}

func isLocalIP(addr netip.Addr) bool {
	if addr.IsLoopback() {
		return true
	}
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, ifa := range ifaces {
		var ip net.IP
		switch v := ifa.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil {
			continue
		}
		if parsed, ok := netip.AddrFromSlice(ip); ok && parsed.Unmap() == addr {
			return true
		}
	}
	return false
}
