// Package gateway aggregates device tools while keeping resource state on Agents.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/oauth"
	"github.com/tzf1003/xmesh/internal/quota"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Principal struct {
	ClientID     string
	Origin       string
	CredentialID string
	// OAuthSession identifies the OAuth grant behind an ingress request. It is
	// only used to correlate audit records and is never written verbatim.
	OAuthSession string
	Scopes       []string
	Local        bool
}
type Dial func(context.Context, mesh.Peer, Principal, mcpmux.Options) (*mcpmux.Bridge, error)
type Discover func(context.Context) ([]mesh.Peer, error)
type Options struct {
	NodeName string
	// NodeID is the network identity that Agents see as the caller's node
	// (CTR-ID-01). Audit callers are NodeID/<session>; it defaults to NodeName.
	NodeID    string
	Ingress   bool
	Favorite  bool
	LocalPeer mesh.Peer
	Dial      Dial
	Discover  Discover
	// Changed, when set, wakes the discovery loop ahead of its 10 s poll (LAN announcements and goodbyes).
	Changed   <-chan struct{}
	Audit     *audit.Logger
	PTYBudget *PTYBudget
	// Quota is gateway.quota; zero fields take the built-in defaults.
	Quota config.Quota
}
type device struct {
	peer     mesh.Peer
	online   bool
	lastSeen time.Time
}
type Gateway struct {
	mu           sync.Mutex
	opts         Options
	devices      map[string]device
	views        map[*view]bool
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	generation   uint64
	draining     map[string]bool
	meshDraining bool
	quota        *quota.Limiter
	// quotaApplied is the gateway.quota in force, so an unchanged generation leaves the limiter alone.
	quotaApplied config.Quota
	// favorite mirrors gateway.expose_backends; Reconfigure may change it while requests run.
	favorite atomic.Bool
	// info holds the background sys.info readings behind the control channel's device state.
	info        map[string]*infoRecord
	infoWorkers sync.WaitGroup
}
type downstream struct {
	bridge      *mcpmux.Bridge
	tools       []*mcp.Tool
	info        map[string]any
	updated     time.Time
	failedSince time.Time
	dead        bool
	peerID      string
	peerAddress string
}
type refreshJob struct {
	peer    mesh.Peer
	done    chan struct{}
	cancel  context.CancelFunc
	running bool
	dirty   bool // a tool-list notification arrived while this snapshot was being read
	retryAt time.Time
}

type view struct {
	mu         sync.Mutex
	refreshMu  sync.Mutex
	workers    sync.WaitGroup
	closedDone chan struct{}
	jobs       map[string]*refreshJob
	ctx        context.Context
	cancel     context.CancelFunc
	g          *Gateway
	p          Principal
	server     *mcp.Server
	session    *mcp.ServerSession
	defaults   map[string]json.RawMessage
	leases     map[string]*leaseBeat
	oauthAudit string
	down       map[string]*downstream
	names      []string
	generation uint64
	catalog    [32]byte
	closed     bool
}

func New(o Options) *Gateway {
	if o.Ingress && o.PTYBudget == nil {
		o.PTYBudget, _ = NewPTYBudget(8, "")
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{opts: o, devices: map[string]device{}, views: map[*view]bool{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), draining: map[string]bool{}, info: map[string]*infoRecord{}, quota: quota.New(o.Quota.Limits()), quotaApplied: o.Quota}
	g.favorite.Store(o.Favorite)
	if o.LocalPeer.Name != "" {
		g.devices[o.LocalPeer.Name] = device{o.LocalPeer, true, time.Now()}
	}
	go g.discover()
	return g
}

// Reconfigure applies the gateway.quota and gateway.expose_backends of a new
// configuration generation. Calls already admitted keep the limits they were
// given; later calls use the new ones. A change of expose_backends refreshes the
// tool lists of the connected clients.
func (g *Gateway) Reconfigure(q config.Quota, favorite bool) {
	changed := g.favorite.Swap(favorite) != favorite
	g.mu.Lock()
	if q != g.quotaApplied {
		g.quota.SetLimits(q.Limits())
		g.quotaApplied = q
	}
	g.mu.Unlock()
	if changed {
		// Which tools tools/list shows depends on it, so connected clients are told to read the list again.
		g.refreshViews()
	}
}
func (g *Gateway) discover() {
	defer close(g.done)
	g.poll()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	info := time.NewTicker(infoTick)
	defer info.Stop()
	// Registered last, so it runs first: Close waits for the reads before it reports the loop done.
	defer g.infoWorkers.Wait()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-g.opts.Changed:
			g.poll()
		case <-ticker.C:
			g.poll()
			g.refreshViews()
		case now := <-info.C:
			g.pollInfo(now)
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
		if (p.ID != "" && p.ID == g.opts.LocalPeer.ID) || (p.ID == "" && p.Name == g.opts.LocalPeer.Name) {
			continue
		}
		if !validName(p.Name) {
			continue
		}
		seen[p.Name] = true
		old, exists := g.devices[p.Name]
		if exists && (old.peer.ID != p.ID || old.peer.Address != p.Address) {
			delete(g.info, p.Name)
		}
		if !exists || !old.online || !reflect.DeepEqual(old.peer, p) {
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
			// A device that leaves the network and returns is a fresh process;
			// its drain state is memory-only (docs/operations.md).
			delete(g.draining, name)
			changed = true
		}
		if !d.online && time.Since(d.lastSeen) > 24*time.Hour {
			delete(g.devices, name)
			delete(g.draining, name)
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
func (g *Gateway) refreshViews() {
	g.mu.Lock()
	views := make([]*view, 0, len(g.views))
	for v := range g.views {
		views = append(views, v)
	}
	g.mu.Unlock()
	for _, v := range views {
		go v.refresh()
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
		if g.meshDraining || g.draining[name] {
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
		// Origin is client-reported audit metadata; authentication and scopes are unchanged.
		if r.Header.Get("X-Xmesh-Origin") == "panel" {
			p.Origin = "panel"
		}
		return g.newView(p).server
	}, &mcp.StreamableHTTPOptions{SessionTimeout: 30 * time.Minute, MaxRequestBodyBytes: 16 << 20})
	protected := http.NewCrossOriginProtection().Handler(handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if e := precheck(w, r); e {
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
			if existing != nil && (existing.p.ClientID != p.ClientID || existing.p.CredentialID != p.CredentialID || existing.p.Local != p.Local || scopeKey(existing.p.Scopes) != scopeKey(p.Scopes)) {
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
	ctx, cancel := context.WithCancel(g.ctx)
	v := &view{g: g, p: p, defaults: map[string]json.RawMessage{}, leases: map[string]*leaseBeat{}, oauthAudit: oauthAuditID(p.OAuthSession), down: map[string]*downstream{}, jobs: map[string]*refreshJob{}, ctx: ctx, cancel: cancel, closedDone: make(chan struct{})}
	v.server = mcp.NewServer(&mcp.Implementation{Name: "xmesh-gateway", Version: "0.1.0"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18"}, InitializedHandler: func(_ context.Context, r *mcp.InitializedRequest) { v.bind(r.Session) }})
	for _, tool := range controlTools() {
		name := tool.Name
		v.server.AddTool(tool, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			v.bind(r.Session)
			return v.control(ctx, name, r.Params.Arguments), nil
		})
	}
	v.server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, r mcp.Request) (result mcp.Result, e error) {
			started := time.Now()
			if method == "tools/call" && g.opts.Audit != nil {
				defer func() {
					call := r.(*mcp.CallToolRequest)
					out, ok := result.(*mcp.CallToolResult)
					if !ok || out == nil {
						out = agent.Result(nil, toolerr.Internal("tools.call", e))
					}
					device, caller := v.auditIdentity(call.Params.Name, call.Session.ID())
					if err := g.opts.Audit.RecordSessionOrigin(v.p.Origin, v.oauthAudit, caller, v.p.ClientID, device, call.Params.Name, call.Params.Arguments, out.StructuredContent, out.IsError, time.Since(started)); err != nil {
						result, e = agent.Result(nil, err), nil
					}
				}()
			}
			if method == "tools/call" {
				principal := v.p.ClientID
				if v.p.CredentialID != "" {
					principal = v.p.CredentialID
				}
				if principal == "" {
					principal = "local"
				}
				call := r.(*mcp.CallToolRequest)
				if e := g.quota.CheckExecTimeout(call.Params.Name, call.Params.Arguments, 0); e != nil {
					return agent.Result(nil, e), nil
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
				if method == "tools/list" {
					v.waitReady(ctx, g.opts.LocalPeer.Name)
				}
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
					if !v.allowed(deviceName, tool, backend) || (!v.p.Local && !oauth.AllowsArguments(v.p.Scopes, deviceName, tool, args)) {
						return agent.Result(nil, toolerr.New("unauthorized", "scope 不允许此工具", "scope", strings.Join(v.p.Scopes, " "), "tool", req.Params.Name)), nil
					}
					devices, _ := g.snapshot()
					d, exists := devices[deviceName]
					if !exists || !d.online {
						return agent.Result(nil, toolerr.New("not_found", "设备不在线", "name", deviceName)), nil
					}
					v.waitReady(ctx, deviceName)
					v.mu.Lock()
					connection := v.down[deviceName]
					ready := connection != nil && !connection.dead && connection.tools != nil
					v.mu.Unlock()
					if !ready {
						return agent.Result(nil, toolerr.New("not_found", "设备能力尚未就绪", "name", deviceName)), nil
					}
				}
			}
			result, e = next(ctx, method, r)
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
							// The default device is what an unprefixed call
							// reaches, so its whole table stays visible with
							// schemas even when favorites hide the rest
							// (CTR-GW-02). Other devices stay on favorites.
							shown := !g.favorite.Load() || builtin(tool) || (d != nil && favorite(d.info, tool)) || device == v.defaultDeviceLocked()
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

// defaultDeviceLocked returns the session's default device; the caller holds
// v.mu.
func (v *view) defaultDeviceLocked() string {
	raw := v.defaults["device"]
	if raw == nil {
		return ""
	}
	var device string
	_ = json.Unmarshal(raw, &device)
	return device
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
	go v.refresh()
	go func() { _ = s.Wait(); v.close() }()
}
func (v *view) close() {
	v.refreshMu.Lock()
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		v.refreshMu.Unlock()
		<-v.closedDone
		return
	}
	defer close(v.closedDone)
	v.closed = true
	v.cancel()
	for _, job := range v.jobs {
		job.cancel()
	}
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
	v.workers.Wait()
}

// refresh reconciles discovery and schedules independent per-device I/O.
// The catalogue lock is never held while connecting to or querying an Agent.
func (v *view) refresh() {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	devices, generation := v.g.snapshot()
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	var removed []*mcpmux.Bridge
	for name, d := range v.down {
		peer, exists := devices[name]
		if !exists || !peer.online || d.dead || d.peerID != peer.peer.ID || d.peerAddress != peer.peer.Address {
			removed = append(removed, d.bridge)
			delete(v.down, name)
		}
	}
	for name, job := range v.jobs {
		peer, exists := devices[name]
		if !exists || !peer.online || job.peer.ID != peer.peer.ID || job.peer.Address != peer.peer.Address {
			job.cancel()
			delete(v.jobs, name)
		}
	}
	for name, device := range devices {
		if !device.online {
			continue
		}
		if job := v.jobs[name]; job != nil && (job.running || time.Now().Before(job.retryAt)) {
			continue
		}
		d := v.down[name]
		if d != nil && time.Since(d.updated) < 25*time.Second {
			continue
		}
		ctx, cancel := context.WithTimeout(v.ctx, 10*time.Second)
		job := &refreshJob{peer: device.peer, done: make(chan struct{}), cancel: cancel, running: true}
		v.jobs[name] = job
		v.workers.Add(1)
		go v.refreshPeer(ctx, name, job, d)
	}
	v.mu.Unlock()
	v.publish(generation)
	for _, bridge := range removed {
		v.workers.Add(1)
		go func() { defer v.workers.Done(); _ = bridge.Close() }()
	}
}

// waitReady only waits for the requested device's first catalogue. Cached calls
// proceed while an independent background refresh updates that device.
func (v *view) waitReady(ctx context.Context, name string) {
	v.refreshMu.Lock()
	v.mu.Lock()
	d, job := v.down[name], v.jobs[name]
	ready := d != nil && !d.dead && d.tools != nil
	v.mu.Unlock()
	v.refreshMu.Unlock()
	if ready || job == nil {
		return
	}
	select {
	case <-ctx.Done():
	case <-job.done:
	}
}

func (v *view) refreshPeer(ctx context.Context, name string, job *refreshJob, d *downstream) {
	defer v.workers.Done()
	defer close(job.done)
	defer job.cancel()
	created := false
	var err error
	if d == nil {
		var bridge *mcpmux.Bridge
		bridge, err = v.g.opts.Dial(ctx, job.peer, v.p, mcpmux.Options{Changed: func() {
			v.mu.Lock()
			if pending := v.jobs[name]; pending != nil && pending.running {
				pending.dirty = true
			}
			if current := v.down[name]; current != nil {
				current.updated = time.Time{}
			}
			v.mu.Unlock()
			go v.refresh()
		}, Reverse: func() *mcp.ServerSession { v.mu.Lock(); defer v.mu.Unlock(); return v.session }, Unrouted: func() { v.auditUnrouted(name) }})
		if err == nil {
			d = &downstream{bridge: bridge, peerID: job.peer.ID, peerAddress: job.peer.Address}
			created = true
		}
	}
	var tools []*mcp.Tool
	var info map[string]any
	if err == nil {
		tools, err = d.bridge.Tools(ctx)
		if err == nil {
			var out *mcp.CallToolResult
			out, err = d.bridge.Call(ctx, "sys.info", map[string]any{}, mcpmux.Progress{})
			if err == nil && out != nil && !out.IsError {
				var b []byte
				b, err = json.Marshal(out.StructuredContent)
				if err == nil {
					err = json.Unmarshal(b, &info)
				}
				if err == nil && info == nil {
					err = fmt.Errorf("sys.info 返回为空")
				}
			} else if err == nil {
				err = fmt.Errorf("sys.info 暂不可用")
			}
		}
	}
	v.refreshMu.Lock()
	devices, generation := v.g.snapshot()
	current, exists := devices[name]
	v.mu.Lock()
	valid := !v.closed && v.jobs[name] == job && exists && current.online && current.peer.ID == job.peer.ID && current.peer.Address == job.peer.Address
	again := false
	if valid {
		job.running = false
		if err != nil {
			job.retryAt = time.Now().Add(5 * time.Second)
		}
		if d != nil {
			v.down[name] = d
			if err == nil {
				d.tools, d.info, d.updated, d.failedSince = tools, info, time.Now(), time.Time{}
				if job.dirty {
					// A change after Tools() must not become fresh when sys.info finishes later.
					d.updated = time.Time{}
					again = true
				}
			} else {
				if d.failedSince.IsZero() {
					d.failedSince = time.Now()
				}
				if time.Since(d.failedSince) > 30*time.Second {
					d.tools = nil
				}
			}
		}
	}
	v.mu.Unlock()
	if valid {
		v.publish(generation)
	}
	v.refreshMu.Unlock()
	if again {
		go v.refresh()
	}
	if !valid && created {
		_ = d.bridge.Close()
	}
	if valid && created {
		go func() {
			_ = d.bridge.Session.Wait()
			v.mu.Lock()
			if v.down[name] == d {
				d.dead = true
			}
			v.mu.Unlock()
			go v.refresh()
		}()
	}
}

// publish runs with refreshMu held, using only cached device data.
func (v *view) publish(generation uint64) {
	v.mu.Lock()
	old := append([]string{}, v.names...)
	entries := map[string]*mcp.Tool{}
	visibility := map[string]bool{}
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
			visibility[full] = !v.g.favorite.Load() || builtin(tool.Name) || favorite(d.info, tool.Name)
		}
	}
	v.names = nil
	for full := range entries {
		v.names = append(v.names, full)
	}
	sort.Strings(v.names)
	encoded, _ := json.Marshal([]any{entries, visibility})
	catalog := sha256.Sum256(encoded)
	same := v.catalog == catalog && v.generation == generation
	v.catalog = catalog
	v.generation = generation
	v.mu.Unlock()
	if !same {
		for full, tool := range entries {
			full := full
			v.server.AddTool(mcpmux.Rename(tool, full), func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return v.forward(ctx, r, full)
			})
		}
		removed := []string{}
		for _, name := range old {
			if entries[name] == nil {
				removed = append(removed, name)
			}
		}
		v.server.RemoveTools(removed...)
	}
}
func builtin(name string) bool {
	for _, p := range []string{"sys.", "exec.", "pty.", "fs.", "backend.", "computer."} {
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
	if !v.allowed(deviceName, tool, backend) || (!v.p.Local && !oauth.AllowsArguments(v.p.Scopes, deviceName, tool, args)) {
		return agent.Result(nil, toolerr.New("unauthorized", "scope 不允许此工具", "scope", strings.Join(v.p.Scopes, " "), "tool", name)), nil
	}
	v.mu.Lock()
	d := v.down[deviceName]
	// Session defaults belong to the built-in tools that declare the parameter;
	// backend tools are forwarded verbatim (CTR-GW-01, CON-TOOL-10).
	if d != nil && builtin(tool) {
		for _, key := range []string{"cwd", "env"} {
			if _, explicit := args[key]; !explicit && v.defaults[key] != nil && declares(d.tools, tool, key) {
				args[key] = append(json.RawMessage{}, v.defaults[key]...)
			}
		}
	}
	dead := d == nil || d.dead
	v.mu.Unlock()
	if dead {
		return agent.Result(nil, toolerr.New("not_found", "设备连接已关闭", "name", deviceName)), nil
	}
	invoke := func(ctx context.Context, bridge *mcpmux.Bridge) (*mcp.CallToolResult, error) {
		return bridge.Call(ctx, tool, args, mcpmux.Progress{Original: r.Params.GetProgressToken(), Send: r.Session.NotifyProgress})
	}
	var out *mcp.CallToolResult
	if v.g.opts.Ingress && (tool == "pty.open" || tool == "pty.transfer") {
		out, e = v.g.opts.PTYBudget.call(ctx, v, device.peer, tool, args, invoke)
	} else {
		out, e = invoke(ctx, d.bridge)
	}
	if e != nil {
		protocol, answered := mcpmux.ProtocolError(e)
		if answered || ctx.Err() != nil && alive(d.bridge) {
			// The Agent answered, or only this call was cancelled: the
			// connection is alive. A protocol error usually means the tool
			// vanished (for example its backend was killed), so refresh the
			// catalogue instead of dropping the whole device.
			v.mu.Lock()
			d.updated = time.Time{}
			v.mu.Unlock()
			go v.refresh()
			if answered {
				return agent.Result(nil, toolerr.New("not_found", "设备上没有此工具，或其后端不可用", "name", deviceName, "tool", tool, "reason", protocol)), nil
			}
			return agent.Result(nil, toolerr.New("timeout", "调用已取消或超时", "name", deviceName, "tool", tool)), nil
		}
		v.mu.Lock()
		d.dead = true
		v.mu.Unlock()
		go v.refresh()
		return agent.Result(nil, toolerr.New("not_found", "设备连接已断开", "name", deviceName)), nil
	}
	if out != nil && out.IsError && meshDraining(out) {
		// The Agent is draining: stop routing to it now rather than waiting
		// for the control message to reach this Gateway (CTR-GW-05).
		v.g.Drain(deviceName)
	} else if out != nil && !out.IsError {
		switch tool {
		case "backend.acquire":
			v.keepLease(deviceName, d, args, out)
		case "backend.release":
			v.dropLease(deviceName, args)
		}
	}
	return out, nil
}

// alive probes a downstream connection after a cancelled call.
func alive(b *mcpmux.Bridge) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return b.Session.Ping(ctx, nil) == nil
}
func (g *Gateway) Drain(name string) {
	g.mu.Lock()
	if name == "" {
		g.meshDraining = true
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
