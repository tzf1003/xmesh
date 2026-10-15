// Package agent connects local tools to the MCP transport. Resource state lives
// here, independently of gateway connections (CON-ARCH-04).
package agent

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/backend"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	runexec "github.com/tzf1003/xmesh/internal/exec"
	toolsfs "github.com/tzf1003/xmesh/internal/fs"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/lease"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/pty"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"net"
	"net/http"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Principal = identity.Principal
type Authenticate func(*http.Request) (Principal, error)
type Agent struct {
	store        *config.Store
	PTY          *pty.Manager
	Exec         *runexec.Manager
	Callers      *identity.Registry
	Backends     *backend.Manager
	Leases       *lease.Manager
	mu           sync.Mutex
	sessions     map[string]identity.Caller
	servers      map[*mcp.ServerSession]bool
	fileMu       sync.Mutex
	closeOnce    sync.Once
	views        map[string]*mcp.Server
	viewMu       sync.Mutex
	backendNames map[*mcp.Server][]string
	Audit        *audit.Logger
	draining     atomic.Bool
}

func New(store *config.Store) (*Agent, error) {
	c := store.Get()
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
	a := &Agent{store: store, PTY: pty.New(c.Agent.PTY, reg), Exec: ex, Callers: reg, Leases: lease.New(), sessions: map[string]identity.Caller{}, servers: map[*mcp.ServerSession]bool{}, views: map[string]*mcp.Server{}, backendNames: map[*mcp.Server][]string{}}
	a.Backends = backend.New(c, a.Leases, a.upstream, func() { go a.refreshBackends() })
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	a.Backends.WaitReady(ctx)
	return a, nil
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
func Allowed(c *config.Config, caller identity.Caller, tool, backend string) bool {
	for _, tag := range caller.Tags {
		grant, ok := c.Agent.Authz[tag]
		if !ok {
			continue
		}
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
func (a *Agent) server(p Principal) *mcp.Server {
	a.viewMu.Lock()
	defer a.viewMu.Unlock()
	keyBytes, _ := json.Marshal(p)
	key := string(keyBytes)
	if view := a.views[key]; view != nil {
		return view
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "xmesh-agent", Version: "0.1.0"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18"}, InitializedHandler: func(_ context.Context, req *mcp.InitializedRequest) { a.bind(req.Session, p) }})
	for name, spec := range builtinSpecs() {
		name, spec := name, spec
		server.AddTool(&mcp.Tool{Name: name, Description: spec.desc, InputSchema: spec.schema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			caller := a.bind(req.Session, p)
			if c, ok := a.Callers.Get(caller.ID); ok {
				caller = c
			}
			cfg := a.store.Get()
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
			a.PTY.Configure(cfg.Agent.PTY)
			a.Exec.Configure(cfg.Agent.Exec)
			var out any
			switch {
			case name == "sys.info":
				out = a.info(cfg, caller, resolver)
			case strings.HasPrefix(name, "fs."):
				// Keep optimistic file mutations synchronized across configuration generations.
				a.fileMu.Lock()
				service := toolsfs.Service{Resolver: resolver}
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
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (result mcp.Result, e error) {
			started := time.Now()
			if method == "tools/call" && a.Audit != nil {
				defer func() {
					call := req.(*mcp.CallToolRequest)
					out, ok := result.(*mcp.CallToolResult)
					if !ok {
						out = Result(nil, toolerr.Internal("tools.call", e))
					}
					caller := a.bind(call.Session, p)
					if err := a.Audit.Record(caller.ID, "", a.store.Get().Node.Name, call.Params.Name, call.Params.Arguments, out.StructuredContent, out.IsError, time.Since(started)); err != nil {
						result, e = Result(nil, err), nil
					}
				}()
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
	warnings := []string{}
	if c.Agent.Policy.AllowDangerous {
		warnings = append(warnings, "allow_dangerous 已启用")
	}
	readable := false
	if c.Mesh.Mode == "tailnet" {
		if f, e := os.Open(c.Mesh.AuthkeyFile); e == nil {
			readable = true
			_ = f.Close()
		}
	}
	return map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "hostname": c.Node.Name, "home": r.Home, "roles": c.Node.Roles, "authkey_readable": readable, "warnings": warnings, "path_sep": string(os.PathSeparator), "shells": shells, "workspaces": workspaces, "backends": backends, "limits": map[string]any{"exec_shared": c.Agent.Exec.MaxConcurrent, "exec_exclusive": c.Agent.Exec.ExclusiveSlots, "pty_max_sessions": c.Agent.PTY.MaxSessions, "pty_max_per_caller": c.Agent.PTY.MaxPerCaller, "pty_in_use": inUse, "pty_active": active, "pty_closed": closed, "pty_closed_retain_max": c.Agent.PTY.ClosedRetainMax}}
}
func (a *Agent) Close() {
	a.closeOnce.Do(func() {
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
	})
}
