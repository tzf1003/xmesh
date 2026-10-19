package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"strings"
)

// Transport provides an in-process Agent connection with a distinct session.
// The caller identity is supplied by the process-owned network boundary.
func (a *Agent) Transport(ctx context.Context, p Principal) (mcp.Transport, error) {
	serverSide, clientSide := mcp.NewInMemoryTransports()
	id := rand.Text()
	_, e := a.server(p).Connect(ctx, &mcpmux.SessionTransport{Base: serverSide, ID: id}, nil)
	if e != nil {
		return nil, e
	}
	return &mcpmux.SessionTransport{Base: clientSide, ID: id}, nil
}

// ConnectDevice attaches a process-authenticated, stable device transport. The connectivity
// boundary validates trust and isolates this logical session from physical path changes.
func (a *Agent) ConnectDevice(ctx context.Context, p Principal, transport mcp.Transport) (*mcp.ServerSession, error) {
	if !identity.Valid(p.Node + "/init") {
		return nil, toolerr.New("unauthenticated", "设备身份无效")
	}
	return a.server(p).Connect(ctx, transport, nil)
}

func (a *Agent) upstream(caller string) *mcp.ServerSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	for s := range a.servers {
		if c, ok := a.sessions[s.ID()]; ok && c.ID == caller {
			return s
		}
	}
	return nil
}
func backendToolAllowed(cfg *config.Config, caller identity.Caller, name string) bool {
	for _, g := range grantsFor(cfg, caller) {
		for _, p := range g.Tools {
			if p == "*" || p == name+".*" || p == name+"." {
				return true
			}
		}
	}
	return false
}
func (a *Agent) toolAllowed(cfg *config.Config, caller identity.Caller, name string) bool {
	prefix, _, _ := strings.Cut(name, ".")
	if _, builtin := builtinSpecs()[name]; builtin {
		return Allowed(cfg, caller, name, "")
	}
	return Allowed(cfg, caller, name, "") && Allowed(cfg, caller, name, prefix)
}
func (a *Agent) installBackends(server *mcp.Server, p Principal) {
	tools := a.Backends.Tools()
	names := []string{}
	cfg := a.store.Get()
	caller := identity.Caller{Tags: p.Tags}
	for _, tool := range tools {
		if !a.toolAllowed(cfg, caller, tool.Name) {
			continue
		}
		tool := tool
		name := tool.Name
		names = append(names, name)
		server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if a.draining.Load() {
				return Result(nil, toolerr.New("policy_denied", "节点正在下线", "rule", "mesh.draining", "reason", "停止接受新的后端调用")), nil
			}
			caller := a.bind(req.Session, p)
			if current, ok := a.Callers.Get(caller.ID); ok {
				caller = current
			}
			cfg := a.store.Get()
			a.Backends.Configure(cfg)
			if !a.toolAllowed(cfg, caller, name) {
				return Result(nil, toolerr.New("unauthorized", "调用方的 tag 不允许此后端工具", "tag", strings.Join(caller.Tags, ","), "tool", name)), nil
			}
			raw := req.Params.Arguments
			if len(raw) == 0 {
				raw = json.RawMessage(`{}`)
			}
			out, e := a.Backends.Call(ctx, caller.ID, name, raw, mcpmux.Progress{Original: req.Params.GetProgressToken(), Send: req.Session.NotifyProgress})
			if e != nil {
				return Result(nil, e), nil
			}
			return out, nil
		})
	}
	a.backendNames[server] = names
}
func (a *Agent) refreshBackends() {
	a.viewMu.Lock()
	defer a.viewMu.Unlock()
	for key, view := range a.views {
		var p Principal
		_ = json.Unmarshal([]byte(key), &p)
		old := a.backendNames[view]
		view.RemoveTools(old...)
		a.installBackends(view, p)
	}
}

// refreshBuiltins applies agent.disabled_tools to every open view. It runs only when the configuration
// changes, so a backend coming or going does not re-announce the built-in tools.
func (a *Agent) refreshBuiltins() {
	a.viewMu.Lock()
	defer a.viewMu.Unlock()
	for key, view := range a.views {
		var p Principal
		_ = json.Unmarshal([]byte(key), &p)
		a.installBuiltins(view, p)
	}
}
