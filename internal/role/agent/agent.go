// Package agent connects local tools to the MCP transport. Resource state lives
// here, independently of gateway connections (CON-ARCH-04).
package agent

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/backend"
	"github.com/tzf1003/xmesh/internal/backendenv"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	runexec "github.com/tzf1003/xmesh/internal/exec"
	toolsfs "github.com/tzf1003/xmesh/internal/fs"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/lease"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/pty"
	"github.com/tzf1003/xmesh/internal/quota"
	"github.com/tzf1003/xmesh/internal/sandbox"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"net"
	"net/http"
	"os"
	"path"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Principal = identity.Principal
type Authenticate func(*http.Request) (Principal, error)
type Agent struct {
	store *config.Store
	PTY   *pty.Manager
	// BudgetAuthority hosts the cross-host PTY admission lease for ingress nodes.
	BudgetAuthority *pty.BudgetAuthority
	Exec            *runexec.Manager
	Callers         *identity.Registry
	Backends        *backend.Manager
	Leases          *lease.Manager
	quota           *quota.Limiter
	quotaMu         sync.Mutex
	quotaApplied    config.Quota
	mu              sync.Mutex
	sessions        map[string]identity.Caller
	servers         map[*mcp.ServerSession]bool
	fileMu          sync.Mutex
	transfers       *toolsfs.Transfers
	closeOnce       sync.Once
	stopDenyFile    func()
	watchers        sync.WaitGroup
	views           map[string]*mcp.Server
	viewMu          sync.Mutex
	backendNames    map[*mcp.Server][]string
	backendEnv      *backendenv.Store // nil: stdio backends get no configured variables
	auditLog        atomic.Pointer[audit.Logger]
	networkInfo     atomic.Pointer[NetworkInfo]
	sandbox         atomic.Pointer[sandboxState]
	draining        atomic.Bool
	backendChanged  chan struct{}
	stop            chan struct{}
	done            chan struct{}
}
type NetworkInfo struct {
	Mode string   `json:"mode"`
	Node string   `json:"node_id"`
	Tags []string `json:"tags"`
}

// Option adjusts an Agent before its backends start.
type Option func(*Agent)

// WithBackendEnv gives stdio backends the variables kept in env. The Agent applies later changes
// of the file itself, restarting only the backends whose variables changed.
func WithBackendEnv(env *backendenv.Store) Option {
	return func(a *Agent) { a.backendEnv = env }
}

func New(store *config.Store, options ...Option) (*Agent, error) {
	c := store.Get()
	stopDenyFile := retainConfiguredDenyFile(store, c)
	retained := false
	defer func() {
		if !retained {
			stopDenyFile()
		}
	}()
	if _, e := cwd.New(c.Agent.DefaultCWD, c.Agent.Workspaces, c.Agent.CWDPolicy); e != nil {
		return nil, e
	}
	if _, e := policy.New(c.Agent.Policy); e != nil {
		return nil, e
	}
	ex, e := runexec.New(c.Agent.Exec)
	if e != nil {
		return nil, e
	}
	reg := identity.NewRegistry()
	a := &Agent{store: store, PTY: pty.New(c.Agent.PTY, reg), BudgetAuthority: pty.NewBudgetAuthority(), Exec: ex, Callers: reg, Leases: lease.New(), sessions: map[string]identity.Caller{}, servers: map[*mcp.ServerSession]bool{}, views: map[string]*mcp.Server{}, backendNames: map[*mcp.Server][]string{}}
	a.stopDenyFile = stopDenyFile
	a.backendChanged, a.stop, a.done = make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	a.transfers = toolsfs.NewTransfers()
	a.quota, a.quotaApplied = quota.New(c.Agent.Quota.Limits()), c.Agent.Quota
	for _, option := range options {
		option(a)
	}
	var backendOptions []backend.Option
	if a.backendEnv != nil {
		backendOptions = append(backendOptions, backend.WithEnv(a.backendEnv.Snapshot()))
	}
	a.Backends = backend.New(c, a.Leases, a.upstream, func() {
		select {
		case a.backendChanged <- struct{}{}:
		default:
		}
	}, backendOptions...)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	a.Backends.WaitReady(ctx)
	go a.supervise(c)
	retained = true
	return a, nil
}

// Retain the current configuration's fallback across cache eviction and hot reloads.
func retainConfiguredDenyFile(store *config.Store, initial *config.Config) func() {
	var mu sync.Mutex
	release := cwd.RetainDenyFile(initial.Agent.CWDPolicy.DenyFile)
	notified, closed := false, false
	replace := func(next *config.Config) {
		keep := cwd.RetainDenyFile(next.Agent.CWDPolicy.DenyFile)
		release()
		release = keep
	}
	unsubscribe := store.Subscribe(func(next *config.Config) {
		mu.Lock()
		defer mu.Unlock()
		notified = true
		if !closed {
			replace(next)
		}
	})
	// Catch a generation adopted before Subscribe. A notification already holds the latest pin.
	current := store.Get()
	mu.Lock()
	if !notified {
		replace(current)
	}
	mu.Unlock()
	return func() {
		unsubscribe()
		mu.Lock()
		defer mu.Unlock()
		closed = true
		release()
	}
}

func (a *Agent) supervise(previous *config.Config) {
	defer close(a.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var recordedFailures uint64
	for {
		select {
		case <-a.stop:
			return
		case <-a.backendChanged:
			a.refreshBackends()
		case <-ticker.C:
			cfg := a.store.Get()
			// Read the deny file as well, so an edit that breaks it is noticed without a tool call.
			cwd.DenyFailing(cfg.Agent.CWDPolicy.DenyFile)
			_, failures := a.store.State()
			failures += cwd.DenyFailures()
			if failures > recordedFailures {
				if log := a.auditLog.Load(); log != nil {
					if err := log.Write(map[string]any{"time": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "event": "policy_reload_failed", "caller": "config/internal", "device": cfg.Node.Name, "tool": "config.reload", "status": "error", "error": "internal", "duration_ms": 0}); err == nil {
						recordedFailures = failures
					}
				}
			}
			if cfg != previous {
				a.Backends.Configure(cfg)
				a.refreshBackends()
				a.refreshBuiltins()
				previous = cfg
			}
			// A file edited by hand is picked up here; the control channel applies its own writes at once.
			if a.backendEnv != nil {
				if changed, _ := a.backendEnv.Reload(); changed {
					a.ApplyBackendEnv()
				}
			}
		}
	}
}

// ApplyBackendEnv hands the variables now in the file to the backends. Only a backend whose own
// variables changed restarts.
func (a *Agent) ApplyBackendEnv() {
	if a.backendEnv != nil {
		a.Backends.SetEnv(a.backendEnv.Snapshot())
	}
}
func (a *Agent) SetAudit(log *audit.Logger) { a.auditLog.Store(log) }

// sandboxState is the managed isolation runner and the startup notes about it.
type sandboxState struct {
	runner   sandbox.Runner
	warnings []string
	// unavailable is why L2 is refused on a node that opted in but has no runner;
	// empty when L2 is served or not requested.
	unavailable string
}

// SetSandbox installs the managed isolation runner used for L2 execution and the
// startup notes reported in sys.info. The runner is long-lived and shared by every
// call; the policy engine is rebuilt per call and re-attached each time. A nil
// runner keeps L2 failing closed on platforms without a native launcher.
func (a *Agent) SetSandbox(runner sandbox.Runner, warnings ...string) {
	a.sandbox.Store(&sandboxState{runner: runner, warnings: append([]string{}, warnings...)})
}

// SetSandboxUnavailable records why a node that opted in to the managed runner
// cannot offer L2. Callers refused with sandbox.unavailable are told this reason
// and the control status reports it. Call it after SetSandbox.
func (a *Agent) SetSandboxUnavailable(reason string) {
	state := &sandboxState{}
	if previous := a.sandbox.Load(); previous != nil {
		*state = *previous
	}
	state.unavailable = reason
	a.sandbox.Store(state)
}

// SandboxUnavailable is the reason L2 is refused, or "" when it is served or was
// not requested.
func (a *Agent) SandboxUnavailable() string {
	if state := a.sandbox.Load(); state != nil {
		return state.unavailable
	}
	return ""
}

// attachSandbox gives a freshly built policy engine the managed runner. Only a
// non-nil interface value is installed, so an absent runner is never a typed nil.
func (a *Agent) attachSandbox(engine *policy.Engine) {
	if state := a.sandbox.Load(); state != nil {
		if state.runner != nil {
			engine.UseSandboxRunner(state.runner)
		} else if state.unavailable != "" {
			engine.UseSandboxUnavailable(state.unavailable)
		}
	}
}
func (a *Agent) SetNetwork(mode string, p Principal) {
	a.networkInfo.Store(&NetworkInfo{Mode: mode, Node: p.Node, Tags: append([]string{}, p.Tags...)})
}

func Local(r *http.Request) (Principal, error) {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil || host != "127.0.0.1" {
		return Principal{}, toolerr.New("unauthenticated", "本机开发监听仅接受 127.0.0.1")
	}
	return Principal{Node: "local", Tags: []string{"tag:peer"}}, nil
}
func Result(out any, err error) *mcp.CallToolResult {
	isError := err != nil
	if err != nil {
		out = toolerr.Internal("tools.call", err).Object()
	}
	b, e := json.Marshal(out)
	if e != nil {
		return Result(nil, toolerr.Internal("result.encode", e))
	}
	return &mcp.CallToolResult{IsError: isError, StructuredContent: out, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// grantsFor returns every agent.authz entry whose key matches one of the
// caller's tags. A key may end in or contain `*` (tag:* matches any tag); a tag
// with no matching key gets nothing, so the default stays deny.
func grantsFor(c *config.Config, caller identity.Caller) []config.Grant {
	var grants []config.Grant
	for key, grant := range c.Agent.Authz {
		for _, tag := range caller.Tags {
			if ok, _ := path.Match(key, tag); ok {
				grants = append(grants, grant)
				break
			}
		}
	}
	return grants
}
func Allowed(c *config.Config, caller identity.Caller, tool, backend string) bool {
	for _, grant := range grantsFor(c, caller) {
		if backend != "" {
			for _, pattern := range grant.Backends {
				if ok, _ := path.Match(pattern, backend); ok {
					return true
				}
			}
		} else {
			for _, pattern := range grant.Tools {
				if ok, _ := path.Match(pattern, tool); ok {
					return true
				}
			}
		}
	}
	return false
}

// syncQuota applies agent.quota when the hot-reloaded value differs from the one
// already in force, leaving the limiter alone otherwise.
func (a *Agent) syncQuota(q config.Quota) {
	a.quotaMu.Lock()
	defer a.quotaMu.Unlock()
	if q != a.quotaApplied {
		a.quota.SetLimits(q.Limits())
		a.quotaApplied = q
	}
}
func (a *Agent) bind(session *mcp.ServerSession, p Principal) identity.Caller {
	id := p.Node + "/" + session.ID()
	c := identity.Caller{ID: id, Tags: p.Tags, OAuthClient: p.OAuthClient}
	a.mu.Lock()
	_, seen := a.servers[session]
	if !seen {
		a.servers[session] = true
		a.sessions[session.ID()] = c
		_ = a.Callers.Add(c)
	}
	a.mu.Unlock()
	if !seen {
		go func() {
			_ = session.Wait()
			a.PTY.Orphan(id)
			a.Callers.Remove(id)
			a.mu.Lock()
			delete(a.sessions, session.ID())
			delete(a.servers, session)
			a.mu.Unlock()
			a.Backends.Disconnect(id)
		}()
	}
	return c
}
func (a *Agent) HTTP(authenticate Authenticate) http.Handler {
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		principal, e := authenticate(r)
		if e != nil {
			return nil
		}
		return a.server(principal)
	}, &mcp.StreamableHTTPOptions{SessionTimeout: 30 * time.Minute, MaxRequestBodyBytes: 16 << 20})
	protected := http.NewCrossOriginProtection().Handler(handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		p, e := authenticate(r)
		if e != nil {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(toolerr.Internal("authenticate", e).Object())
			return
		}
		if !identity.Valid(p.Node + "/init") {
			http.Error(w, "身份无效", 401)
			return
		}
		a.mu.Lock()
		c, exists := a.sessions[r.Header.Get("Mcp-Session-Id")]
		a.mu.Unlock()
		if exists && (identity.Node(c.ID) != p.Node || c.OAuthClient != p.OAuthClient) {
			http.Error(w, "会话身份不匹配", 403)
			return
		}
		if exists {
			c.Tags = append([]string{}, p.Tags...)
			_ = a.Callers.Add(c)
		}
		protected.ServeHTTP(w, r)
	})
}

// installBuiltins lists the built-in tools that agent.disabled_tools leaves on and removes the others
// from the view, so a tool switched off in the configuration disappears from tools/list on reload.
func (a *Agent) installBuiltins(server *mcp.Server, p Principal) {
	disabled := a.store.Get().Agent.DisabledTools
	for name, spec := range builtinSpecs() {
		if slices.Contains(disabled, name) {
			server.RemoveTools(name)
			continue
		}
		name, spec := name, spec
		server.AddTool(&mcp.Tool{Name: name, Description: spec.desc, InputSchema: spec.schema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			caller := a.bind(req.Session, p)
			if c, ok := a.Callers.Get(caller.ID); ok {
				caller = c
			}
			cfg := a.store.Get()
			if slices.Contains(cfg.Agent.DisabledTools, name) {
				return Result(nil, toolerr.New("policy_denied", "工具已在配置中关闭", "rule", "tool.disabled", "tool", name)), nil
			}
			if a.draining.Load() && (name == "exec.run" || name == "pty.open" || name == "backend.acquire") {
				return Result(nil, toolerr.New("policy_denied", "节点正在下线", "rule", "mesh.draining", "reason", "停止接受新的资源调用")), nil
			}
			if !Allowed(cfg, caller, name, "") {
				return Result(nil, toolerr.New("unauthorized", "调用方的 tag 不允许此工具", "tag", strings.Join(caller.Tags, ","), "tool", name)), nil
			}
			a.Backends.Configure(cfg)
			raw := req.Params.Arguments
			if len(raw) == 0 {
				raw = json.RawMessage(`{}`)
			}
			if e := spec.validate(raw); e != nil {
				return Result(nil, e), nil
			}
			resolver, e := cwd.New(cfg.Agent.DefaultCWD, cfg.Agent.Workspaces, cfg.Agent.CWDPolicy)
			if e != nil {
				return Result(nil, toolerr.Internal("cwd", e)), nil
			}
			engine, e := policy.New(cfg.Agent.Policy)
			if e != nil {
				return Result(nil, toolerr.Internal("policy", e)), nil
			}
			a.attachSandbox(engine)
			a.PTY.Configure(cfg.Agent.PTY)
			a.Exec.Configure(cfg.Agent.Exec)
			var out any
			switch {
			case name == "sys.info":
				out = a.info(cfg, caller, resolver)
			case strings.HasPrefix(name, "fs."):
				// Keep optimistic file mutations synchronized across configuration generations.
				a.fileMu.Lock()
				service := toolsfs.Service{Resolver: resolver, Transfers: a.transfers, Caller: caller.ID, TransferConfig: cfg.Agent.FS}
				out, e = service.Call(ctx, name, raw)
				a.fileMu.Unlock()
			case name == "exec.run":
				out, e = a.Exec.Run(ctx, caller.ID, resolver, engine, raw)
			case name == "exec.output":
				out, e = a.Exec.Output(caller.ID, raw)
			case strings.HasPrefix(name, "pty."):
				out, e = a.PTY.Call(ctx, caller, resolver, engine, name, raw)
			case strings.HasPrefix(name, "backend."):
				var args struct {
					Name string `json:"name"`
				}
				_ = json.Unmarshal(raw, &args)
				if !Allowed(cfg, caller, args.Name+".*", args.Name) || !backendToolAllowed(cfg, caller, args.Name) {
					return Result(nil, toolerr.New("unauthorized", "调用方不允许访问此后端", "tag", strings.Join(caller.Tags, ","), "tool", name)), nil
				}
				if strings.HasSuffix(caller.ID, "/init") {
					return Result(nil, toolerr.New("unauthenticated", "租约需要已建立的 MCP 会话")), nil
				}
				out, e = a.Backends.Control(ctx, caller.ID, name, raw)
			}
			return Result(out, e), nil
		})
	}
}

func (a *Agent) server(p Principal) *mcp.Server {
	a.viewMu.Lock()
	defer a.viewMu.Unlock()
	keyBytes, _ := json.Marshal(p)
	key := string(keyBytes)
	if view := a.views[key]; view != nil {
		return view
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "xmesh-agent", Version: "0.1.0"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18"}, InitializedHandler: func(_ context.Context, req *mcp.InitializedRequest) { a.bind(req.Session, p) }})
	a.installBuiltins(server, p)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (result mcp.Result, e error) {
			started := time.Now()
			if log := a.auditLog.Load(); method == "tools/call" && log != nil {
				defer func() {
					call := req.(*mcp.CallToolRequest)
					out, ok := result.(*mcp.CallToolResult)
					if !ok || out == nil {
						out = Result(nil, toolerr.Internal("tools.call", e))
					}
					caller := a.bind(call.Session, p)
					if err := log.Record(caller.ID, "", a.store.Get().Node.Name, call.Params.Name, call.Params.Arguments, out.StructuredContent, out.IsError, time.Since(started)); err != nil {
						result, e = Result(nil, err), nil
					}
				}()
			}
			if method == "tools/call" {
				call := req.(*mcp.CallToolRequest)
				if call.Params.Name != "sys.info" {
					caller := a.bind(call.Session, p)
					cfg := a.store.Get()
					a.syncQuota(cfg.Agent.Quota)
					if err := a.quota.CheckExecTimeout(call.Params.Name, call.Params.Arguments, config.Duration(cfg.Agent.Exec.DefaultTimeout)); err != nil {
						return Result(nil, err), nil
					}
					limited, release, err := a.quota.Acquire(ctx, identity.Budget(caller))
					if err != nil {
						return Result(nil, err), nil
					}
					defer release()
					ctx = limited
				}
			}
			result, e = next(ctx, method, req)
			if e == nil && method == "tools/list" {
				caller := identity.Caller{Tags: p.Tags}
				if session := req.GetSession(); session != nil {
					if current, ok := a.Callers.Get(p.Node + "/" + session.ID()); ok {
						caller = current
					}
				}
				if result, ok := result.(*mcp.ListToolsResult); ok {
					cfg := a.store.Get()
					visible := []*mcp.Tool{}
					for _, tool := range result.Tools {
						if a.toolAllowed(cfg, caller, tool.Name) {
							visible = append(visible, tool)
						}
					}
					result.Tools = visible
				}
			}
			return result, e
		}
	})
	a.views[key] = server
	a.installPTYBudget(server, p)
	a.installBackends(server, p)
	return server
}
func (a *Agent) info(c *config.Config, caller identity.Caller, r *cwd.Resolver) map[string]any {
	workspaces := []map[string]any{}
	keys := []string{}
	for alias := range c.Agent.Workspaces {
		keys = append(keys, alias)
	}
	sort.Strings(keys)
	for _, alias := range keys {
		w := c.Agent.Workspaces[alias]
		row := map[string]any{"alias": alias, "desc": w.Desc}
		p, e := r.Resolve("@"+alias, "", cwd.Existing)
		if e != nil {
			row["error"] = toolerr.Internal("workspace", e).Object()
		} else {
			row["path"] = p.Path
		}
		workspaces = append(workspaces, row)
	}
	backends := []map[string]any{}
	states := a.Backends.States()
	for name, b := range c.Agent.Backends {
		if Allowed(c, caller, "", name) {
			backends = append(backends, map[string]any{"name": name, "policy": b.Policy, "favorite": b.Favorite, "state": states[name]})
		}
	}
	shell, _ := pty.DefaultShell(c.Agent.PTY.DefaultShell)
	shells := []string{}
	if shell != "" {
		shells = append(shells, shell)
	}
	inUse, active, closed := a.PTY.Counts()
	execShared, execExclusive := a.Exec.Counts()
	warnings := []string{}
	restart, failures := a.store.State()
	if restart {
		warnings = append(warnings, "节点、网络或监听配置已变更，需要重启")
	}
	if a.store.Failing() {
		warnings = append(warnings, "配置文件当前无法加载，继续使用最近有效配置")
	}
	if cwd.DenyFailing(c.Agent.CWDPolicy.DenyFile) {
		warnings = append(warnings, "禁止目录文件当前无法加载，继续使用最近有效内容")
	}
	failures += cwd.DenyFailures()
	if c.Agent.Policy.AllowDangerous {
		warnings = append(warnings, "allow_dangerous 已启用")
	}
	if state := a.sandbox.Load(); state != nil {
		warnings = append(warnings, state.warnings...)
	}
	keyReadable := authkeyReadable(c)

	network := a.networkInfo.Load()
	if network == nil {
		network = &NetworkInfo{Mode: "local", Node: "local", Tags: []string{"tag:peer"}}
	}
	return map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "hostname": c.Node.Name, "home": r.Home, "roles": c.Node.Roles, "network": network, "restart_required": restart, "policy_reload_failures": failures, "authkey_readable": keyReadable, "warnings": warnings, "path_sep": string(os.PathSeparator), "shells": shells, "workspaces": workspaces, "backends": backends, "limits": map[string]any{"exec_shared": c.Agent.Exec.MaxConcurrent, "exec_exclusive": c.Agent.Exec.ExclusiveSlots, "exec_shared_in_use": execShared, "exec_exclusive_in_use": execExclusive, "pty_max_sessions": c.Agent.PTY.MaxSessions, "pty_max_per_caller": c.Agent.PTY.MaxPerCaller, "pty_in_use": inUse, "pty_active": active, "pty_closed": closed, "pty_closed_retain_max": c.Agent.PTY.ClosedRetainMax}}
}
func (a *Agent) Close() {
	a.closeOnce.Do(func() {
		close(a.stop)
		<-a.done
		a.watchers.Wait()
		a.mu.Lock()
		sessions := []*mcp.ServerSession{}
		for s := range a.servers {
			sessions = append(sessions, s)
		}
		a.mu.Unlock()
		for _, s := range sessions {
			_ = s.Close()
		}
		a.PTY.Shutdown()
		a.Exec.Close()
		a.Backends.Close()
		a.Leases.Close()
		a.transfers.Close()
		a.stopDenyFile()
	})
}
