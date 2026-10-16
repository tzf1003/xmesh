package app

import (
	"context"
	"fmt"
	"os"

	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

// Version is the kernel version reported through the control channel. The main
// package sets it from its link-time build version.
var Version = "dev"

// controlDeps is what Run hands the control channel. The channel is a window
// onto the running node, never a dependency of it (CON-ARCH-10).
type controlDeps struct {
	store     *config.Store
	stateDir  string
	auditDir  string
	log       *audit.Logger
	agent     *agent.Agent // nil on a node without the agent role
	network   mesh.Network // nil in --local mode
	self      identity.Principal
	mode      string
	allowSIDs []string
	drain     func(kill bool) // Run's own bookkeeping for gateways, beyond the agent
	resume    func()
}

// startControl opens the channel. A failure is reported and otherwise ignored:
// a node without a working UI channel must keep serving.
func startControl(d controlDeps) (stop func()) {
	options := control.Options{Host: controlHost{d}, Store: d.store, Version: Version, StateDir: d.stateDir, AuditDir: d.auditDir, Listen: control.ListenOptions{AllowSIDs: d.allowSIDs}, Audit: d.log}
	s, e := control.Start(options)
	if e != nil {
		fmt.Fprintln(os.Stderr, "控制通道未启动:", e)
		return func() {}
	}
	d.log.Observe(s.Observe)
	fmt.Fprintln(os.Stderr, "控制通道: "+s.Endpoint().Address)
	unprotect := func() {}
	if ep := s.Endpoint(); ep.Transport == "unix" {
		// L2 workspaces that would expose the socket are refused (CON-SEC-12).
		unprotect = policy.ProtectPath(ep.Address)
	}
	return func() {
		unprotect()
		d.log.Observe(nil)
		s.Close()
	}
}

type controlHost struct{ d controlDeps }

func (h controlHost) Status() control.Status {
	cfg := h.d.store.Get()
	st := control.Status{Node: cfg.Node.Name, Roles: cfg.Node.Roles, Warnings: []string{}, Backends: []control.Backend{}, Workspaces: []control.Workspace{}, Network: &control.Network{Mode: h.d.mode, Node: h.d.self.Node, Tags: append([]string{}, h.d.self.Tags...)}}
	if h.d.agent == nil {
		restart, failures := h.d.store.State()
		st.RestartRequired, st.ReloadFailures = restart, failures
		if restart {
			st.Warnings = append(st.Warnings, "节点、网络或监听配置已变更，需要重启")
		}
		if failures > 0 {
			st.Warnings = append(st.Warnings, "配置热加载曾失败，继续使用最近有效配置")
		}
		return st
	}
	ov := h.d.agent.Overview()
	st.Draining, st.RestartRequired, st.ReloadFailures, st.Warnings, st.Limits = ov.Draining, ov.RestartRequired, ov.ReloadFailures, ov.Warnings, ov.Limits
	for _, b := range ov.Backends {
		st.Backends = append(st.Backends, control.Backend{Name: b.Name, Policy: b.Policy, Favorite: b.Favorite, State: b.State})
	}
	for _, w := range ov.Workspaces {
		st.Workspaces = append(st.Workspaces, control.Workspace{Alias: w.Alias, Desc: w.Desc, Path: w.Path})
	}
	return st
}
func (h controlHost) Peers(ctx context.Context) (control.Peers, error) {
	out := control.Peers{Mode: h.d.mode, Peers: []control.Peer{}}
	if h.d.network == nil {
		return out, nil
	}
	peers, e := h.d.network.Peers(ctx)
	if e != nil {
		return out, e
	}
	for _, p := range peers {
		tags := p.Tags
		if tags == nil {
			tags = []string{}
		}
		out.Peers = append(out.Peers, control.Peer{ID: p.ID, Name: p.Name, Address: p.Address, Tags: tags, Online: p.Online, OS: p.OS, Direct: p.Direct, Relay: p.Relay, Fingerprint: p.Fingerprint})
	}
	return out, nil
}
func (h controlHost) Sessions() control.Sessions {
	out := control.Sessions{PTY: []map[string]any{}, Exec: map[string]int{}, Backends: []control.BackendLease{}}
	if h.d.agent == nil {
		return out
	}
	v := h.d.agent.Sessions()
	out.PTY, out.Exec = v.PTY, v.Exec
	for _, b := range v.Backends {
		out.Backends = append(out.Backends, control.BackendLease{Name: b.Name, State: b.State, Owner: b.Owner})
	}
	return out
}
func (h controlHost) Drain(kill bool) {
	if h.d.agent != nil {
		h.d.agent.Drain(kill)
	}
	h.d.drain(kill)
}
func (h controlHost) Resume() {
	if h.d.agent != nil {
		h.d.agent.Resume()
	}
	h.d.resume()
}
