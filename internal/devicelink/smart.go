package devicelink

import (
	"cmp"
	"slices"
	"time"

	"github.com/tzf1003/xmesh/internal/device"
)

// Switching tunes how smart mode moves a live carrier. A path must stay clearly better for Sustain
// before the carrier moves, and a carrier is not touched before it has lived Dwell; both guard against
// flapping between two paths that trade places. A failing path skips the wait (EmergencyDwell only).
type Switching struct {
	Every          time.Duration
	Dwell          time.Duration
	EmergencyDwell time.Duration
	Sustain        time.Duration
}

func (s Switching) withDefaults() Switching {
	if s.Every <= 0 {
		s.Every = 5 * time.Second
	}
	if s.Dwell <= 0 {
		s.Dwell = 60 * time.Second
	}
	if s.EmergencyDwell <= 0 {
		s.EmergencyDwell = 10 * time.Second
	}
	if s.Sustain <= 0 {
		s.Sustain = 30 * time.Second
	}
	return s
}

const (
	// A challenger must beat the active path by a quarter and by at least this much to count as better.
	betterFraction = 0.25
	betterMarginMs = 30.0
)

func (m *Manager) smart() bool { return m.opts.Smart != nil && m.opts.Smart() }

// kindState is the best score among a device's known paths of one kind.
type kindState struct {
	score     float64
	scored    bool
	unhealthy bool
}

// kindStates scores each enabled path kind of a device from its recent probes. With reachableOnly
// false, paths that are unreachable this instant still count: a path that keeps dropping is exactly the
// one to judge. A path to move to must be reachable now, or the redial could not use it.
func (m *Manager) kindStates(id string, reachableOnly bool) map[device.PathKind]kindState {
	devices, _ := m.opts.Directory.Snapshot(time.Now())
	states := map[device.PathKind]kindState{}
	for _, d := range devices {
		if d.DeviceID != id || d.IdentityConflict {
			continue
		}
		for _, path := range d.Paths {
			if m.rank(path.Kind) < 0 || (reachableOnly && !path.Reachable) {
				continue
			}
			score, ok := device.Score(path.Kind, path.Quality)
			if !ok {
				continue
			}
			if cur, seen := states[path.Kind]; !seen || score < cur.score {
				states[path.Kind] = kindState{score: score, scored: true, unhealthy: device.Unhealthy(path.Quality)}
			}
		}
	}
	return states
}

// pathState scores the address a carrier actually runs on, so a healthy sibling address of the same
// kind cannot hide that the carrier's own address is failing.
func (m *Manager) pathState(id string, path device.Path) (kindState, bool) {
	devices, _ := m.opts.Directory.Snapshot(time.Now())
	for _, d := range devices {
		if d.DeviceID != id || d.IdentityConflict {
			continue
		}
		for _, q := range d.Paths {
			if q.Kind != path.Kind || q.Address != path.Address {
				continue
			}
			if score, ok := device.Score(q.Kind, q.Quality); ok {
				return kindState{score: score, scored: true, unhealthy: device.Unhealthy(q.Quality)}, true
			}
		}
	}
	return kindState{}, false
}

// smartOrder is the order to try paths when connecting: scored paths first, best score first, then
// the rest in the configured order. With nothing scored yet that is just the configured order.
func (m *Manager) smartOrder(id string, order []device.PathKind) []device.PathKind {
	states := m.kindStates(id, true)
	out := slices.Clone(order)
	slices.SortStableFunc(out, func(a, b device.PathKind) int {
		sa, sb := states[a], states[b]
		switch {
		case sa.scored && sb.scored:
			if sa.score < sb.score {
				return -1
			}
			if sa.score > sb.score {
				return 1
			}
			return 0
		case sa.scored:
			return -1
		case sb.scored:
			return 1
		}
		return 0
	})
	return out
}

type pathSwitch struct {
	id       string
	from, to device.PathKind
	reason   string
}

func (m *Manager) reviewLoop() {
	ticker := time.NewTicker(m.opts.Switching.Every)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		}
		if m.smart() {
			m.review(time.Now())
		}
	}
}

// review moves each carrier this end dialed onto a clearly better path. Only the dialing end judges, so
// the two ends, which measure the path from opposite sides, cannot pull a carrier back and forth.
func (m *Manager) review(now time.Time) {
	var moved []pathSwitch
	m.mu.Lock()
	for _, p := range m.peers {
		p.mu.Lock()
		if sw, ok := m.reviewLocked(p, now); ok {
			moved = append(moved, sw)
		}
		p.mu.Unlock()
	}
	m.mu.Unlock()
	if m.opts.OnPathSwitch != nil {
		for _, sw := range moved {
			m.opts.OnPathSwitch(sw.id, sw.from, sw.to, sw.reason)
		}
	}
}

func (m *Manager) reviewLocked(p *peerLink, now time.Time) (pathSwitch, bool) {
	c := p.active
	reset := func() { p.betterKind, p.betterSince = "", time.Time{} }
	if c == nil || !c.committed || !c.outgoing {
		reset()
		return pathSwitch{}, false
	}
	cur, ok := m.pathState(p.id, c.path)
	if !ok {
		reset()
		return pathSwitch{}, false
	}
	states := m.kindStates(p.id, true)
	var best device.PathKind
	var alt kindState
	for _, kind := range m.pathOrder() {
		st, ok := states[kind]
		if kind == c.path.Kind || !ok || st.unhealthy || (best != "" && st.score >= alt.score) {
			continue
		}
		best, alt = kind, st
	}
	if best == "" {
		reset()
		return pathSwitch{}, false
	}
	lived := now.Sub(c.started)
	move := func(reason string) (pathSwitch, bool) {
		reset()
		c.cancel()
		_ = c.conn.CloseNow()
		return pathSwitch{id: p.id, from: c.path.Kind, to: best, reason: reason}, true
	}
	if cur.unhealthy && alt.score < cur.score {
		if lived >= m.opts.Switching.EmergencyDwell {
			return move("unhealthy")
		}
		return pathSwitch{}, false
	}
	better := alt.score < cur.score*(1-betterFraction) && cur.score-alt.score >= betterMarginMs
	if !better {
		reset()
		return pathSwitch{}, false
	}
	if p.betterKind != best {
		p.betterKind, p.betterSince = best, now
	}
	if lived >= m.opts.Switching.Dwell && now.Sub(p.betterSince) >= m.opts.Switching.Sustain {
		return move("better")
	}
	return pathSwitch{}, false
}

// sortByScore puts the best measured address of each kind first, so the address that made a kind win
// is the one dialed. Unscored addresses keep their order, after the scored ones.
func sortByScore(paths []device.Path) {
	slices.SortStableFunc(paths, func(a, b device.Path) int {
		sa, oka := device.Score(a.Kind, a.Quality)
		sb, okb := device.Score(b.Kind, b.Quality)
		switch {
		case oka && okb:
			return cmp.Compare(sa, sb)
		case oka:
			return -1
		case okb:
			return 1
		}
		return 0
	})
}
