package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/gateway"
)

// gatewayRegistry holds the gateways of this node, the local one and the ingress's. A drain asked
// for before a gateway exists applies to it as it is added, so a gateway that starts later in the
// life of the process starts out in the same state as the others.
type gatewayRegistry struct {
	mu      sync.Mutex
	list    []*gateway.Gateway
	pending map[string]bool
}

func newGatewayRegistry() *gatewayRegistry {
	return &gatewayRegistry{pending: map[string]bool{}}
}

func (r *gatewayRegistry) add(g *gateway.Gateway) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.pending {
		g.Drain(name)
	}
	r.list = append(r.list, g)
}

func (r *gatewayRegistry) remove(g *gateway.Gateway) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, have := range r.list {
		if have == g {
			r.list = append(r.list[:i:i], r.list[i+1:]...)
			return
		}
	}
}

func (r *gatewayRegistry) all() []*gateway.Gateway {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*gateway.Gateway{}, r.list...)
}

// drain marks name as draining on every gateway, including the ones added later.
func (r *gatewayRegistry) drain(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[name] = true
	current := append([]*gateway.Gateway{}, r.list...)
	for _, g := range current {
		g.Drain(name)
	}
}

func (r *gatewayRegistry) draining(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending[name] || r.pending[""]
}

// resume undoes drain for the names.
func (r *gatewayRegistry) resume(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		delete(r.pending, name)
	}
	current := append([]*gateway.Gateway{}, r.list...)
	for _, g := range current {
		for _, name := range names {
			g.Resume(name)
		}
	}
}

// gatewayDeps is what the local gateway needs from the rest of the process.
type gatewayDeps struct {
	store     *config.Store
	registry  *gatewayRegistry
	log       *audit.Logger
	network   mesh.Network
	nodeID    string
	localPeer mesh.Peer
	discover  gateway.Discover
	changed   <-chan struct{}
	dial      gateway.Dial
	// mode is the mesh mode of the running process, which a configuration change cannot alter.
	mode string
	// serveFailed hears about an accept loop that ended on its own.
	serveFailed func(error)
}

// gatewayRole is the local gateway with the listeners that serve it. The role can be started and
// stopped, and its listen address changed, while the agent, the network and the PTY and exec
// sessions go on: the sessions live in the agent, and the gateway is only the way in.
type gatewayRole struct {
	deps    gatewayDeps
	gw      *gateway.Gateway
	server  *http.Server
	wg      sync.WaitGroup
	mu      sync.Mutex
	listen  string
	bound   []net.Listener
	retired map[net.Listener]bool
}

// startGatewayRole creates the gateway and binds gateway.listen from cfg. On failure nothing is left
// running.
func startGatewayRole(deps gatewayDeps, cfg *config.Config) (*gatewayRole, error) {
	g := gateway.New(gateway.Options{Quota: cfg.Gateway.Quota, NodeName: cfg.Node.Name, NodeID: deps.nodeID, LocalPeer: deps.localPeer, Favorite: cfg.Gateway.ExposeBackends == "favorite", Discover: deps.discover, Changed: deps.changed, Dial: deps.dial, Audit: deps.log})
	r := &gatewayRole{deps: deps, gw: g, retired: map[net.Listener]bool{}}
	r.server = &http.Server{Handler: g.HTTP(gateway.LocalAuthentication(deps.store)), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, ErrorLog: newHandshakeLog()}
	bound, err := r.bind(cfg.Gateway.Listen)
	if err != nil {
		g.Close()
		return nil, err
	}
	deps.registry.add(g)
	// Read the generation after registering, so a change landing between the two is either seen here
	// or delivered to the subscription.
	r.reconfigure(deps.store.Get())
	r.mu.Lock()
	r.listen = cfg.Gateway.Listen
	r.serve(bound)
	r.mu.Unlock()
	return r, nil
}

// bind opens the listeners gateway.listen asks for: the OS listener, and in tailnet mode the tailnet
// one, which the tsnet netstack serves rather than the OS (CON-NET). Nothing is served yet.
func (r *gatewayRole) bind(listen string) ([]net.Listener, error) {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	out := []net.Listener{listener}
	multiPath := false
	if network, ok := r.deps.network.(interface{ MultiPath() bool }); ok {
		multiPath = network.MultiPath()
	}
	if tn, ok := r.deps.network.(tailnetGatewayListener); ok && (r.deps.mode == "tailnet" || multiPath) && !gatewayLoopbackOnly(listen) {
		tailnet, err := tn.ListenPort(gatewayPort(listen))
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
		out = append(out, tailnet)
	}
	return out, nil
}

// serve starts the accept loops for listeners. The caller holds r.mu.
func (r *gatewayRole) serve(listeners []net.Listener) {
	for _, l := range listeners {
		r.bound = append(r.bound, l)
		label := "Gateway MCP"
		if len(listeners) > 1 && l == listeners[1] {
			label = "Gateway MCP (tailnet)"
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			err := r.server.Serve(l)
			r.mu.Lock()
			deliberate := r.retired[l]
			r.mu.Unlock()
			if err != nil && !deliberate && !errors.Is(err, http.ErrServerClosed) && r.deps.serveFailed != nil {
				r.deps.serveFailed(err)
			}
		}()
		fmt.Fprintln(os.Stderr, label+": "+l.Addr().String()+"/mcp")
	}
}

// retire stops accepting on the listeners now serving. Connections already open stay with the server
// until they end. The caller holds r.mu.
func (r *gatewayRole) retire() []net.Listener {
	old := r.bound
	r.bound = nil
	for _, l := range old {
		r.retired[l] = true
		_ = l.Close()
	}
	return old
}

// rebind moves the gateway to listen. The new address is bound before the old one is given up, so a
// failed bind leaves the gateway where it was. When the old listener itself holds the port, as when
// 127.0.0.1:8080 becomes :8080 on a system that does not allow both, the old one is closed first and
// taken back if the new bind still fails.
func (r *gatewayRole) rebind(listen string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if listen == r.listen {
		return nil
	}
	next, err := r.bind(listen)
	if err != nil && addressInUse(err) && gatewayPort(listen) == gatewayPort(r.listen) {
		previous := r.listen
		r.retire()
		if next, err = r.bind(listen); err != nil {
			if back, e := r.bind(previous); e == nil {
				r.serve(back)
			}
			return err
		}
	} else if err != nil {
		return err
	} else {
		r.retire()
	}
	r.listen = listen
	r.serve(next)
	return nil
}

// reconfigure applies the quota and tool exposure of cfg to the running gateway.
func (r *gatewayRole) reconfigure(cfg *config.Config) {
	r.gw.Reconfigure(cfg.Gateway.Quota, cfg.Gateway.ExposeBackends == "favorite")
}

// bindAddress is the gateway.listen the role is serving now.
func (r *gatewayRole) bindAddress() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listen
}

// stop ends the role: it stops accepting, closes the sessions and waits for the accept loops.
func (r *gatewayRole) stop() {
	r.mu.Lock()
	r.retire()
	r.mu.Unlock()
	r.deps.registry.remove(r.gw)
	r.gw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = r.server.Shutdown(ctx)
	cancel()
	_ = r.server.Close()
	r.wg.Wait()
}

// addressInUse reports whether err says the address is taken. Windows reports it as WSAEADDRINUSE.
func addressInUse(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == syscall.EADDRINUSE || errno == 10048)
}
