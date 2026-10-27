package devicelink

import (
	"cmp"
	"slices"
	"time"

	"github.com/tzf1003/xmesh/internal/device"
)

// A device pair keeps up to two carriers, one for each dialing direction. The carrier this end dialed is
// its "out" slot and the one the peer dialed its "in" slot. The two ends dial and commit their own slot
// without waiting on each other, so a call in either direction can run on the path its caller measured
// best. A logical session belongs to the slot of the end that opened it; it borrows the other slot's
// carrier while its own is missing and moves back once it is quiet.

// dirIndex indexes per-direction state: 0 for carriers this end dialed, 1 for carriers the peer dialed.
func dirIndex(outgoing bool) int {
	if outgoing {
		return 0
	}
	return 1
}

func (p *peerLink) hasLocked(c *carrier) bool { return c != nil && slices.Contains(p.cars, c) }

func (p *peerLink) removeLocked(c *carrier) {
	p.cars = slices.DeleteFunc(p.cars, func(o *carrier) bool { return o == c })
}

// slotLocked returns the committed carrier of one direction that is not being replaced, and a handshake
// still in progress for that direction.
func (p *peerLink) slotLocked(outgoing bool) (current, reserved *carrier) {
	for _, c := range p.cars {
		if c.outgoing != outgoing || c.draining {
			continue
		}
		if c.committed {
			current = c
		} else {
			reserved = c
		}
	}
	return current, reserved
}

// usable reports whether new traffic may be put on the carrier.
func (c *carrier) usable() bool { return c.committed && !c.draining && !c.suspect.Load() }

func (p *peerLink) usableLocked() bool {
	return slices.ContainsFunc(p.cars, func(c *carrier) bool { return c.usable() })
}

// primaryLocked is the carrier shown as the device's link: a healthy one, the one this end dialed first.
func (p *peerLink) primaryLocked() *carrier {
	rank := func(c *carrier) int {
		r := 0
		if c.usable() {
			r += 2
		}
		if c.outgoing {
			r++
		}
		return r
	}
	var best *carrier
	for _, c := range p.cars {
		if c.committed && !c.draining && (best == nil || rank(c) > rank(best)) {
			best = c
		}
	}
	return best
}

func (p *peerLink) attachedToLocked(c *carrier) int {
	n := 0
	for _, l := range p.sessions {
		if l.attached == c {
			n++
		}
	}
	return n
}

// admitLocked decides whether a handshake may take a slot. A peer without capDualSlot gets one carrier
// for the pair. The end that dials only fills an empty slot, or the one it is replacing; the end that is
// dialed takes the new carrier in place of the old one, whose sessions then move over.
func (p *peerLink) admitLocked(outgoing bool, replace *carrier, dual bool) error {
	if p.closed {
		return ErrUnavailable
	}
	p.legacy = !dual
	if !dual && len(p.cars) > 0 {
		return ErrUnavailable
	}
	current, reserved := p.slotLocked(outgoing)
	if reserved != nil {
		return ErrUnavailable
	}
	if current != nil && outgoing && current != replace {
		return ErrUnavailable
	}
	return nil
}

// desiredLocked is the carrier a logical session should run on: its own slot's, else the other's. A
// session stays put while its carrier is healthy, and leaves a borrowed carrier for its own only when quiet.
func (m *Manager) desiredLocked(p *peerLink, l *logical, now time.Time) *carrier {
	home, _ := p.slotLocked(!l.incoming)
	other, _ := p.slotLocked(l.incoming)
	var best *carrier
	for _, c := range []*carrier{home, other} {
		if c != nil && c.usable() {
			best = c
			break
		}
	}
	cur := l.attached
	if !p.hasLocked(cur) || !cur.committed || cur.draining {
		cur = nil
	}
	switch {
	case best == nil && cur != nil:
		return cur
	case best == nil && home != nil:
		return home
	case best == nil:
		return other
	case cur == nil || cur == best || cur.suspect.Load():
		return best
	case best == home && m.quietLocked(l, now):
		return home
	}
	return cur
}

func (m *Manager) quietLocked(l *logical, now time.Time) bool {
	return l.session != nil && l.session.Idle() && now.Sub(l.active) >= m.opts.MigrateIdle
}

// demandOutLocked reports whether this end has calls to make: an open waiting for a carrier, or a session
// it opened that moved data lately. That is what justifies a carrier of its own beside the peer's.
func (m *Manager) demandOutLocked(p *peerLink, now time.Time) bool {
	for _, l := range p.pending {
		if l.err == nil {
			return true
		}
	}
	for _, l := range p.sessions {
		if !l.incoming && l.err == nil && !l.session.Closed() && now.Sub(l.active) < m.opts.IdleClose {
			return true
		}
	}
	return false
}

// dialPlan says what a dial is for: filling the out slot, or replacing the carrier in it.
type dialPlan struct {
	replace *carrier
	kinds   []device.PathKind
}

// dialPlanLocked decides whether this end should dial now, and for what.
func (m *Manager) dialPlanLocked(p *peerLink, now time.Time) (dialPlan, bool) {
	out, reserved := p.slotLocked(true)
	if reserved != nil {
		return dialPlan{}, false
	}
	if out == nil {
		switch {
		case p.legacy && len(p.cars) > 0:
			return dialPlan{}, false
		case m.demandOutLocked(p, now):
			return dialPlan{}, true
		case !p.usableLocked() && now.Sub(p.lastUsable) >= m.opts.PeerDialDelay:
			return dialPlan{}, true
		}
		return dialPlan{}, false
	}
	if p.legacy {
		return dialPlan{}, false
	}
	if h := p.handoff; h != nil && h.from == out {
		return dialPlan{replace: out, kinds: []device.PathKind{h.to}}, true
	}
	if out.suspect.Load() {
		p.handoff = &handoff{from: out, reason: "unresponsive"}
		// Another path first: the silent one may be what is wrong. Same path last, on a fresh connection.
		order := m.pathOrder()
		if m.smart() {
			order = m.smartOrder(p.id, order)
		}
		order = slices.DeleteFunc(slices.Clone(order), func(k device.PathKind) bool { return k == out.path.Kind })
		if slices.Contains(m.pathOrder(), out.path.Kind) {
			order = append(order, out.path.Kind)
		}
		return dialPlan{replace: out, kinds: order}, true
	}
	return dialPlan{}, false
}

// dialDoneLocked reports whether a dial has no more work to do: the slot it serves is filled or the
// carrier it replaces is gone, or the peer's own carrier covers everything this end wants to call.
func (m *Manager) dialDoneLocked(p *peerLink, plan dialPlan) bool {
	if p.closed {
		return true
	}
	if plan.replace != nil {
		return plan.replace.draining || !p.hasLocked(plan.replace)
	}
	if out, reserved := p.slotLocked(true); out != nil || reserved != nil {
		return true
	}
	return !m.demandOutLocked(p, time.Now()) && p.usableLocked()
}

// maintainLocked retires carriers that have been replaced or have gone unused, and notes when the device
// last had a usable carrier. It runs on every tick of the device loop.
func (m *Manager) maintainLocked(p *peerLink, now time.Time) {
	var live, idle []*carrier
	for _, c := range p.cars {
		if c.usable() {
			p.lastUsable = now
		}
		if c.draining {
			// Replies still queued on it are worth a moment, but not a stuck peer.
			if now.Sub(c.drainSince) >= m.opts.HandoffTimeout || (p.attachedToLocked(c) == 0 && len(c.control) == 0) {
				c.cancel()
				_ = c.conn.CloseNow()
			}
		} else if c.committed {
			live = append(live, c)
			if now.Sub(c.busyAt) >= m.opts.IdleClose {
				idle = append(idle, c)
			}
		}
	}
	// Of two carriers that carry nothing the newer goes, and only the end that dialed it closes it, so the
	// two ends agree on which one stays. A device's only carrier is never closed for idleness.
	if len(live) > 1 && len(idle) > 0 {
		victim := slices.MaxFunc(idle, func(a, b *carrier) int { return cmp.Compare(a.epoch, b.epoch) })
		if victim.outgoing {
			victim.cancel()
			_ = victim.conn.CloseNow()
		}
	}
}

// Link describes one carrier of a device.
type Link struct {
	Path  device.Path
	Epoch uint64
	// Outgoing is true when this end dialed the carrier, the "out" slot.
	Outgoing bool
	// Draining marks a carrier about to close because a newer one took its slot.
	Draining bool
	// Suspect marks a carrier that has gone quiet; its sessions prefer another carrier.
	Suspect  bool
	Sessions int
	Since    time.Time
}

// Links lists the committed carriers of a device, the out slot first.
func (m *Manager) Links(id string) []Link {
	m.mu.Lock()
	p := m.peers[id]
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Link
	for _, c := range p.cars {
		if c.committed {
			out = append(out, Link{Path: c.path, Epoch: c.epoch, Outgoing: c.outgoing, Draining: c.draining, Suspect: c.suspect.Load(), Sessions: p.attachedToLocked(c), Since: c.started})
		}
	}
	slices.SortStableFunc(out, func(a, b Link) int {
		switch {
		case a.Outgoing == b.Outgoing:
			return 0
		case a.Outgoing:
			return -1
		}
		return 1
	})
	return out
}
