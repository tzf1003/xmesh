package app

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/backendenv"
	"github.com/tzf1003/xmesh/internal/ci"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/connectivity"
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
	pairing    control.PairingAdmin // nil unless the node runs the LAN network
	drain      func(kill bool)      // Run's own bookkeeping for gateways, beyond the agent
	resume     func()
	isDraining func() bool
	management *sync.Mutex
	// peerInfo is the gateways' sys.info readings by device; ok is false while the node runs no gateway.
	peerInfo func() (readings map[string]gateway.DeviceInfo, ok bool)
	// roles starts, stops and moves the roles that follow the configuration.
	roles *roleRuntime
	// ci supervises the GitHub Actions runners; nil when the node cannot host them.
	ci         *ci.Manager
	ciAccounts *ci.Service
}

// startControl opens the channel. A failure is reported and otherwise ignored:
// a node without a working UI channel must keep serving.
func startControl(d controlDeps) (stop func()) {
	options := control.Options{Host: &controlHost{d: d, firewall: newFirewallWatch()}, Store: d.store, Version: Version, StateDir: d.stateDir, AuditDir: d.auditDir, Listen: control.ListenOptions{AllowSIDs: d.allowSIDs}, Audit: d.log, Pairing: d.pairing}
	if node, ok := d.network.(*connectivity.Node); ok {
		options.Pairing = devicePairing{node: node}
		options.DeviceIdentity = node.Identity()
		options.DeviceTrust = node.Trust()
	}
	options.BackendEnv = d.backendEnv
	if d.agent != nil {
		options.Computer = d.agent.Computer
		options.ApplyBackendEnv = d.agent.ApplyBackendEnv
	}
	if d.ci != nil {
		options.CI = d.ci
		options.CIAccounts = d.ciAccounts
	}
	if d.roles != nil {
		node, _ := d.network.(*connectivity.Node)
		options.Applier = runtimeApplier{roles: d.roles, network: node}
		options.IngressNow = d.roles.ingressAdmin
	}
	s, e := control.Start(options)
	if e != nil {
		fmt.Fprintln(os.Stderr, "控制通道未启动:", e)
		return func() {}
	}
	d.log.Observe(s.Observe)
	fmt.Fprintln(os.Stderr, "控制通道: "+s.Endpoint().Address())
	unprotect := func() {}
	if ep := s.Endpoint(); ep.Transport == v2.TransportUnix {
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

func (h *controlHost) Status() v2.StatusResponse {
	cfg := h.d.store.Get()
	var st v2.StatusResponse
	if h.d.agent != nil {
		st = h.d.agent.Overview()
	} else {
		restart, failures := h.d.store.State()
		st = v2.StatusResponse{
			OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: cfg.Node.Name, Roles: cfg.Node.Roles, PathSep: string(os.PathSeparator),
			Network:         v2.NetworkIdentity{Mode: h.d.mode, NodeID: h.d.self.Node, Tags: append([]string{}, h.d.self.Tags...)},
			RestartRequired: restart, PolicyReloadFailures: int(failures),
		}
		st.Home, _ = os.UserHomeDir()
	}
	st.Roles = cfg.Node.Roles
	if node, ok := h.d.network.(*connectivity.Node); ok {
		st.Network.Mode = cfg.Mesh.NetworkMode()
		for _, provider := range node.ProviderStatuses() {
			if provider.Enabled && !provider.Ready {
				message := string(provider.Kind) + " 路径尚未就绪"
				if provider.Error != "" {
					message += "：" + provider.Error
				}
				st.Warnings = append(st.Warnings, v2.Warning{Code: v2.WarnUnclassified, Severity: v2.SeverityWarning, Message: message})
			}
		}
		for _, issue := range node.ListenerErrors() {
			st.Warnings = append(st.Warnings, v2.Warning{Code: v2.WarnUnclassified, Severity: v2.SeverityWarning, Message: issue.Message})
		}
	}
	st.PolicyReloadFailures += int(h.d.roles.failureCount())
	st.AuthkeyReadable = authkeyReadable(cfg)
	st.Warnings = append(st.Warnings, h.warnings(cfg, st)...)
	if listen, serving := h.d.roles.gatewayListen(); serving {
		// What the gateway answers on is where it is bound, which differs from the configuration while
		// a move to a new listen address has failed.
		shown := *cfg
		shown.Gateway.Listen = listen
		st.Gateway = gatewayInfo(&shown, h.d.network)
	}
	if hasRole(cfg.Node.Roles, "ingress") {
		// What the ingress answers on is the section it runs with, which differs from the file while a
		// move to a new section has failed.
		section := cfg.Ingress
		if running, ok := h.d.roles.ingressConfig(); ok {
			section = running
		}
		info := &v2.IngressInfo{Listen: section.Listen}
		if section.OAuth.Issuer != "" {
			info.Issuer = &section.OAuth.Issuer
		}
		st.Ingress = info
	}
	return st
}

// warnings are the ones the node itself can tell; the control server adds peer_offline and
// audit_dropped (CTR-CTL-03).
func (h *controlHost) warnings(cfg *config.Config, st v2.StatusResponse) []v2.Warning {
	out := []v2.Warning{}
	add := func(code v2.WarningCode, severity v2.Severity, message string) {
		out = append(out, v2.Warning{Code: code, Severity: severity, Message: message})
	}
	if st.RestartRequired {
		add(v2.WarnRestartRequired, v2.SeverityInfo, "节点、网络或监听配置已变更，需要重启")
	}
	if h.d.store.Failing() {
		add(v2.WarnPolicyReloadFailed, v2.SeverityError, "配置文件当前无法加载，继续使用最近有效配置")
	}
	for _, message := range h.d.roles.problemMessages() {
		add(v2.WarnPolicyReloadFailed, v2.SeverityError, message)
	}
	if h.d.agent != nil && cwd.DenyFailing(cfg.Agent.CWDPolicy.DenyFile) {
		add(v2.WarnPolicyReloadFailed, v2.SeverityError, "禁止目录文件当前无法加载，继续使用最近有效内容")
	}
	if h.d.agent != nil && cfg.Agent.Policy.AllowDangerous {
		add(v2.WarnAllowDangerous, v2.SeverityInfo, "allow_dangerous 已启用")
	}
	if h.d.agent != nil {
		if reason := h.d.agent.SandboxUnavailable(); reason != "" {
			add(v2.WarnSandboxUnavailable, v2.SeverityWarning, "Windows L2 不可用，L2 的 exec 与 PTY 一律被拒绝（sandbox.unavailable）："+reason)
		}
	}
	if h.firewall.Blocked() {
		add(v2.WarnFirewallBlocked, v2.SeverityWarning, "Windows 防火墙阻止了本程序的入站连接，其他设备可能连不上本节点：请在 Windows 安全中心的防火墙设置里删除对 xmesh 的阻止规则")
	}
	tailnet, authMethod := cfg.Mesh.Mode == "tailnet", cfg.Mesh.AuthMethod
	if cfg.SchemaVersion == 2 {
		tailnet, authMethod = cfg.Mesh.TailnetEnabled(), cfg.Mesh.Tailnet.AuthMethod
	}
	if tailnet {
		if h.d.network == nil {
			add(v2.WarnTailnetNotReady, v2.SeverityWarning, "尚未加入 tailnet")
		}
		if authMethod != "browser" && !st.AuthkeyReadable {
			add(v2.WarnAuthkeyUnreadable, v2.SeverityWarning, "无法读取 authkey_file")
		}
	}
	l := st.Limits
	if (l.PTYMaxSessions > 0 && l.PTYInUse >= l.PTYMaxSessions) || (l.ExecShared > 0 && l.ExecSharedInUse >= l.ExecShared) || (l.ExecExclusive > 0 && l.ExecExclusiveInUse >= l.ExecExclusive) {
		add(v2.WarnQuotaExhausted, v2.SeverityWarning, "终端或执行配额已满")
	}
	return out
}

func authkeyReadable(cfg *config.Config) bool {
	if cfg.SchemaVersion == 2 {
		if !cfg.Mesh.Tailnet.Enabled || cfg.Mesh.Tailnet.AuthMethod == "browser" {
			return false
		}
		file, err := os.Open(cfg.Mesh.Tailnet.AuthkeyFile)
		if err != nil {
			return false
		}
		_ = file.Close()
		return true
	}
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
	return h.flagged.Load() || (h.d.isDraining != nil && h.d.isDraining())
}

func (h *controlHost) Peers(ctx context.Context) ([]v2.Peer, error) {
	cfg := h.d.store.Get()
	self := v2.Peer{Name: cfg.Node.Name, Self: true, NodeID: h.d.self.Node, Tags: append([]string{}, h.d.self.Tags...), Online: true, Connection: "local", Addrs: []string{}}
	if cfg.SchemaVersion == 2 && strings.HasPrefix(h.d.self.Node, "dev-") {
		self.DeviceID = h.d.self.Node
		self.DisplayName = cfg.Node.Name
		self.TrustState = "self"
		self.ConnectionState = "online"
	}
	goos, version := runtime.GOOS, Version
	self.OS, self.Version = &goos, &version
	out := []v2.Peer{self}
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
		if (cfg.SchemaVersion != 2 && p.Name == cfg.Node.Name) || (p.ID != "" && p.ID == h.d.self.Node) {
			continue
		}
		row := v2.Peer{Name: p.Name, NodeID: p.ID, Tags: append([]string{}, p.Tags...), Online: p.Online, Connection: "unknown", Addrs: []string{}}
		if cfg.SchemaVersion == 2 {
			row.DeviceID = p.ID
			row.DisplayName = p.DisplayName
			row.TrustState = "paired"
			if p.ConnectionState == "identity_conflict" {
				row.TrustState = "identity_conflict"
			}
			row.ActivePath = string(p.ActivePath)
			row.ConnectionState = p.ConnectionState
			for _, path := range p.Paths {
				var verified *v2.Time
				if !path.VerifiedAt.IsZero() {
					t := v2.At(path.VerifiedAt)
					verified = &t
				}
				row.Paths = append(row.Paths, v2.DevicePath{Kind: string(path.Kind), Address: path.Address, Reachable: path.Reachable, VerifiedAt: verified})
				row.Addrs = append(row.Addrs, path.Address)
			}
		}
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
		if p.Address != "" && cfg.SchemaVersion != 2 {
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
func deviceInfoView(r gateway.DeviceInfo) *v2.PeerInfo {
	out := &v2.PeerInfo{}
	if !r.RefreshedAt.IsZero() {
		at := v2.At(r.RefreshedAt)
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
func (h *controlHost) Sessions() v2.SessionsResponse {
	if h.d.agent == nil {
		return v2.SessionsResponse{PTY: []v2.PtySession{}, Exec: []v2.ExecRun{}, Leases: []v2.BackendLease{}}
	}
	return h.d.agent.Sessions()
}
func (h *controlHost) Drain(kill bool) (closedPTY, releasedLeases int) {
	if h.d.management != nil {
		h.d.management.Lock()
		defer h.d.management.Unlock()
	}
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
	if h.d.management != nil {
		h.d.management.Lock()
		defer h.d.management.Unlock()
	}
	if h.d.agent != nil {
		h.d.agent.Resume()
	}
	h.flagged.Store(false)
	h.d.resume()
}
