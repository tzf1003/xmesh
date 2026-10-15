// Package agent connects local tools to the MCP transport. Resource state lives
// here, independently of gateway connections (CON-ARCH-04).
package agent

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	runexec "github.com/tzf1003/xmesh/internal/exec"
	toolsfs "github.com/tzf1003/xmesh/internal/fs"
	"github.com/tzf1003/xmesh/internal/identity"
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
	"time"
)

type Principal struct {
	Node        string
	Tags        []string
	OAuthClient string
}
type Authenticate func(*http.Request) (Principal, error)
type Agent struct {
	store     *config.Store
	PTY       *pty.Manager
	Exec      *runexec.Manager
	Callers   *identity.Registry
	mu        sync.Mutex
	sessions  map[string]identity.Caller
	servers   map[*mcp.ServerSession]bool
	fileMu    sync.Mutex
	closeOnce sync.Once
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
	return &Agent{store: store, PTY: pty.New(c.Agent.PTY, reg), Exec: ex, Callers: reg, sessions: map[string]identity.Caller{}, servers: map[*mcp.ServerSession]bool{}}, nil
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
		protected.ServeHTTP(w, r)
	})
}
func (a *Agent) server(p Principal) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "xmesh-agent", Version: "0.1.0"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18"}, InitializedHandler: func(_ context.Context, req *mcp.InitializedRequest) { a.bind(req.Session, p) }})
	for name, spec := range builtinSpecs() {
		name, spec := name, spec
		server.AddTool(&mcp.Tool{Name: name, Description: spec.desc, InputSchema: spec.schema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			caller := a.bind(req.Session, p)
			if c, ok := a.Callers.Get(caller.ID); ok {
				caller = c
			}
			cfg := a.store.Get()
			if !Allowed(cfg, caller, name, "") {
				return Result(nil, toolerr.New("unauthorized", "调用方的 tag 不允许此工具", "tag", strings.Join(caller.Tags, ","), "tool", name)), nil
			}
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
			}
			return Result(out, e), nil
		})
	}
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, e := next(ctx, method, req)
			if e == nil && method == "tools/list" {
				caller := identity.Caller{Tags: p.Tags}
				if result, ok := result.(*mcp.ListToolsResult); ok {
					cfg := a.store.Get()
					visible := []*mcp.Tool{}
					for _, tool := range result.Tools {
						if Allowed(cfg, caller, tool.Name, "") {
							visible = append(visible, tool)
						}
					}
					result.Tools = visible
				}
			}
			return result, e
		}
	})
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
	for name, b := range c.Agent.Backends {
		if Allowed(c, caller, "", name) {
			backends = append(backends, map[string]any{"name": name, "policy": b.Policy, "favorite": b.Favorite, "state": "stopped"})
		}
	}
	shell, _ := pty.DefaultShell(c.Agent.PTY.DefaultShell)
	shells := []string{}
	if shell != "" {
		shells = append(shells, shell)
	}
	inUse, active, closed := a.PTY.Counts()
	return map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "hostname": c.Node.Name, "home": r.Home, "path_sep": string(os.PathSeparator), "shells": shells, "workspaces": workspaces, "backends": backends, "limits": map[string]any{"exec_shared": c.Agent.Exec.MaxConcurrent, "exec_exclusive": c.Agent.Exec.ExclusiveSlots, "pty_max_sessions": c.Agent.PTY.MaxSessions, "pty_max_per_caller": c.Agent.PTY.MaxPerCaller, "pty_in_use": inUse, "pty_active": active, "pty_closed": closed, "pty_closed_retain_max": c.Agent.PTY.ClosedRetainMax}}
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
	})
}
