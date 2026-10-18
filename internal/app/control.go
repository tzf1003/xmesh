package app

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/backendenv"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/policy"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/role/gateway"
)

// Version is the kernel version reported through the control channel. The main
// package sets it from its link-time build version.
var Version = "dev"

// controlDeps is what Run hands the control channel. The channel is a window
// onto the running node, never a dependency of it (CON-ARCH-10).
type controlDeps struct {
	store      *config.Store
	backendEnv *backendenv.Store // nil: the node cannot keep backend variables
	stateDir   string
	auditDir   string
	log        *audit.Logger
	agent      *agent.Agent // nil on a node without the agent role
	network    mesh.Network // nil in --local mode
	self       identity.Principal
	mode       string
	allowSIDs  []string
	ingress    control.IngressAdmin // nil on a node without the ingress role
	pairing    control.PairingAdmin // nil unless the node runs the LAN network
	drain      func(kill bool)      // Run's own bookkeeping for gateways, beyond the agent
	resume     func()
	// peerInfo is the gateways' sys.info readings by device; ok is false while the node runs no gateway.
	peerInfo func() (readings map[string]gateway.DeviceInfo, ok bool)
	// roles starts, stops and moves the roles that follow the configuration.
	roles *roleRuntime
}

// startControl opens the channel. A failure is reported and otherwise ignored:
// a node without a working UI channel must keep serving.
func startControl(d controlDeps) (stop func()) {
	options := control.Options{Host: &controlHost{d: d, firewall: newFirewallWatch()}, Store: d.store, Version: Version, StateDir: d.stateDir, AuditDir: d.auditDir, Listen: control.ListenOptions{AllowSIDs: d.allowSIDs}, Audit: d.log, Ingress: d.ingress, Pairing: d.pairing}
	options.BackendEnv = d.backendEnv
	if d.agent != nil {
		options.ApplyBackendEnv = d.agent.ApplyBackendEnv
	}
	if d.roles != nil {
		options.Applier = d.roles
	}
	s, e := control.Start(options)
	if e != nil {
		fmt.Fprintln(os.Stderr, "控制通道未启动:", e)
		return func() {}
	}
	d.log.Observe(s.Observe)
	fmt.Fprintln(os.Stderr, "控制通道: "+s.Endpoint().Address())
	unprotect := func() {}
	if ep := s.Endpoint(); ep.Transport == v1.TransportUnix {
		// L2 workspaces that would expose the socket are refused (CON-SEC-12).
		unprotect = policy.ProtectPath(ep.Address())
	}
	return func() {
		unprotect()
		d.log.Observe(nil)
		s.Close()
	}
}

// controlHost adapts the running node to control.Host.
type controlHost struct {
	d        controlDeps
	flagged  atomic.Bool // drain mark of a node without the agent role
	firewall *firewallWatch
}

func (h *controlHost) Status() v1.StatusResponse {
	cfg := h.d.store.Get()
	var st v1.StatusResponse
	if h.d.agent != nil {
		st = h.d.agent.Overview()
	} else {
		restart, failures := h.d.store.State()
		st = v1.StatusResponse{
			OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: cfg.Node.Name, Roles: cfg.Node.Roles, PathSep: string(os.PathSeparator),
			Network:         v1.NetworkIdentity{Mode: h.d.mode, NodeID: h.d.self.Node, Tags: append([]string{}, h.d.self.Tags...)},
			RestartRequired: restart, PolicyReloadFailures: int(failures),
		}
		st.Home, _ = os.UserHomeDir()
	}
	st.Roles = cfg.Node.Roles
	st.PolicyReloadFailures += int(h.d.roles.failureCount())
	st.AuthkeyReadable = authkeyReadable(cfg)
	st.Warnings = h.warnings(cfg, st)
	if listen, serving := h.d.roles.gatewayListen(); serving {
		// What the gateway answers on is where it is bound, which differs from the configuration while
		// a move to a new listen address has failed.
		shown := *cfg
		shown.Gateway.Listen = listen
		st.Gateway = gatewayInfo(&shown, h.d.network)
	}
	if hasRole(cfg.Node.Roles, "ingress") {
		info := &v1.IngressInfo{Listen: cfg.Ingress.Listen}
		if cfg.Ingress.OAuth.Issuer != "" {
			info.Issuer = &cfg.Ingress.OAuth.Issuer
		}
		st.Ingress = info
	}
	return st
}

// warnings are the ones the node itself can tell; the control server adds peer_offline and
// audit_dropped (CTR-CTL-03).
func (h *controlHost) warnings(cfg *config.Config, st v1.StatusResponse) []v1.Warning {
	out := []v1.Warning{}
	add := func(code v1.WarningCode, severity v1.Severity, message string) {
		out = append(out, v1.Warning{Code: code, Severity: severity, Message: message})
	}
	if st.RestartRequired {
		add(v1.WarnRestartRequired, v1.SeverityInfo, "节点、网络或监听配置已变更，需要重启")
	}
	if h.d.store.Failing() {
		add(v1.WarnPolicyReloadFailed, v1.SeverityError, "配置文件当前无法加载，继续使用最近有效配置")
	}
	for _, message := range h.d.roles.problemMessages() {
		add(v1.WarnPolicyReloadFailed, v1.SeverityError, message)
	}
	if h.d.agent != nil && cwd.DenyFailing(cfg.Agent.CWDPolicy.DenyFile) {
		add(v1.WarnPolicyReloadFailed, v1.SeverityError, "禁止目录文件当前无法加载，继续使用最近有效内容")
	}
	if h.d.agent != nil && cfg.Agent.Policy.AllowDangerous {
		add(v1.WarnAllowDangerous, v1.SeverityInfo, "allow_dangerous 已启用")
	}
	if h.d.agent != nil {
		if reason := h.d.agent.SandboxUnavailable(); reason != "" {
			add(v1.WarnSandboxUnavailable, v1.SeverityWarning, "Windows L2 不可用，L2 的 exec 与 PTY 一律被拒绝（sandbox.unavailable）："+reason)
		}
	}
	if h.firewall.Blocked() {
		add(v1.WarnFirewallBlocked, v1.SeverityWarning, "Windows 防火墙阻止了本程序的入站连接，其他设备可能连不上本节点：请在 Windows 安全中心的防火墙设置里删除对 xmesh 的阻止规则")
	}
	if cfg.Mesh.Mode == "tailnet" {
		if h.d.network == nil {
			add(v1.WarnTailnetNotReady, v1.SeverityWarning, "尚未加入 tailnet")
		}
		if !st.AuthkeyReadable {
			add(v1.WarnAuthkeyUnreadable, v1.SeverityWarning, "无法读取 authkey_file")
		}
	}
	l := st.Limits
	if (l.PTYMaxSessions > 0 && l.PTYInUse >= l.PTYMaxSessions) || (l.ExecShared > 0 && l.ExecSharedInUse >= l.ExecShared) || (l.ExecExclusive > 0 && l.ExecExclusiveInUse >= l.ExecExclusive) {
		add(v1.WarnQuotaExhausted, v1.SeverityWarning, "终端或执行配额已满")
	}
	return out
}

func authkeyReadable(cfg *config.Config) bool {
	if cfg.Mesh.Mode != "tailnet" {
		return false
	}
	f, err := os.Open(cfg.Mesh.AuthkeyFile)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func (h *controlHost) Draining() bool {
	if h.d.agent != nil {
		return h.d.agent.Draining()
	}
	return h.flagged.Load()
}

func (h *controlHost) Peers(ctx context.Context) ([]v1.Peer, error) {
	cfg := h.d.store.Get()
	self := v1.Peer{Name: cfg.Node.Name, Self: true, NodeID: h.d.self.Node, Tags: append([]string{}, h.d.self.Tags...), Online: true, Connection: "local", Addrs: []string{}}
	goos, version := runtime.GOOS, Version
	self.OS, self.Version = &goos, &version
	out := []v1.Peer{self}
	if h.d.network == nil {
		return out, nil
	}
	peers, e := h.d.network.Peers(ctx)
	if e != nil {
		return nil, e
	}
	var readings map[string]gateway.DeviceInfo
	haveInfo := false
	if h.d.peerInfo != nil {
		readings, haveInfo = h.d.peerInfo()
	}
	for _, p := range peers {
		if p.Name == cfg.Node.Name || (p.ID != "" && p.ID == h.d.self.Node) {
			continue
		}
		row := v1.Peer{Name: p.Name, NodeID: p.ID, Tags: append([]string{}, p.Tags...), Online: p.Online, Connection: "unknown", Addrs: []string{}}
		switch {
		case !p.Online:
		case p.Direct:
			row.Connection = "direct"
		case p.Relay != "":
			row.Connection = "derp"
		}
		if p.Relay != "" {
			relay := p.Relay
			row.DERPRegion = &relay
		}
		if p.Address != "" {
			row.Addrs = append(row.Addrs, p.Address)
		}
		if p.OS != "" {
			system := p.OS
			row.OS = &system
		}
		if p.Online && haveInfo {
			row.Info = deviceInfoView(readings[p.Name])
		}
		out = append(out, row)
	}
	return out, nil
}

// deviceInfoView is the control view of one device's sys.info reading. A device nobody has read yet
// has null in both fields.
func deviceInfoView(r gateway.DeviceInfo) *v1.PeerInfo {
	out := &v1.PeerInfo{}
	if !r.RefreshedAt.IsZero() {
		at := v1.At(r.RefreshedAt)
		out.RefreshedAt = &at
	}
	if r.Error != "" {
		msg := r.Error
		out.Error = &msg
	}
	return out
}

// readings merges the readings of every gateway on this node.
func readings(gateways []*gateway.Gateway) map[string]gateway.DeviceInfo {
	sets := make([]map[string]gateway.DeviceInfo, 0, len(gateways))
	for _, g := range gateways {
		sets = append(sets, g.DeviceInfo())
	}
	return mergeReadings(sets...)
}

// mergeReadings keeps, per device, the latest successful read; without one, a failure is kept in
// preference to no read at all.
func mergeReadings(sets ...map[string]gateway.DeviceInfo) map[string]gateway.DeviceInfo {
	out := map[string]gateway.DeviceInfo{}
	for _, set := range sets {
		for name, r := range set {
			cur, seen := out[name]
			if !seen || r.RefreshedAt.After(cur.RefreshedAt) || (r.RefreshedAt.Equal(cur.RefreshedAt) && cur.Error == "" && r.Error != "") {
				out[name] = r
			}
		}
	}
	return out
}
func (h *controlHost) Sessions() v1.SessionsResponse {
	if h.d.agent == nil {
		return v1.SessionsResponse{PTY: []v1.PtySession{}, Exec: []v1.ExecRun{}, Leases: []v1.BackendLease{}}
	}
	return h.d.agent.Sessions()
}
func (h *controlHost) Drain(kill bool) (closedPTY, releasedLeases int) {
	if h.d.agent != nil {
		if kill {
			closedPTY, releasedLeases = h.d.agent.DrainCounts()
		}
		h.d.agent.Drain(kill)
	}
	h.flagged.Store(true)
	h.d.drain(kill)
	return closedPTY, releasedLeases
}
func (h *controlHost) Resume() {
	if h.d.agent != nil {
		h.d.agent.Resume()
	}
	h.flagged.Store(false)
	h.d.resume()
}
