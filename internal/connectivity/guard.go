package connectivity

import (
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
)

const (
	guardPerIPPreauth = 8
	guardBackoffBase  = time.Second
	guardBackoffMax   = time.Minute
	guardForget       = 10 * time.Minute
)

// directGuard protects the public direct port before a peer has authenticated: an optional source allow list,
// a cap on connections still in the handshake, and a per-source backoff after connections that never got a request through.
type directGuard struct {
	now      func() time.Time
	allow    []netip.Prefix
	maxTotal int

	mu      sync.Mutex
	total   int
	sources map[netip.Addr]*guardSource
	conns   map[net.Conn]netip.Addr
}

type guardSource struct {
	pending   int
	failures  int
	blockedTo time.Time
	last      time.Time
}

func newDirectGuard(cfg config.Direct, now func() time.Time) *directGuard {
	g := &directGuard{now: now, maxTotal: cfg.PreauthLimit(), sources: map[netip.Addr]*guardSource{}, conns: map[net.Conn]netip.Addr{}}
	for _, entry := range cfg.AllowIPs {
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			g.allow = append(g.allow, prefix.Masked())
		} else if addr, err := netip.ParseAddr(entry); err == nil {
			g.allow = append(g.allow, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
		}
	}
	return g
}

func (g *directGuard) listener(l net.Listener) net.Listener { return &guardListener{Listener: l, g: g} }

type guardListener struct {
	net.Listener
	g *directGuard
}

func (l *guardListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.g.admit(conn) {
			return conn, nil
		}
		_ = conn.Close()
	}
}

func sourceAddr(conn net.Conn) (netip.Addr, bool) {
	tcp, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	addr, ok := netip.AddrFromSlice(tcp.IP)
	return addr.Unmap(), ok
}

func (g *directGuard) permitted(addr netip.Addr) bool {
	if len(g.allow) == 0 {
		return true
	}
	for _, prefix := range g.allow {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// admit decides at accept time, before any TLS bytes are read.
func (g *directGuard) admit(conn net.Conn) bool {
	addr, ok := sourceAddr(conn)
	if !ok || !g.permitted(addr) {
		return false
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.forgetLocked(now)
	s := g.sources[addr]
	if s == nil {
		s = &guardSource{}
		g.sources[addr] = s
	}
	if now.Before(s.blockedTo) || s.pending >= guardPerIPPreauth || g.total >= g.maxTotal {
		return false
	}
	s.pending++
	s.last = now
	g.total++
	g.conns[conn] = addr
	return true
}

// connState settles a connection once it either sent a request (the handshake worked) or closed without one.
func (g *directGuard) connState(conn net.Conn, state http.ConnState) {
	if state != http.StateActive && state != http.StateClosed && state != http.StateHijacked {
		return
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	addr, pending := g.conns[conn]
	if !pending {
		return
	}
	delete(g.conns, conn)
	g.total--
	s := g.sources[addr]
	s.pending--
	s.last = now
	if state == http.StateClosed {
		s.failures++
		backoff := guardBackoffBase << min(s.failures-1, 6)
		s.blockedTo = now.Add(min(backoff, guardBackoffMax))
		return
	}
	s.failures, s.blockedTo = 0, time.Time{}
}

func (g *directGuard) forgetLocked(now time.Time) {
	for addr, s := range g.sources {
		if s.pending == 0 && now.Sub(s.last) > guardForget {
			delete(g.sources, addr)
		}
	}
}
