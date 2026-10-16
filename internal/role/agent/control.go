package agent

import (
	"sort"

	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/identity"
)

// This file is the Agent side of the local control channel (internal/control).
// Everything here is read-only except Resume, and none of it is reachable from
// the MCP tool table.

// Overview is the Agent's part of the control status. Unlike sys.info it is not
// filtered by the caller's tags: the control channel belongs to the node's own user.
type Overview struct {
	Draining        bool
	RestartRequired bool
	ReloadFailures  uint64
	Warnings        []string
	Network         *NetworkInfo
	Limits          map[string]any
	Backends        []BackendOverview
	Workspaces      []WorkspaceOverview
}
type BackendOverview struct {
	Name     string
	Policy   string
	Favorite bool
	State    string
}
type WorkspaceOverview struct {
	Alias, Desc, Path string
}

// Sessions is the read-only resource view behind GET /v1/sessions. It carries
// ownership and counts, never terminal output or command lines.
type Sessions struct {
	PTY      []map[string]any
	Exec     map[string]int
	Backends []BackendLease
}
type BackendLease struct {
	Name, State, Owner string
}

func (a *Agent) Draining() bool { return a.draining.Load() }

// Resume undoes Drain: new exec, PTY and backend calls are accepted again.
// Resources a kill-mode drain already released are not restored.
func (a *Agent) Resume() { a.draining.Store(false) }

func (a *Agent) Overview() Overview {
	cfg := a.store.Get()
	out := Overview{Draining: a.Draining(), Warnings: []string{}, Network: a.networkInfo.Load(), Limits: map[string]any{}, Backends: []BackendOverview{}, Workspaces: []WorkspaceOverview{}}
	if resolver, e := cwd.New(cfg.Agent.DefaultCWD, cfg.Agent.Workspaces, cfg.Agent.CWDPolicy); e == nil {
		info := a.info(cfg, identity.Caller{}, resolver)
		out.RestartRequired, _ = info["restart_required"].(bool)
		out.ReloadFailures, _ = info["policy_reload_failures"].(uint64)
		if warnings, ok := info["warnings"].([]string); ok {
			out.Warnings = warnings
		}
		if limits, ok := info["limits"].(map[string]any); ok {
			out.Limits = limits
		}
		if rows, ok := info["workspaces"].([]map[string]any); ok {
			for _, row := range rows {
				alias, _ := row["alias"].(string)
				desc, _ := row["desc"].(string)
				path, _ := row["path"].(string)
				out.Workspaces = append(out.Workspaces, WorkspaceOverview{alias, desc, path})
			}
		}
	}
	states := a.Backends.States()
	for name, b := range cfg.Agent.Backends {
		out.Backends = append(out.Backends, BackendOverview{Name: name, Policy: b.Policy, Favorite: b.Favorite, State: states[name]})
	}
	sort.Slice(out.Backends, func(i, j int) bool { return out.Backends[i].Name < out.Backends[j].Name })
	return out
}

func (a *Agent) Sessions() Sessions {
	cfg := a.store.Get()
	shared, exclusive := a.Exec.Counts()
	out := Sessions{PTY: a.PTY.List(), Exec: map[string]int{"shared": cfg.Agent.Exec.MaxConcurrent, "exclusive": cfg.Agent.Exec.ExclusiveSlots, "shared_in_use": shared, "exclusive_in_use": exclusive}, Backends: []BackendLease{}}
	states := a.Backends.States()
	for name := range cfg.Agent.Backends {
		out.Backends = append(out.Backends, BackendLease{Name: name, State: states[name], Owner: a.Leases.Owner("backend:" + name)})
	}
	sort.Slice(out.Backends, func(i, j int) bool { return out.Backends[i].Name < out.Backends[j].Name })
	return out
}
