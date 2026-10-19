package connectivity

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/device"
	"tailscale.com/tsnet"
)

// MultiPath marks a runtime whose independently enabled Tailnet provider can become ready
// after its local gateway starts. ListenPort retains its accept loop across provider restarts.
func (n *Node) MultiPath() bool { return true }

type portListener struct {
	ctx      context.Context
	cancel   context.CancelFunc
	port     int
	funnel   bool
	ready    bool // guarded by the owning Node's mu
	err      string
	incoming chan net.Conn
	done     chan struct{}
	once     sync.Once
}

func (l *portListener) Accept() (net.Conn, error) {
	select {
	case <-l.ctx.Done():
		return nil, net.ErrClosed
	case conn := <-l.incoming:
		return conn, nil
	}
}
func (l *portListener) Addr() net.Addr { return &net.TCPAddr{Port: l.port} }
func (l *portListener) Close() error   { l.once.Do(l.cancel); <-l.done; return nil }

func (n *Node) ListenPort(port int) (net.Listener, error) {
	return n.listenTailnetPort(port, false)
}

func (n *Node) listenTailnetPort(port int, funnel bool) (net.Listener, error) {
	if port < 1 || port > 65535 || port == 7433 {
		return nil, fmt.Errorf("Gateway 端口不可用")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, net.ErrClosed
	}
	ctx, cancel := context.WithCancel(n.ctx)
	l := &portListener{ctx: ctx, cancel: cancel, port: port, funnel: funnel, incoming: make(chan net.Conn), done: make(chan struct{})}
	if n.listeners == nil {
		n.listeners = map[*portListener]bool{}
	}
	n.listeners[l] = true
	n.workers.Go(func() {
		defer close(l.done)
		defer func() { n.mu.Lock(); delete(n.listeners, l); n.mu.Unlock() }()
		n.runGatewayListener(l)
	})
	return l, nil
}

func (n *Node) runGatewayListener(l *portListener) {
	for l.ctx.Err() == nil {
		n.mu.Lock()
		p := n.providers[device.Tailnet]
		var server *tsnet.Server
		if p != nil {
			server = p.ts
		}
		n.mu.Unlock()
		var listener net.Listener
		var listenError error
		if server != nil {
			if l.funnel {
				listener, listenError = server.ListenFunnel("tcp", fmt.Sprintf(":%d", l.port))
			} else {
				listener, listenError = server.Listen("tcp", fmt.Sprintf(":%d", l.port))
			}
		}
		n.mu.Lock()
		current := p != nil && n.providers[device.Tailnet] == p && p.ts == server && n.cfg.TailnetEnabled()
		l.ready, l.err = current && listener != nil, ""
		if current && listenError != nil {
			l.err = listenError.Error()
		}
		n.mu.Unlock()
		if !current && listener != nil {
			_ = listener.Close()
			listener = nil
		}
		if listener != nil {
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					select {
					case l.incoming <- conn:
					case <-l.ctx.Done():
						_ = conn.Close()
						return
					}
				}
			}()
			select {
			case <-l.ctx.Done():
			case <-done:
			}
			_ = listener.Close()
			<-done
			n.mu.Lock()
			l.ready = false
			n.mu.Unlock()
		}
		select {
		case <-l.ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}
