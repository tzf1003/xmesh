package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/oauth"
	"github.com/tzf1003/xmesh/internal/role/gateway"
	"golang.org/x/crypto/acme/autocert"
)

// ingressDeps is what the ingress needs from the rest of the process, beyond what any gateway needs.
type ingressDeps struct {
	gatewayDeps
	// local is true for a node that runs without a device network, which cannot be an ingress.
	local bool
	// self is the node's identity on the network; the ingress needs tag:ingress.
	self identity.Principal
	// stateDir holds the OAuth state, the PTY budget journal and the ACME cache.
	stateDir string
	// passwordFile is the OAuth login password file; empty means oauth-password in stateDir.
	passwordFile string
	// tunnelListen is where a reverse tunnel reaches the ingress; empty falls back to ingress.tunnel_listen, then 127.0.0.1:8443.
	tunnelListen string
}

func (d ingressDeps) password() string {
	if d.passwordFile != "" {
		return d.passwordFile
	}
	return filepath.Join(d.stateDir, "oauth-password")
}

// tunnel is the address the reverse tunnel reaches: the --tunnel-listen flag, else ingress.tunnel_listen,
// else 127.0.0.1:8443.
func (d ingressDeps) tunnel(cfg *config.Config) string {
	if d.tunnelListen != "" {
		return d.tunnelListen
	}
	if cfg.Ingress.TunnelListen != "" {
		return cfg.Ingress.TunnelListen
	}
	return "127.0.0.1:8443"
}

// ingressRole is the public entry: an OAuth server in front of a gateway, served on the listener the
// ingress configuration names. Like the local gateway it is only a way in: the sessions it hands out
// live in the agents (INV-03), so the role can be stopped and started again while the node goes on.
// Clients that signed in through it have to sign in again, because the OAuth tokens live in the
// process of the role, not on disk.
type ingressRole struct {
	deps   ingressDeps
	cfg    *config.Config // the generation the role was started from
	admin  *ingressAdmin
	gw     *gateway.Gateway
	server *http.Server
	wg     sync.WaitGroup

	mu       sync.Mutex
	stopping bool
}

// validateIngress checks everything about starting an ingress with cfg that can be checked without
// starting it, so a restart that cannot succeed leaves the running ingress alone.
func validateIngress(deps ingressDeps, cfg *config.Config) error {
	if deps.local {
		return fmt.Errorf("入口需要已入网的节点身份")
	}
	_, deviceNetwork := deps.network.(interface{ MultiPath() bool })
	tagged := false
	for _, tag := range deps.self.Tags {
		if tag == "tag:ingress" {
			tagged = true
		}
	}
	if !tagged && !deviceNetwork {
		return fmt.Errorf("入口节点需要控制面授予 tag:ingress")
	}
	if _, e := os.ReadFile(deps.password()); e != nil {
		return fmt.Errorf("无法读取 OAuth 登录口令文件")
	}
	switch cfg.Ingress.Listen {
	case "funnel":
		if _, ok := deps.network.(interface{ Funnel() (net.Listener, error) }); !ok {
			return fmt.Errorf("Funnel 需要 tailnet")
		}
	case "tunnel":
		host, _, err := net.SplitHostPort(deps.tunnel(cfg))
		if err != nil || host != "127.0.0.1" {
			return fmt.Errorf("反向隧道入口必须绑定 127.0.0.1")
		}
	case "public:443":
		if cfg.Ingress.TLS == "file" {
			if _, err := tls.LoadX509KeyPair(cfg.Ingress.CertFile, cfg.Ingress.KeyFile); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("入口监听模式无效")
	}
	return nil
}

// startIngressRole starts the ingress from cfg. On failure nothing is left running.
func startIngressRole(deps ingressDeps, cfg *config.Config) (*ingressRole, error) {
	if e := validateIngress(deps, cfg); e != nil {
		return nil, e
	}
	passwordFile := deps.password()
	password, e := os.ReadFile(passwordFile)
	if e != nil {
		return nil, fmt.Errorf("无法读取 OAuth 登录口令文件")
	}
	clients := map[string][]string{}
	for id, client := range cfg.Ingress.OAuth.Clients {
		clients[id] = client.Scopes
	}
	authorization, e := oauth.New(oauth.Options{Issuer: cfg.Ingress.OAuth.Issuer, Password: strings.TrimSpace(string(password)), StateDir: filepath.Join(deps.stateDir, "oauth"), Clients: clients, DynamicScopes: cfg.Ingress.OAuth.DynamicClientScopes, Proxied: cfg.Ingress.Listen == "tunnel"})
	if e != nil {
		return nil, e
	}
	budgetFile := cfg.Ingress.PTYBudgetFile
	if budgetFile == "" {
		budgetFile = filepath.Join(deps.stateDir, "ingress-pty.json")
	}
	ptyBudget, e := gateway.NewPTYBudget(cfg.Ingress.MaxPTYPerClient, budgetFile)
	if e != nil {
		return nil, e
	}
	if cfg.Ingress.PTYBudgetAuthority != "" {
		if e = ptyBudget.UseAuthority(cfg.Ingress.PTYBudgetAuthority); e != nil {
			return nil, e
		}
	}
	var listener net.Listener
	var tlsConfig *tls.Config
	switch cfg.Ingress.Listen {
	case "funnel":
		listener, e = deps.network.(interface{ Funnel() (net.Listener, error) }).Funnel()
	case "tunnel":
		listener, e = net.Listen("tcp", deps.tunnel(cfg))
	case "public:443":
		if cfg.Ingress.TLS == "file" {
			cert, err := tls.LoadX509KeyPair(cfg.Ingress.CertFile, cfg.Ingress.KeyFile)
			if err != nil {
				return nil, err
			}
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		} else {
			cache := filepath.Join(deps.stateDir, "acme")
			if e = os.MkdirAll(cache, 0700); e != nil {
				return nil, e
			}
			manager := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(cfg.Ingress.Domain), Cache: autocert.DirCache(cache)}
			tlsConfig = manager.TLSConfig()
		}
		listener, e = net.Listen("tcp", ":443")
	}
	if e != nil {
		return nil, e
	}
	ingressDial := func(ctx context.Context, peer mesh.Peer, principal gateway.Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		principal.Ingress = true
		return deps.dial(ctx, peer, principal, opts)
	}
	g := gateway.New(gateway.Options{Quota: cfg.Gateway.Quota, NodeName: cfg.Node.Name, NodeID: deps.nodeID, Ingress: true, LocalPeer: deps.localPeer, Favorite: cfg.Gateway.ExposeBackends == "favorite", Discover: deps.discover, Changed: deps.changed, Dial: ingressDial, Audit: deps.log, PTYBudget: ptyBudget})
	deps.registry.add(g)
	// Read the generation after registering, so a change landing between the two is either seen here
	// or delivered to the subscription.
	current := deps.store.Get().Gateway
	g.Reconfigure(current.Quota, current.ExposeBackends == "favorite")
	handler := authorization.Handler(g.HTTP(func(r *http.Request) (gateway.Principal, error) {
		token, ok := oauth.FromContext(r.Context())
		if !ok {
			return gateway.Principal{}, fmt.Errorf("OAuth 会话未认证")
		}
		return gateway.Principal{ClientID: token.ClientID, OAuthSession: token.Family, Scopes: token.Scopes}, nil
	}))
	r := &ingressRole{deps: deps, cfg: cfg, admin: &ingressAdmin{auth: authorization, passwordFile: passwordFile}, gw: g}
	r.server = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, TLSConfig: tlsConfig, ErrorLog: newHandshakeLog()}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		var err error
		if tlsConfig != nil {
			err = r.server.ServeTLS(listener, "", "")
		} else {
			err = r.server.Serve(listener)
		}
		r.mu.Lock()
		deliberate := r.stopping
		r.mu.Unlock()
		if err != nil && !deliberate && !errors.Is(err, http.ErrServerClosed) && deps.serveFailed != nil {
			deps.serveFailed(err)
		}
	}()
	fmt.Fprintln(os.Stderr, "Ingress MCP: "+listener.Addr().String()+"/mcp")
	return r, nil
}

// stop ends the role: it stops accepting, closes the sessions it handed out and waits for the
// accept loop.
func (r *ingressRole) stop() {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
	r.deps.registry.remove(r.gw)
	r.gw.Close()
	_ = r.server.Close()
	r.wg.Wait()
}
