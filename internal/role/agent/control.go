package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/lease"
)

// This file is the Agent side of the local control channel (internal/control).
// Everything here is read-only except Resume, and none of it is reachable from
// the MCP tool table.

func (a *Agent) Draining() bool { return a.draining.Load() }

// Resume undoes Drain: new exec, PTY and backend calls are accepted again.
// Resources a kill-mode drain already released are not restored.
func (a *Agent) Resume() { a.draining.Store(false) }

// DrainCounts reports what a kill-mode drain would end right now: live PTY
// sessions and held leases.
func (a *Agent) DrainCounts() (pty, leases int) {
	inUse, _, _ := a.PTY.Counts()
	return inUse, len(a.backendLeases())
}

// Overview is the Agent's part of the control status. Unlike sys.info it is not
// filtered by the caller's tags: the control channel belongs to the node's own user.
// Warnings, drain and the fields only the control server knows are left empty.
func (a *Agent) Overview() v2.StatusResponse {
	cfg := a.store.Get()
	out := v2.StatusResponse{
		OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: cfg.Node.Name, Roles: cfg.Node.Roles,
		PathSep: string(os.PathSeparator), Shells: []string{}, Workspaces: []v2.Workspace{}, Backends: []v2.Backend{}, Warnings: []v2.Warning{},
		Network: v2.NetworkIdentity{Mode: "local", NodeID: "local", Tags: []string{"tag:peer"}},
	}
	if home, err := os.UserHomeDir(); err == nil {
		out.Home = home
	}
	if network := a.networkInfo.Load(); network != nil {
		out.Network = v2.NetworkIdentity{Mode: network.Mode, NodeID: network.Node, Tags: append([]string{}, network.Tags...)}
	}
	out.RestartRequired, out.PolicyReloadFailures = restartState(a.store)
	out.PolicyReloadFailures += int(cwd.DenyFailures())
	out.AuthkeyReadable = cfg.Mesh.Mode == "tailnet" && readable(cfg.Mesh.AuthkeyFile)
	if resolver, e := cwd.New(cfg.Agent.DefaultCWD, cfg.Agent.Workspaces, cfg.Agent.CWDPolicy); e == nil {
		info := a.info(cfg, identity.Caller{}, resolver)
		out.Home = resolver.Home
		if shells, ok := info["shells"].([]string); ok {
			out.Shells = shells
		}
		if rows, ok := info["workspaces"].([]map[string]any); ok {
			for _, row := range rows {
				w := v2.Workspace{}
				w.Alias, _ = row["alias"].(string)
				w.Desc, _ = row["desc"].(string)
				w.Path, _ = row["path"].(string)
				if _, failed := row["error"]; failed {
					w.Error = "工作目录不可用"
				}
				out.Workspaces = append(out.Workspaces, w)
			}
		}
	}
	inUse, active, closed := a.PTY.Counts()
	shared, exclusive := a.Exec.Counts()
	out.Limits = v2.Limits{
		ExecShared: cfg.Agent.Exec.MaxConcurrent, ExecExclusive: cfg.Agent.Exec.ExclusiveSlots, ExecSharedInUse: shared, ExecExclusiveInUse: exclusive,
		PTYMaxSessions: cfg.Agent.PTY.MaxSessions, PTYMaxPerCaller: cfg.Agent.PTY.MaxPerCaller, PTYInUse: inUse, PTYActive: active, PTYClosed: closed, PTYClosedRetainMax: cfg.Agent.PTY.ClosedRetainMax,
	}
	states := a.Backends.States()
	for name, b := range cfg.Agent.Backends {
		row := v2.Backend{Name: name, Policy: b.Policy, Favorite: b.Favorite, State: states[name]}
		if a.backendEnv != nil && b.Transport == "stdio" {
			row.EnvNames = a.backendEnv.Names(name)
		}
		out.Backends = append(out.Backends, row)
	}
	sort.Slice(out.Backends, func(i, j int) bool { return out.Backends[i].Name < out.Backends[j].Name })
	return out
}

func readable(path string) bool {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func restartState(s *config.Store) (bool, int) {
	restart, failures := s.State()
	return restart, int(failures)
}

// Sessions is the read-only resource view behind GET /v2/sessions. It carries
// ownership and counts, never terminal output or command lines.
func (a *Agent) Sessions() v2.SessionsResponse {
	out := v2.SessionsResponse{PTY: []v2.PtySession{}, Exec: []v2.ExecRun{}, Leases: []v2.BackendLease{}}
	for _, s := range a.PTY.Views() {
		row := v2.PtySession{Kind: "pty", ID: s.ID, Owner: s.Owner, PID: s.PID, State: s.State, IdleS: s.IdleS, Cols: s.Cols, Rows: s.Rows, Cwd: s.CWD, CreatedAt: v2.At(s.Created)}
		if s.Name != "" {
			name := s.Name
			row.Name = &name
		}
		if s.OAuthClient != "" {
			client := s.OAuthClient
			row.OAuthClient = &client
		}
		out.PTY = append(out.PTY, row)
	}
	for _, r := range a.Exec.Runs() {
		state := "queued"
		if r.Running {
			state = "running"
		}
		out.Exec = append(out.Exec, v2.ExecRun{Kind: "exec", ID: "e-" + strconv.FormatUint(r.ID, 10), Caller: r.Caller, Cmd: r.Cmd, Mode: r.Mode, State: state, StartedAt: v2.At(r.Started)})
	}
	for _, l := range a.backendLeases() {
		out.Leases = append(out.Leases, v2.BackendLease{Kind: "lease", Resource: l.Resource, Owner: l.Owner, Token: l.Token, ExpiresAt: v2.At(l.Expires)})
	}
	return out
}

// backendLeases are the leases the control view shows: backend ownership only.
func (a *Agent) backendLeases() []lease.Lease {
	out := []lease.Lease{}
	for _, l := range a.Leases.Active() {
		if strings.HasPrefix(l.Resource, "backend:") {
			out = append(out, l)
		}
	}
	return out
}
