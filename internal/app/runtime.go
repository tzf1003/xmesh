// Package app assembles the configured roles in one process.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/backendenv"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/oauth"
	"github.com/tzf1003/xmesh/internal/privdir"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/role/gateway"
	"github.com/tzf1003/xmesh/internal/sandbox"
	"golang.org/x/crypto/acme/autocert"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Local                                        bool
	Listen, StateDir, PasswordFile, TunnelListen string
	Ingress                                      bool
	// ControlAllowSIDs adds Windows principals to the control pipe's access list.
	ControlAllowSIDs []string
}

func StateDir(name string) (string, error) {
	root, e := os.UserConfigDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(root, "xmesh", name), nil
}
func Run(ctx context.Context, store *config.Store, o Options) (runErr error) {
	c := store.Get()
	if o.Ingress && !c.HasRole("ingress") {
		copy := *c
		copy.Node.Roles = append(append([]string{}, c.Node.Roles...), "ingress")
		if copy.Ingress.Listen == "" || copy.Ingress.OAuth.Issuer == "" {
			return fmt.Errorf("--ingress 需要 ingress 配置")
		}
		if e := copy.Validate(); e != nil {
			return e
		}
		c = &copy
	}
	if o.StateDir == "" {
		var e error
		o.StateDir, e = StateDir(c.Node.Name)
		if e != nil {
			return e
		}
	}
	if e := privdir.Ensure(o.StateDir); e != nil {
		return e
	}
	auditDir := c.Audit.Dir
	if auditDir == "" {
		auditDir = filepath.Join(o.StateDir, "audit")
	}
	retention := config.Duration(c.Audit.Retention)
	if retention <= 0 {
		retention = 720 * time.Hour
	}
	log, e := audit.New(auditDir, retention)
	if e != nil {
		return e
	}
	defer log.Close()
	var network mesh.Network
	self := identity.Principal{Node: "local", Tags: []string{"tag:peer"}}
	if !o.Local {
		if c.Mesh.Mode == "tailnet" {
			network, e = mesh.OpenTailnet(ctx, c, filepath.Join(o.StateDir, "network"))
		} else {
			network, e = mesh.OpenLAN(ctx, c.Node.Name, filepath.Join(o.StateDir, "lan"), mesh.LANOptions{MDNS: c.Mesh.LAN.MDNS, Interface: c.Mesh.LAN.Interface})
		}
		if e != nil {
			return e
		}
		defer func() { runErr = errors.Join(runErr, network.Close()) }()
		self, e = network.Self(ctx)
		if e != nil {
			return e
		}
	}
	// The variables of stdio backends (API keys) live in the state directory, apart from the
	// configuration. A file that cannot be read is left alone and backends start without variables.
	envStore, envErr := backendenv.Open(filepath.Join(o.StateDir, backendenv.FileName))
	if envErr != nil {
		fmt.Fprintln(os.Stderr, "后端环境变量文件无法读取，后端将不带环境变量启动，文件保持原样:", filepath.Join(o.StateDir, backendenv.FileName))
		envStore = nil
	}
	var a *agent.Agent
	if c.HasRole("agent") {
		// Registered before the agent's own Close, so the runner is released
		// only after exec and PTY executions have drained.
		runner, notes, unavailable := startSandbox(ctx, c, o.StateDir)
		if runner != nil {
			defer func() { runErr = errors.Join(runErr, closeSandbox(runner)) }()
		}
		a, e = agent.New(store, agent.WithBackendEnv(envStore))
		if e != nil {
			return e
		}
		a.SetAudit(log)
		a.SetSandbox(runnerOrNil(runner), notes...)
		a.SetSandboxUnavailable(unavailable)
		mode := c.Mesh.Mode
		if o.Local {
			mode = "local"
		}
		a.SetNetwork(mode, self)
		if presence, ok := network.(mesh.Presence); ok {
			a.WatchPeers(presence.Online, 5*time.Second)
		}
		defer a.Close()
	}
	servers := []*http.Server{}
	listeners := []net.Listener{}
	registry := newGatewayRegistry()
	addGateway := func(g *gateway.Gateway) {
		registry.add(g)
		// Read the generation after registering, so a change landing between the two is
		// either seen here or delivered to the subscription.
		gw := store.Get().Gateway
		g.Reconfigure(gw.Quota, gw.ExposeBackends == "favorite")
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go store.Watch(watchCtx, time.Second)
	errc := make(chan error, 8)
	var wg sync.WaitGroup
	// roles starts, stops and moves the roles that follow the configuration; it exists once the
	// network is open.
	var roles *roleRuntime
	defer func() {
		if roles != nil {
			roles.close()
		}
		for _, g := range registry.all() {
			g.Close()
		}
		if a != nil {
			a.Close()
		}
		for _, s := range servers {
			_ = s.Close()
		}
		for _, l := range listeners {
			_ = l.Close()
		}
		wg.Wait()
	}()
	launch := func(listener net.Listener, handler http.Handler, label string, tlsConfig *tls.Config) {
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, TLSConfig: tlsConfig, ErrorLog: newHandshakeLog()}
		servers = append(servers, server)
		listeners = append(listeners, listener)
		wg.Add(1)
		go func() {
			defer wg.Done()
			var e error
			if tlsConfig != nil {
				e = server.ServeTLS(listener, "", "")
			} else {
				e = server.Serve(listener)
			}
			if e != nil && e != http.ErrServerClosed {
				select {
				case errc <- e:
				default:
				}
			}
		}()
		fmt.Fprintln(os.Stderr, label+": "+listener.Addr().String()+"/mcp")
	}
	if a != nil {
		var listener net.Listener
		auth := agent.Authenticate(agent.Local)
		if o.Local {
			address := o.Listen
			if address == "" {
				address = "127.0.0.1:7433"
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil || host != "127.0.0.1" {
				return fmt.Errorf("本机模式必须监听 127.0.0.1")
			}
			listener, e = net.Listen("tcp", address)
		} else {
			listener, e = network.Listen(ctx)
			auth = network.Authenticate
		}
		if e != nil {
			return e
		}
		mux := http.NewServeMux()
		mux.Handle("/mcp", a.HTTP(auth))
		if !o.Local {
			mux.Handle("/control/drain", a.Control(auth, registry.drain))
		}
		var handler http.Handler = mux
		if lan, ok := network.(*mesh.LAN); ok {
			// Pairing handshakes reach only the pairing routes (docs/design/14-password-pairing.md).
			handler = lan.ServeAgent(mux)
		}
		launch(listener, handler, "Agent MCP", nil)
	}
	localPeer := mesh.Peer{}
	if a != nil {
		localPeer = mesh.Peer{ID: "self", Name: c.Node.Name, Online: true, Tags: self.Tags}
	}
	var discover gateway.Discover
	var changed <-chan struct{}
	if network != nil {
		discover = network.Peers
		if notifier, ok := network.(mesh.Notifier); ok {
			changed = notifier.Changed()
		}
	}
	dialPeer := func(ctx context.Context, peer mesh.Peer, p gateway.Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		if peer.ID == "self" {
			principal := self
			if p.ClientID != "" {
				principal.OAuthClient = p.ClientID
			}
			transport, e := a.Transport(ctx, principal)
			if e != nil {
				return nil, e
			}
			return mcpmux.Connect(ctx, transport, opts)
		}
		if network == nil {
			return nil, fmt.Errorf("尚未连接设备网络")
		}
		return gateway.RemoteDial(network.HTTPClient(peer))(ctx, peer, p, opts)
	}
	// Both the catalogue refresh and the device info read dial through here; the pacing is per peer.
	dial := gateway.Paced(dialPeer, time.Now)
	roles = newRoleRuntime(gatewayDeps{store: store, registry: registry, log: log, network: network, nodeID: self.Node, localPeer: localPeer, discover: discover, changed: changed, dial: dial, mode: c.Mesh.Mode}, o.StateDir, func(e error) {
		select {
		case errc <- e:
		default:
		}
	})
	// Generations that take effect reach the running gateways; the agent re-reads its own sections
	// on every call. Sections in config.RestartPaths keep their startup values. The roles follow in
	// the worker, which starts below.
	unsubscribe := store.Subscribe(func(next *config.Config) {
		for _, g := range registry.all() {
			g.Reconfigure(next.Gateway.Quota, next.Gateway.ExposeBackends == "favorite")
		}
		roles.submit(next)
	})
	defer unsubscribe()
	if c.HasRole("gateway") {
		if e := roles.startGateway(c); e != nil {
			return e
		}
	}
	var ingress control.IngressAdmin
	if c.HasRole("ingress") {
		if o.Local {
			return fmt.Errorf("入口需要已入网的节点身份")
		}
		tagged := false
		for _, tag := range self.Tags {
			if tag == "tag:ingress" {
				tagged = true
			}
		}
		if !tagged {
			return fmt.Errorf("入口节点需要控制面授予 tag:ingress")
		}
		passwordFile := o.PasswordFile
		if passwordFile == "" {
			passwordFile = filepath.Join(o.StateDir, "oauth-password")
		}
		password, e := os.ReadFile(passwordFile)
		if e != nil {
			return fmt.Errorf("无法读取 OAuth 登录口令文件")
		}
		clients := map[string][]string{}
		for id, client := range c.Ingress.OAuth.Clients {
			clients[id] = client.Scopes
		}
		authorization, e := oauth.New(oauth.Options{Issuer: c.Ingress.OAuth.Issuer, Password: strings.TrimSpace(string(password)), StateDir: filepath.Join(o.StateDir, "oauth"), Clients: clients, DynamicScopes: c.Ingress.OAuth.DynamicClientScopes, Proxied: c.Ingress.Listen == "tunnel"})
		if e != nil {
			return e
		}
		budgetFile := c.Ingress.PTYBudgetFile
		if budgetFile == "" {
			budgetFile = filepath.Join(o.StateDir, "ingress-pty.json")
		}
		ptyBudget, e := gateway.NewPTYBudget(c.Ingress.MaxPTYPerClient, budgetFile)
		if e != nil {
			return e
		}
		if c.Ingress.PTYBudgetAuthority != "" {
			if e = ptyBudget.UseAuthority(c.Ingress.PTYBudgetAuthority); e != nil {
				return e
			}
		}
		g := gateway.New(gateway.Options{Quota: c.Gateway.Quota, NodeName: c.Node.Name, NodeID: self.Node, Ingress: true, LocalPeer: localPeer, Favorite: c.Gateway.ExposeBackends == "favorite", Discover: discover, Changed: changed, Dial: dial, Audit: log, PTYBudget: ptyBudget})
		addGateway(g)
		ingress = &ingressAdmin{auth: authorization, passwordFile: passwordFile}
		handler := authorization.Handler(g.HTTP(func(r *http.Request) (gateway.Principal, error) {
			token, ok := oauth.FromContext(r.Context())
			if !ok {
				return gateway.Principal{}, fmt.Errorf("OAuth 会话未认证")
			}
			return gateway.Principal{ClientID: token.ClientID, OAuthSession: token.Family, Scopes: token.Scopes}, nil
		}))
		var listener net.Listener
		var tlsConfig *tls.Config
		switch c.Ingress.Listen {
		case "funnel":
			tailnet, ok := network.(*mesh.Tailnet)
			if !ok {
				return fmt.Errorf("Funnel 需要 tailnet")
			}
			listener, e = tailnet.Funnel()
		case "tunnel":
			address := o.TunnelListen
			if address == "" {
				address = "127.0.0.1:8443"
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil || host != "127.0.0.1" {
				return fmt.Errorf("反向隧道入口必须绑定 127.0.0.1")
			}
			listener, e = net.Listen("tcp", address)
		case "public:443":
			listener, e = net.Listen("tcp", ":443")
			if c.Ingress.TLS == "file" {
				cert, err := tls.LoadX509KeyPair(c.Ingress.CertFile, c.Ingress.KeyFile)
				if err != nil {
					if listener != nil {
						listener.Close()
					}
					return err
				}
				tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
			} else {
				cache := filepath.Join(o.StateDir, "acme")
				if e = os.MkdirAll(cache, 0700); e != nil {
					return e
				}
				manager := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(c.Ingress.Domain), Cache: autocert.DirCache(cache)}
				tlsConfig = manager.TLSConfig()
			}
		default:
			return fmt.Errorf("入口监听模式无效")
		}
		if e != nil {
			return e
		}
		launch(listener, handler, "Ingress MCP", tlsConfig)
	}
	if _, serving := roles.gatewayListen(); len(servers) == 0 && !serving {
		return fmt.Errorf("没有可启动的角色")
	}
	mode := c.Mesh.Mode
	if o.Local {
		mode = "local"
	}
	// From here the roles follow the configuration. A generation that arrived while the node was
	// starting is applied first; applying the current one again does nothing if it changes nothing.
	roles.begin(watchCtx)
	roles.submit(store.Get())
	peerInfo := func() (map[string]gateway.DeviceInfo, bool) {
		if _, serving := roles.gatewayListen(); !serving {
			return nil, false
		}
		return readings(registry.all()), true
	}
	var pairing control.PairingAdmin
	if lan, ok := network.(*mesh.LAN); ok {
		pairing = lan
	}
	defer startControl(controlDeps{store: store, backendEnv: envStore, stateDir: o.StateDir, auditDir: auditDir, log: log, agent: a, network: network, self: self, mode: mode, allowSIDs: o.ControlAllowSIDs, ingress: ingress, pairing: pairing, peerInfo: peerInfo, roles: roles,
		drain:  func(kill bool) { registry.drain(c.Node.Name) },
		resume: func() { registry.resume(c.Node.Name, "") }})()
	select {
	case <-ctx.Done():
		return nil
	case e := <-errc:
		return e
	}
}

// runnerOrNil converts an absent runner to a nil interface value, never a typed nil.
func runnerOrNil(r l2Runner) sandbox.Runner {
	if r == nil {
		return nil
	}
	return r
}
