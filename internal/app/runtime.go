// Package app assembles the configured roles in one process.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/tzf1003/xmesh/internal/audit"
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
			network, e = mesh.OpenLAN(ctx, c.Node.Name, filepath.Join(o.StateDir, "lan"), mesh.LANOptions{MDNS: c.Mesh.LAN.MDNS})
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
	var a *agent.Agent
	if c.HasRole("agent") {
		// Registered before the agent's own Close, so the runner is released
		// only after exec and PTY executions have drained.
		runner, notes := startSandbox(ctx, c, o.StateDir)
		if runner != nil {
			defer func() { runErr = errors.Join(runErr, closeSandbox(runner)) }()
		}
		a, e = agent.New(store)
		if e != nil {
			return e
		}
		a.SetAudit(log)
		a.SetSandbox(runnerOrNil(runner), notes...)
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
	gateways := []*gateway.Gateway{}
	var gatewaysMu sync.Mutex
	pendingDrain := map[string]bool{}
	addGateway := func(g *gateway.Gateway) {
		gatewaysMu.Lock()
		defer gatewaysMu.Unlock()
		for name := range pendingDrain {
			g.Drain(name)
		}
		gateways = append(gateways, g)
	}
	errc := make(chan error, 8)
	var wg sync.WaitGroup
	defer func() {
		for _, g := range gateways {
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
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, TLSConfig: tlsConfig}
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
			mux.Handle("/control/drain", a.Control(auth, func(name string) {
				gatewaysMu.Lock()
				pendingDrain[name] = true
				current := append([]*gateway.Gateway{}, gateways...)
				gatewaysMu.Unlock()
				for _, g := range current {
					g.Drain(name)
				}
			}))
		}
		launch(listener, mux, "Agent MCP", nil)
	}
	localPeer := mesh.Peer{}
	if a != nil {
		localPeer = mesh.Peer{ID: "self", Name: c.Node.Name, Online: true, Tags: self.Tags}
	}
	var discover gateway.Discover
	if network != nil {
		discover = network.Peers
	}
	dial := func(ctx context.Context, peer mesh.Peer, p gateway.Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
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
	if c.HasRole("gateway") {
		g := gateway.New(gateway.Options{Quota: c.Gateway.Quota, NodeName: c.Node.Name, NodeID: self.Node, LocalPeer: localPeer, Favorite: c.Gateway.ExposeBackends == "favorite", Discover: discover, Dial: dial, Audit: log})
		addGateway(g)
		listener, e := net.Listen("tcp", c.Gateway.Listen)
		if e != nil {
			return e
		}
		launch(listener, g.HTTP(gateway.LocalAuthentication(store)), "Gateway MCP", nil)
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
		g := gateway.New(gateway.Options{Quota: c.Gateway.Quota, NodeName: c.Node.Name, NodeID: self.Node, Ingress: true, LocalPeer: localPeer, Favorite: c.Gateway.ExposeBackends == "favorite", Discover: discover, Dial: dial, Audit: log, PTYBudget: ptyBudget})
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
	if len(servers) == 0 {
		return fmt.Errorf("没有可启动的角色")
	}
	mode := c.Mesh.Mode
	if o.Local {
		mode = "local"
	}
	defer startControl(controlDeps{store: store, stateDir: o.StateDir, auditDir: auditDir, log: log, agent: a, network: network, self: self, mode: mode, allowSIDs: o.ControlAllowSIDs, ingress: ingress,
		drain: func(kill bool) {
			gatewaysMu.Lock()
			pendingDrain[c.Node.Name] = true
			current := append([]*gateway.Gateway{}, gateways...)
			gatewaysMu.Unlock()
			for _, g := range current {
				g.Drain(c.Node.Name)
			}
		},
		resume: func() {
			gatewaysMu.Lock()
			delete(pendingDrain, c.Node.Name)
			delete(pendingDrain, "")
			current := append([]*gateway.Gateway{}, gateways...)
			gatewaysMu.Unlock()
			for _, g := range current {
				g.Resume(c.Node.Name)
				g.Resume("")
			}
		}})()
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
