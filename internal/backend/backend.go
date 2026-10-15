// Package backend supervises MCP backends and serializes each backend actor.
package backend

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/lease"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	mu        sync.Mutex
	instances map[string]*actor
	cfg       *config.Config
	leases    *lease.Manager
	upstream  func(string) *mcp.ServerSession
	changed   func()
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}
type actor struct {
	mu           sync.Mutex
	name, caller string
	cfg          config.Backend
	root         *config.Config
	gate         chan struct{}
	bridge       *mcpmux.Bridge
	tools        []*mcp.Tool
	state        string
	attempts     int
	cancel       context.CancelFunc
	ready        chan struct{}
	manager      *Manager
	lastUse      time.Time
}

func New(c *config.Config, leases *lease.Manager, upstream func(string) *mcp.ServerSession, changed func()) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{instances: map[string]*actor{}, leases: leases, upstream: upstream, changed: changed, ctx: ctx, cancel: cancel}
	m.Configure(c)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				m.reap(now)
			}
		}
	}()
	return m
}

func (m *Manager) reap(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, a := range m.instances {
		if a.cfg.Policy != "per-caller" || a.caller == "" {
			continue
		}
		idle := config.Duration(a.cfg.IdleTimeout)
		if idle <= 0 {
			idle = 30 * time.Minute
		}
		select {
		case a.gate <- struct{}{}:
			a.mu.Lock()
			expired := now.Sub(a.lastUse) >= idle
			a.mu.Unlock()
			if expired {
				a.cancel()
				delete(m.instances, key)
			}
			<-a.gate
		default: // An in-flight call retains its process until completion.
		}
	}
}
func (m *Manager) Configure(c *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.cfg
	m.cfg = c
	for key, a := range m.instances {
		cfg, ok := c.Agent.Backends[a.name]
		if !ok || !reflect.DeepEqual(cfg, a.cfg) || (old != nil && (old.Agent.DefaultCWD != c.Agent.DefaultCWD || !reflect.DeepEqual(old.Agent.Policy, c.Agent.Policy) || !reflect.DeepEqual(old.Agent.CWDPolicy, c.Agent.CWDPolicy))) {
			a.cancel()
			delete(m.instances, key)
			_ = m.leases.Invalidate("backend:" + a.name)
		}
	}
	for name, cfg := range c.Agent.Backends {
		key := name
		if cfg.Policy == "per-caller" {
			key = name + "/discovery"
		}
		if m.instances[key] == nil {
			m.spawnLocked(key, name, "", cfg, c)
		}
	}
}
func (m *Manager) spawnLocked(key, name, caller string, cfg config.Backend, c *config.Config) *actor {
	ctx, cancel := context.WithCancel(m.ctx)
	a := &actor{name: name, caller: caller, cfg: cfg, root: c, gate: make(chan struct{}, 1), state: "stopped", cancel: cancel, ready: make(chan struct{}), manager: m, lastUse: time.Now()}
	m.instances[key] = a
	m.wg.Add(1)
	go func() { defer m.wg.Done(); a.supervise(ctx) }()
	return a
}
func (m *Manager) WaitReady(ctx context.Context) {
	m.mu.Lock()
	ready := []chan struct{}{}
	for _, a := range m.instances {
		ready = append(ready, a.ready)
	}
	m.mu.Unlock()
	for _, ch := range ready {
		select {
		case <-ctx.Done():
			return
		case <-ch:
		}
	}
}
func (a *actor) notify() {
	if a.manager.changed != nil {
		a.manager.changed()
	}
}
func (a *actor) unavailable() error {
	a.mu.Lock()
	attempts := a.attempts
	a.mu.Unlock()
	return toolerr.New("backend_unavailable", "后端暂时不可用", "resource", "backend:"+a.name, "attempts", max(1, attempts))
}
func (a *actor) connect(ctx context.Context) (*mcpmux.Bridge, error) {
	resolver, e := cwd.New(a.root.Agent.DefaultCWD, a.root.Agent.Workspaces, a.root.Agent.CWDPolicy)
	if e != nil {
		return nil, e
	}
	engine, e := policy.New(a.root.Agent.Policy)
	if e != nil {
		return nil, e
	}
	var transport mcp.Transport
	if a.cfg.Transport == "stdio" {
		command := a.cfg.Command
		if len(command) == 0 {
			return nil, toolerr.Args("command")
		}
		resolved, e := resolver.Directory(a.cfg.CWD)
		if e != nil {
			return nil, e
		}
		env, e := engine.Environment(nil)
		if e != nil {
			return nil, e
		}
		if e = engine.Command(command[0], command[1:]); e != nil {
			return nil, e
		}
		launch, argv, env, e := engine.Sandbox(command[0], command[1:], resolved.Path, env)
		if e != nil {
			return nil, e
		}
		cmd := exec.Command(launch, argv...)
		cmd.Dir = resolved.Path
		cmd.Env = env
		transport = &mcp.CommandTransport{Command: cmd, TerminateDuration: 250 * time.Millisecond}
	} else {
		transport = &mcp.StreamableClientTransport{Endpoint: a.cfg.URL, MaxRetries: -1}
	}
	return mcpmux.Connect(ctx, transport, mcpmux.Options{Changed: func() { go a.refresh() }, Reverse: func() *mcp.ServerSession {
		owner := a.caller
		if a.cfg.Policy == "exclusive" {
			owner = a.manager.leases.Owner("backend:" + a.name)
		}
		if owner == "" || a.cfg.Policy == "shared" || a.manager.upstream == nil {
			return nil
		}
		return a.manager.upstream(owner)
	}})
}
func (a *actor) supervise(ctx context.Context) {
	first := true
	defer func() {
		if first {
			close(a.ready)
		}
	}()
	for attempt := 1; attempt <= 5; attempt++ {
		a.mu.Lock()
		a.attempts = attempt
		a.mu.Unlock()
		connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		bridge, e := a.connect(connectCtx)
		var tools []*mcp.Tool
		if e == nil {
			tools, e = bridge.Tools(connectCtx)
		}
		cancel()
		if e != nil {
			if bridge != nil {
				_ = bridge.Close()
			}
			a.mu.Lock()
			a.state = "crashed"
			a.mu.Unlock()
			if first {
				close(a.ready)
				first = false
			}
			a.notify()
			if !delay(ctx, time.Duration(1<<uint(attempt-1))*100*time.Millisecond) {
				return
			}
			continue
		}
		a.mu.Lock()
		a.bridge = bridge
		a.tools = tools
		a.state = "running"
		a.mu.Unlock()
		if first {
			close(a.ready)
			first = false
		}
		a.notify()
		started := time.Now()
		disconnected := make(chan struct{})
		go func() { _ = bridge.Session.Wait(); close(disconnected) }()
		select {
		case <-ctx.Done():
			_ = bridge.Close()
			<-disconnected
			return
		case <-disconnected:
		}
		if ctx.Err() != nil {
			return
		}
		// Invalidate fencing before waking queued calls or announcing the crash.
		if a.cfg.Policy == "exclusive" {
			_ = a.manager.leases.Invalidate("backend:" + a.name)
		}
		a.mu.Lock()
		a.bridge = nil
		a.state = "crashed"
		a.mu.Unlock()
		a.notify()
		// A stable run starts a new sequence of consecutive restart attempts.
		if time.Since(started) >= 30*time.Second {
			attempt = 0
		}
		if !delay(ctx, time.Duration(1<<uint(max(0, attempt-1)))*100*time.Millisecond) {
			return
		}
	}
}
func delay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (a *actor) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case a.gate <- struct{}{}:
		defer func() { <-a.gate }()
	case <-ctx.Done():
		return
	}
	a.mu.Lock()
	b := a.bridge
	a.mu.Unlock()
	if b == nil {
		return
	}
	tools, e := b.Tools(ctx)
	if e != nil {
		return
	}
	a.mu.Lock()
	changed := !reflect.DeepEqual(tools, a.tools)
	a.tools = tools
	a.mu.Unlock()
	if changed {
		a.notify()
	}
}
func (m *Manager) instance(name, caller string) (*actor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.cfg.Agent.Backends[name]
	if !ok {
		return nil, toolerr.New("not_found", "后端不存在", "name", name)
	}
	key := name
	if cfg.Policy == "per-caller" {
		key = name + "/" + caller
	}
	a := m.instances[key]
	if a == nil {
		a = m.spawnLocked(key, name, caller, cfg, m.cfg)
	}
	a.mu.Lock()
	a.lastUse = time.Now()
	a.mu.Unlock()
	return a, nil
}
func (m *Manager) Call(ctx context.Context, caller, name string, raw json.RawMessage, progress mcpmux.Progress) (*mcp.CallToolResult, error) {
	backend, tool, e := mcpmux.Split(name, ".")
	if e != nil {
		return nil, e
	}
	var args map[string]json.RawMessage
	if e = json.Unmarshal(raw, &args); e != nil || args == nil {
		return nil, toolerr.Args("arguments")
	}
	tokenRaw, hasToken := args["_lease_token"]
	delete(args, "_lease_token")
	a, e := m.instance(backend, caller)
	if e != nil {
		return nil, e
	}
	var token uint64
	if a.cfg.Policy == "exclusive" {
		if !hasToken {
			return nil, toolerr.New("lease_required", "此后端需要租约", "resource", "backend:"+backend)
		}
		token, e = lease.ParseToken(tokenRaw)
		if e != nil {
			return nil, e
		}
		if e = m.leases.Validate("backend:"+backend, caller, token); e != nil {
			return nil, e
		}
	}
	select {
	case <-a.ready:
	case <-ctx.Done():
		return nil, a.unavailable()
	}
	select {
	case a.gate <- struct{}{}:
		defer func() { <-a.gate }()
	case <-ctx.Done():
		return nil, toolerr.New("timeout", "等待后端超时", "timeout_ms", 0)
	}
	if a.cfg.Policy == "exclusive" {
		if e = m.leases.Validate("backend:"+backend, caller, token); e != nil {
			return nil, e
		}
	}
	a.mu.Lock()
	b, state := a.bridge, a.state
	a.lastUse = time.Now()
	a.mu.Unlock()
	if b == nil || state != "running" {
		return nil, a.unavailable()
	}
	result, e := b.Call(ctx, tool, args, progress)
	a.mu.Lock()
	a.lastUse = time.Now()
	a.mu.Unlock()
	if e != nil {
		if ctx.Err() != nil {
			return nil, toolerr.New("timeout", "后端调用超时", "timeout_ms", 0)
		}
		if a.cfg.Policy == "exclusive" {
			_ = m.leases.Invalidate("backend:" + backend)
		}
		_ = b.Close()
		return nil, a.unavailable()
	}
	return result, nil
}
func (m *Manager) Control(ctx context.Context, caller, name string, raw json.RawMessage) (any, error) {
	var args struct {
		Name  string          `json:"name"`
		TTL   string          `json:"ttl"`
		Token json.RawMessage `json:"_lease_token"`
	}
	if e := json.Unmarshal(raw, &args); e != nil {
		return nil, toolerr.Args("arguments")
	}
	m.mu.Lock()
	cfg, ok := m.cfg.Agent.Backends[args.Name]
	m.mu.Unlock()
	if !ok || cfg.Policy != "exclusive" {
		return nil, toolerr.Args("name")
	}
	resource := "backend:" + args.Name
	maxTTL := 30 * time.Minute
	if cfg.IdleTimeout != "" {
		maxTTL = config.Duration(cfg.IdleTimeout)
	}
	var token uint64
	var e error
	if name != "backend.acquire" {
		if len(args.Token) == 0 {
			return nil, toolerr.Args("_lease_token")
		}
		token, e = lease.ParseToken(args.Token)
		if e != nil {
			return nil, e
		}
	}
	if name == "backend.release" {
		e = m.leases.Release(resource, caller, token)
		return map[string]any{"released": true}, e
	}
	ttl, e := time.ParseDuration(args.TTL)
	if e != nil || ttl <= 0 || ttl > maxTTL {
		return nil, toolerr.Args("ttl")
	}
	if name == "backend.renew" {
		return m.leases.Renew(resource, caller, token, ttl, maxTTL)
	}
	a, e := m.instance(args.Name, caller)
	if e != nil {
		return nil, e
	}
	select {
	case <-a.ready:
	case <-ctx.Done():
		return nil, a.unavailable()
	}
	a.mu.Lock()
	running := a.state == "running"
	a.mu.Unlock()
	if !running {
		return nil, a.unavailable()
	}
	return m.leases.Acquire(resource, caller, ttl, maxTTL)
}
func (m *Manager) Tools() []*mcp.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*mcp.Tool{}
	for key, a := range m.instances {
		if a.cfg.Policy == "per-caller" && !strings.HasSuffix(key, "/discovery") {
			continue
		}
		a.mu.Lock()
		if a.state == "running" {
			for _, tool := range a.tools {
				out = append(out, mcpmux.Rename(tool, mcpmux.Prefix(a.name, tool.Name, ".")))
			}
		}
		a.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (m *Manager) States() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for _, a := range m.instances {
		a.mu.Lock()
		out[a.name] = a.state
		a.mu.Unlock()
	}
	return out
}
func (m *Manager) Disconnect(caller string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, a := range m.instances {
		if a.caller == caller && a.cfg.Policy == "per-caller" {
			a.cancel()
			delete(m.instances, key)
		}
	}
}
func (m *Manager) Close() { m.cancel(); m.wg.Wait() }
