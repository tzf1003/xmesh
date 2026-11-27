//go:build linux || darwin

package delegateguard

import (
	"syscall"
)

type procInfo struct {
	pid    int
	parent int
	pgid   int
	sid    uintptr
	birth  string
}

func supportsTreeSampling() bool { return true }

func selfProcessGroupAndSession(pid int) (int, uintptr) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		pgid = 0
	}
	return pgid, selfSessionID(pid)
}

// refreshLocked snapshots all live processes and updates the tracked PIDs,
// process groups, and sessions belonging to active delegated roots.
// Caller holds g.mu.
func (g *Guard) refreshLocked() []procInfo {
	all, err := treeSnapshot()
	if err != nil {
		return nil
	}
	byPID := make(map[int]procInfo, len(all))
	for _, p := range all {
		byPID[p.pid] = p
	}
	for pid, root := range g.roots {
		if info, ok := byPID[pid]; ok {
			if root.birth == "" {
				root.birth = info.birth
			}
			if root.birth == info.birth {
				g.pids[pid] = info.birth
				if info.pgid > 0 && info.pgid != g.selfPGID {
					g.pgids[info.pgid] = true
				}
				if info.sid != 0 && info.sid != g.selfSID {
					g.sids[info.sid] = true
				}
			}
		}
	}
	for pid, birth := range g.pids {
		if info, ok := byPID[pid]; !ok || info.birth != birth {
			delete(g.pids, pid)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range all {
			if p.pid == g.selfPID {
				continue
			}
			if _, ok := g.pids[p.pid]; ok {
				continue
			}
			parentMatch := false
			if _, ok := g.pids[p.parent]; ok {
				parentMatch = true
			}
			groupMatch := p.pgid > 0 && p.pgid != g.selfPGID && g.pgids[p.pgid]
			sessMatch := p.sid != 0 && p.sid != g.selfSID && g.sids[p.sid]
			if parentMatch || groupMatch || sessMatch {
				g.pids[p.pid] = p.birth
				if p.pgid > 0 && p.pgid != g.selfPGID {
					g.pgids[p.pgid] = true
				}
				if p.sid != 0 && p.sid != g.selfSID {
					g.sids[p.sid] = true
				}
				changed = true
			}
		}
	}
	return all
}

func (g *Guard) isDelegatedPID(pid int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.roots) == 0 {
		return false
	}
	all := g.refreshLocked()
	if _, ok := g.pids[pid]; ok {
		return true
	}
	byPID := make(map[int]procInfo, len(all))
	for _, p := range all {
		byPID[p.pid] = p
	}
	cur := pid
	for steps := 0; steps < len(all)+1 && cur > 1 && cur != g.selfPID; steps++ {
		if _, ok := g.pids[cur]; ok {
			return true
		}
		info, ok := byPID[cur]
		if !ok {
			break
		}
		if (info.pgid > 0 && info.pgid != g.selfPGID && g.pgids[info.pgid]) ||
			(info.sid != 0 && info.sid != g.selfSID && g.sids[info.sid]) {
			return true
		}
		cur = info.parent
	}
	return false
}
