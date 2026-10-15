// Package gateway aggregates device tools while keeping resource state on Agents.
package gateway

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/oauth"
	"github.com/tzf1003/xmesh/internal/quota"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Principal struct {
	ClientID string
	Scopes   []string
	Local    bool
}
type Dial func(context.Context, mesh.Peer, Principal, mcpmux.Options) (*mcpmux.Bridge, error)
type Discover func(context.Context) ([]mesh.Peer, error)
type Options struct {
	NodeName  string
	Ingress   bool
	Favorite  bool
	LocalPeer mesh.Peer
	Dial      Dial
	Discover  Discover
	Audit     *audit.Logger
}
type device struct {
	peer     mesh.Peer
	online   bool
	lastSeen time.Time
}
type Gateway struct {
	mu         sync.Mutex
	opts       Options
	devices    map[string]device
	views      map[*view]bool
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	generation uint64
	draining   map[string]bool
	quota      *quota.Limiter
}
type downstream struct {
	bridge      *mcpmux.Bridge
	tools       []*mcp.Tool
	info        map[string]any
	updated     time.Time
	failedSince time.Time
	dead        bool
	peerID      string
}
type view struct {
	mu         sync.Mutex
	refreshMu  sync.Mutex
	g          *Gateway
	p          Principal
	server     *mcp.Server
	session    *mcp.ServerSession
	defaults   map[string]json.RawMessage
	down       map[string]*downstream
	names      []string
	generation uint64
	closed     bool
}

func New(o Options) *Gateway {
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{opts: o, devices: map[string]device{}, views: map[*view]bool{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), draining: map[string]bool{}, quota: quota.New(10000, 64, 10*time.Minute)}
	if o.LocalPeer.Name != "" {
		g.devices[o.LocalPeer.Name] = device{o.LocalPeer, true, time.Now()}
	}
	go g.discover()
	return g
}
func (g *Gateway) discover() {
	defer close(g.done)
	g.poll()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
			g.poll()
		}
	}
}
func (g *Gateway) poll() {
	if g.opts.Discover == nil {
		return
	}
	ctx, cancel := context.WithTimeout(g.ctx, 5*time.Second)
	defer cancel()
	peers, e := g.opts.Discover(ctx)
	if e != nil {
		return
	}
	g.Update(peers)
}
func (g *Gateway) Update(peers []mesh.Peer) {
	g.mu.Lock()
	seen := map[string]bool{}
	changed := false
	for _, p := range peers {
		if p.Name == g.opts.LocalPeer.Name {
			continue
		}
		if !validName(p.Name) {
			continue
		}
		seen[p.Name] = true
		old, exists := g.devices[p.Name]
		if !exists || !old.online || old.peer.ID != p.ID || old.peer.Address != p.Address {
			changed = true
		}
		g.devices[p.Name] = device{p, true, time.Now()}
	}
	for name, d := range g.devices {
		if name == g.opts.LocalPeer.Name {
			continue
		}
		if !seen[name] && d.online {
			d.online = false
			g.devices[name] = d
			changed = true
		}
		if !d.online && time.Since(d.lastSeen) > 24*time.Hour {
			delete(g.devices, name)
			changed = true
		}
	}
	if changed {
		g.generation++
	}
	views := []*view{}
	for v := range g.views {
		views = append(views, v)
	}
	g.mu.Unlock()
	if changed {
		for _, v := range views {
			go v.refresh()
		}
	}
}
func validName(name string) bool {
	if len(name) < 1 || len(name) > 63 {
		return false
	}
	for _, c := range name {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return name != "local"
}
func (g *Gateway) snapshot() (map[string]device, uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]device{}
	for name, d := range g.devices {
		if g.draining[name] {
			d.online = false
		}
		out[name] = d
	}
	return out, g.generation
}
func (g *Gateway) prefix(name string) string {
	if !g.opts.Ingress && name == g.opts.LocalPeer.Name {
		return "local"
	}
	return name
}
func (v *view) allowed(device, tool, backend string) bool {
	return v.p.Local || oauth.Allows(v.p.Scopes, device, tool, backend)
}
func (g *Gateway) HTTP(auth func(*http.Request) (Principal, error)) http.Handler {
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, e := auth(r)
		if e != nil {
			return nil
		}
		return g.newView(p).server
	}, &mcp.StreamableHTTPOptions{SessionTimeout: 30 * time.Minute, MaxRequestBodyBytes: 16 << 20})
	protected := http.NewCrossOriginProtection().Handler(handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		p, e := auth(r)
		if e != nil {
			http.Error(w, "身份无效", 401)
			return
		}
		id := r.Header.Get("Mcp-Session-Id")
		if id != "" {
			g.mu.Lock()
			var existing *view
			for v := range g.views {
				v.mu.Lock()
				if v.session != nil && v.session.ID() == id {
					existing = v
				}
				v.mu.Unlock()
				if existing != nil {
					break
				}
			}
			g.mu.Unlock()
			if existing != nil && (existing.p.ClientID != p.ClientID || existing.p.Local != p.Local || scopeKey(existing.p.Scopes) != scopeKey(p.Scopes)) {
				http.Error(w, "会话身份不匹配", 403)
				return
			}
		}
		protected.ServeHTTP(w, r)
	})
}
func scopeKey(scopes []string) string {
	expanded, e := oauth.Expand(scopes)
	if e != nil {
		return "invalid"
	}
	sort.Strings(expanded)
	return strings.Join(expanded, " ")
}
func (g *Gateway) newView(p Principal) *view {
	v := &view{g: g, p: p, defaults: map[string]json.RawMessage{}, down: map[string]*downstream{}}
	v.server = mcp.NewServer(&mcp.Implementation{Name: "xmesh-gateway", Version: "0.1.0"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18"}, InitializedHandler: func(_ context.Context, r *mcp.InitializedRequest) { v.bind(r.Session) }})
	for _, name := range []string{"devices.list", "tools.search", "session.set_defaults"} {
		name := name
		v.server.AddTool(&mcp.Tool{Name: name, Description: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			v.bind(r.Session)
			return v.control(ctx, name, r.Params.Arguments), nil
		})
	}
	v.server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, r mcp.Request) (mcp.Result, error) {
			started := time.Now()
			if method == "tools/call" {
				principal := v.p.ClientID
				if principal == "" {
					principal = "local"
				}
				limited, release, e := g.quota.Acquire(ctx, principal)
				if e != nil {
					return agent.Result(nil, e), nil
				}
				defer release()
				ctx = limited
			}
			if method == "tools/list" || method == "tools/call" {
				if session, ok := r.GetSession().(*mcp.ServerSession); ok {
					v.bind(session)
				}
				v.refresh()
			}
			if method == "tools/call" {
				req := r.(*mcp.CallToolRequest)
				name := req.Params.Name
				switch name {
				case "devices.list", "tools.search", "session.set_defaults":
				default:
					if !strings.Contains(name, "/") {
						v.mu.Lock()
						deviceRaw := v.defaults["device"]
						v.mu.Unlock()
						var target string
						_ = json.Unmarshal(deviceRaw, &target)
						if target != "" {
							req.Params.Name = target + "/" + name
						}
					}
					target, tool, e := mcpmux.Split(req.Params.Name, "/")
					if e != nil {
						return agent.Result(nil, e), nil
					}
					deviceName := target
					if target == "local" && !g.opts.Ingress {
						deviceName = g.opts.LocalPeer.Name
					}
					var args map[string]json.RawMessage
					_ = json.Unmarshal(req.Params.Arguments, &args)
					backend := ""
					if strings.HasPrefix(tool, "backend.") {
						_ = json.Unmarshal(args["name"], &backend)
					}
					if !v.allowed(deviceName, tool, backend) {
						return agent.Result(nil, toolerr.New("unauthorized", "scope 不允许此工具", "scope", strings.Join(v.p.Scopes, " "), "tool", req.Params.Name)), nil
					}
					devices, _ := g.snapshot()
					d, exists := devices[deviceName]
					if !exists || !d.online {
						return agent.Result(nil, toolerr.New("not_found", "设备不在线", "name", deviceName)), nil
					}
				}
			}
			result, e := next(ctx, method, r)
			if method == "tools/call" && e == nil && g.opts.Audit != nil {
				if call, ok := r.(*mcp.CallToolRequest); ok {
					if out, ok := result.(*mcp.CallToolResult); ok {
						caller := "gateway/" + call.Session.ID()
						if err := g.opts.Audit.Record(caller, v.p.ClientID, g.opts.NodeName, call.Params.Name, call.Params.Arguments, out.StructuredContent, out.IsError, time.Since(started)); err != nil {
							return agent.Result(nil, err), nil
						}
					}
				}
			}
			if e == nil && method == "tools/list" {
				if list, ok := result.(*mcp.ListToolsResult); ok {
					visible := []*mcp.Tool{}
					for _, t := range list.Tools {
						if strings.Contains(t.Name, "/") {
							device, tool, _ := mcpmux.Split(t.Name, "/")
							if device == "local" && !g.opts.Ingress {
								device = g.opts.LocalPeer.Name
							}
							v.mu.Lock()
							d := v.down[device]
							shown := !g.opts.Favorite || builtin(tool) || (d != nil && favorite(d.info, tool))
							v.mu.Unlock()
							if shown {
								visible = append(visible, t)
							}
						} else if v.allowed("", t.Name, "") {
							visible = append(visible, t)
						}
					}
					list.Tools = visible
				}
			}
			return result, e
		}
	})
	return v
}
func (v *view) bind(s *mcp.ServerSession) {
	v.mu.Lock()
	if v.session != nil {
		v.mu.Unlock()
		return
	}
	v.session = s
	v.mu.Unlock()
	v.g.mu.Lock()
	v.g.views[v] = true
	v.g.mu.Unlock()
	go func() { _ = s.Wait(); v.close() }()
}
func (v *view) close() {
	v.refreshMu.Lock()
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		v.refreshMu.Unlock()
		return
	}
	v.closed = true
	connections := []*mcpmux.Bridge{}
	for _, d := range v.down {
		connections = append(connections, d.bridge)
	}
	v.down = map[string]*downstream{}
	v.mu.Unlock()
	v.refreshMu.Unlock()
	v.g.mu.Lock()
	delete(v.g.views, v)
	v.g.mu.Unlock()
	for _, b := range connections {
		_ = b.Close()
	}
}
func (v *view) refresh() {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	v.mu.Unlock()
	devices, generation := v.g.snapshot()
	ctx, cancel := context.WithTimeout(v.g.ctx, 10*time.Second)
	defer cancel()
	v.mu.Lock()
	remove := []*mcpmux.Bridge{}
	for name, d := range v.down {
		peer, exists := devices[name]
		if !exists || !peer.online || d.dead || d.peerID != peer.peer.ID {
			remove = append(remove, d.bridge)
			delete(v.down, name)
		}
	}
	v.mu.Unlock()
	for _, b := range remove {
		_ = b.Close()
	}
	for name, device := range devices {
		if !device.online {
			continue
		}
		v.mu.Lock()
		d := v.down[name]
		v.mu.Unlock()
		if d == nil {
			name := name
			bridge, e := v.g.opts.Dial(ctx, device.peer, v.p, mcpmux.Options{Changed: func() {
				v.mu.Lock()
				if d := v.down[name]; d != nil {
					d.updated = time.Time{}
				}
				v.mu.Unlock()
				go v.refresh()
			}, Reverse: func() *mcp.ServerSession { v.mu.Lock(); defer v.mu.Unlock(); return v.session }})
			if e != nil {
				continue
			}
			d = &downstream{bridge: bridge, peerID: device.peer.ID}
			v.mu.Lock()
			v.down[name] = d
			v.mu.Unlock()
			go func() {
				_ = bridge.Session.Wait()
				v.mu.Lock()
				if v.down[name] == d {
					d.dead = true
				}
				v.mu.Unlock()
				go v.refresh()
			}()
		}
		v.mu.Lock()
		needs := time.Since(d.updated) >= 25*time.Second
		v.mu.Unlock()
		if needs {
			tools, e := d.bridge.Tools(ctx)
			var info map[string]any
			if e == nil {
				out, err := d.bridge.Call(ctx, "sys.info", map[string]any{}, mcpmux.Progress{})
				if err == nil && !out.IsError {
					b, _ := json.Marshal(out.StructuredContent)
					_ = json.Unmarshal(b, &info)
				}
			}
			v.mu.Lock()
			if e == nil {
				d.tools = tools
				if info != nil {
					d.info = info
				}
				d.updated = time.Now()
				d.failedSince = time.Time{}
			} else {
				if d.failedSince.IsZero() {
					d.failedSince = time.Now()
				}
				if time.Since(d.failedSince) > 30*time.Second {
					d.tools = nil
				}
			}
			v.mu.Unlock()
		}
	}
	v.mu.Lock()
	old := append([]string{}, v.names...)
	entries := map[string]*mcp.Tool{}
	for name, d := range v.down {
		if d.dead {
			continue
		}
		prefix := v.g.prefix(name)
		for _, tool := range d.tools {
			full := mcpmux.Prefix(prefix, tool.Name, "/")
			if !v.allowed(name, tool.Name, "") {
				continue
			}
			entries[full] = tool
		}
	}
	v.names = nil
	for full := range entries {
		v.names = append(v.names, full)
	}
	sort.Strings(v.names)
	same := strings.Join(old, "\x00") == strings.Join(v.names, "\x00") && v.generation == generation
	v.generation = generation
	v.mu.Unlock()
	if !same {
		v.server.RemoveTools(old...)
		for full, tool := range entries {
			full := full
			v.server.AddTool(mcpmux.Rename(tool, full), func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return v.forward(ctx, r, full)
			})
		}
	}
}
func builtin(name string) bool {
	for _, p := range []string{"sys.", "exec.", "pty.", "fs.", "backend."} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
func favorite(info map[string]any, tool string) bool {
	name, _, _ := strings.Cut(tool, ".")
	backends, _ := info["backends"].([]any)
	for _, b := range backends {
		if b, ok := b.(map[string]any); ok && b["name"] == name {
			return b["favorite"] == true
		}
	}
	return false
}
func (v *view) forward(ctx context.Context, r *mcp.CallToolRequest, name string) (*mcp.CallToolResult, error) {
	target, tool, e := mcpmux.Split(name, "/")
	if e != nil {
		return agent.Result(nil, e), nil
	}
	deviceName := target
	if target == "local" && !v.g.opts.Ingress {
		deviceName = v.g.opts.LocalPeer.Name
	}
	devices, _ := v.g.snapshot()
	device, exists := devices[deviceName]
	if !exists || !device.online {
		return agent.Result(nil, toolerr.New("not_found", "设备不在线", "name", deviceName)), nil
	}
	var args map[string]json.RawMessage
	raw := r.Params.Arguments
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if e = json.Unmarshal(raw, &args); e != nil || args == nil {
		return agent.Result(nil, toolerr.Args("arguments")), nil
	}
	backend := ""
	if strings.HasPrefix(tool, "backend.") {
		_ = json.Unmarshal(args["name"], &backend)
	}
	if !v.allowed(deviceName, tool, backend) {
		return agent.Result(nil, toolerr.New("unauthorized", "scope 不允许此工具", "scope", strings.Join(v.p.Scopes, " "), "tool", name)), nil
	}
	v.mu.Lock()
	d := v.down[deviceName]
	for _, key := range []string{"cwd", "env"} {
		if _, explicit := args[key]; !explicit && v.defaults[key] != nil {
			args[key] = append(json.RawMessage{}, v.defaults[key]...)
		}
	}
	dead := d == nil || d.dead
	v.mu.Unlock()
	if dead {
		return agent.Result(nil, toolerr.New("not_found", "设备连接已关闭", "name", deviceName)), nil
	}
	out, e := d.bridge.Call(ctx, tool, args, mcpmux.Progress{Original: r.Params.GetProgressToken(), Send: r.Session.NotifyProgress})
	if e != nil {
		v.mu.Lock()
		d.dead = true
		v.mu.Unlock()
		go v.refresh()
		return agent.Result(nil, toolerr.New("not_found", "设备连接已断开", "name", deviceName)), nil
	}
	return out, nil
}
func (g *Gateway) Drain(name string) {
	g.mu.Lock()
	if name == "" {
		for device := range g.devices {
			g.draining[device] = true
		}
	} else {
		g.draining[name] = true
	}
	g.generation++
	views := []*view{}
	for v := range g.views {
		views = append(views, v)
	}
	g.mu.Unlock()
	for _, v := range views {
		go v.refresh()
	}
}
func (g *Gateway) Close() {
	g.cancel()
	<-g.done
	g.mu.Lock()
	views := []*view{}
	for v := range g.views {
		views = append(views, v)
	}
	g.mu.Unlock()
	for _, v := range views {
		v.mu.Lock()
		s := v.session
		v.mu.Unlock()
		if s != nil {
			_ = s.Close()
		}
		v.close()
	}
}
func RemoteDial(client *http.Client) Dial {
	return func(ctx context.Context, peer mesh.Peer, p Principal, opts mcpmux.Options) (*mcpmux.Bridge, error) {
		copy := *client
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		if p.ClientID != "" {
			copy.Transport = headerTransport{base: copy.Transport, client: p.ClientID}
		}
		return mcpmux.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: peer.Address, HTTPClient: &copy, MaxRetries: -1}, opts)
	}
}

type headerTransport struct {
	base   http.RoundTripper
	client string
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("X-Xmesh-Oauth-Client", t.client)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(copy)
}
