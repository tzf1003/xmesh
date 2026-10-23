package devicelink

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/identity"
	"sync/atomic"
	"time"
)

const maxLogicalSessions = 128
const maxControlFrames = 1024

// chunkBytes bounds one data frame on the wire (about 256 KiB after JSON base64) so
// control traffic interleaves between frames even on a slow uplink.
const chunkBytes = 192 << 10
const maxParts = (MaxFrameBytes+chunkBytes-1)/chunkBytes + 1

// assembler joins the consecutive frames of one split packet. The sender never
// interleaves other data frames inside a packet, so one partial per carrier suffices.
type assembler struct {
	session  string
	sequence uint64
	parts    uint32
	next     uint32
	buf      []byte
}

func (a *assembler) pending() bool { return a.buf != nil }

func (a *assembler) reset() { *a = assembler{} }

func (a *assembler) add(v frame) ([]byte, bool, error) {
	if v.Parts <= 1 {
		if a.pending() {
			return nil, false, ErrSequence
		}
		return v.Payload, true, nil
	}
	if v.Parts > maxParts || v.Part >= v.Parts {
		return nil, false, ErrSequence
	}
	if v.Part == 0 {
		if a.pending() {
			return nil, false, ErrSequence
		}
		*a = assembler{session: v.SessionID, sequence: v.Sequence, parts: v.Parts, buf: make([]byte, 0, min(len(v.Payload)*int(v.Parts), MaxFrameBytes))}
	} else if !a.pending() || a.session != v.SessionID || a.sequence != v.Sequence || a.parts != v.Parts || a.next != v.Part {
		return nil, false, ErrSequence
	}
	if len(a.buf)+len(v.Payload) > MaxFrameBytes {
		return nil, false, ErrFrameTooLarge
	}
	a.buf = append(a.buf, v.Payload...)
	a.next = v.Part + 1
	if a.next < a.parts {
		return nil, false, nil
	}
	out := a.buf
	a.reset()
	return out, true, nil
}

// splitData returns the wire frames for one packet.
func splitData(sid string, packet Packet) []frame {
	if len(packet.Payload) <= chunkBytes {
		return []frame{{Type: "data", SessionID: sid, Sequence: packet.Sequence, Payload: packet.Payload}}
	}
	parts := uint32((len(packet.Payload) + chunkBytes - 1) / chunkBytes)
	out := make([]frame, 0, parts)
	for i := uint32(0); i < parts; i++ {
		end := min((int(i)+1)*chunkBytes, len(packet.Payload))
		out = append(out, frame{Type: "data", SessionID: sid, Sequence: packet.Sequence, Payload: packet.Payload[int(i)*chunkBytes : end], Part: i, Parts: parts})
	}
	return out
}

func (p *peerLink) queueLocked(value frame) error {
	if len(p.control) >= maxControlFrames {
		return ErrBackpressure
	}
	p.control = append(p.control, value)
	p.signalLocked()
	return nil
}

func (m *Manager) readCarrier(ctx context.Context, p *peerLink, c *carrier) error {
	var join assembler
	for {
		value, err := readFrame(ctx, c.conn)
		if err != nil {
			return err
		}
		c.seen.Store(time.Now().UnixNano())
		if value.Epoch != c.epoch {
			return ErrSequence
		}
		p.mu.Lock()
		if p.active != c || p.closed {
			p.mu.Unlock()
			return ErrUnavailable
		}
		if m.opts.Directory.IdentityConflict(p.id, time.Now()) {
			p.mu.Unlock()
			return ErrIdentityConflict
		}
		trusted, ok := m.opts.Trust.Find(p.id)
		if !ok && value.Type != "ack" && value.Type != "close" && value.Type != "closed" {
			p.mu.Unlock()
			return ErrUnavailable
		}
		switch value.Type {
		case "shutdown":
			for _, l := range p.sessions {
				closeLogical(l, ErrRestarted)
			}
			for _, l := range p.pending {
				closeLogical(l, ErrRestarted)
			}
			err = ErrRestarted
		case "revoked":
			for _, l := range p.sessions {
				closeLogical(l, device.ErrUntrusted)
			}
			for _, l := range p.pending {
				closeLogical(l, device.ErrUntrusted)
			}
			err = device.ErrUntrusted
		case "open":
			if value.OpenID == "" || len(value.OpenID) > 128 || len(value.OAuthClient) > 256 {
				p.mu.Unlock()
				return ErrSequence
			}
			principal, e := delegatedPrincipal(p.id, trusted.Tags, value.OAuthClient)
			if e != nil {
				err = p.queueLocked(frame{Type: "rejected", OpenID: value.OpenID, Error: "unauthorized"})
				break
			}
			if l := p.opens[value.OpenID]; l != nil {
				if !l.incoming || l.oauth != value.OAuthClient || l.err != nil || l.session.Closed() || l.trustGeneration != trusted.Generation {
					err = ErrSequence
					break
				}
				l.attachedEpoch = c.epoch
				l.detachedAt = time.Time{}
				err = p.queueLocked(frame{Type: "opened", OpenID: l.openID, SessionID: l.session.id, Token: l.token, Ack: l.session.Received()})
				break
			}
			if len(p.sessions) >= maxLogicalSessions || m.opts.Accept == nil {
				err = p.queueLocked(frame{Type: "rejected", OpenID: value.OpenID, Error: "agent_unavailable"})
				break
			}
			id, token := rand.Text(), rand.Text()+rand.Text()
			s := NewSession(id, m.budget)
			l := &logical{session: s, openID: value.OpenID, token: token, oauth: value.OAuthClient, incoming: true, ready: true, changed: make(chan struct{}), trustGeneration: trusted.Generation, attachedEpoch: c.epoch}
			if e := m.opts.Accept(m.ctx, principal, s); e != nil {
				_ = s.Close()
				err = p.queueLocked(frame{Type: "rejected", OpenID: value.OpenID, Error: "agent_unavailable"})
				break
			}
			p.sessions[id], p.opens[l.openID] = l, l
			err = p.queueLocked(frame{Type: "opened", OpenID: l.openID, SessionID: id, Token: token})
		case "opened":
			l := p.pending[value.OpenID]
			if l == nil {
				if existing := p.sessions[value.SessionID]; existing != nil && !existing.incoming && existing.openID == value.OpenID && tokenEqual(existing.token, value.Token) {
					break
				}
				err = p.queueLocked(frame{Type: "close", SessionID: value.SessionID, Token: value.Token})
				break
			}
			if value.SessionID == "" || len(value.SessionID) > 128 || len(value.Token) < 32 || len(value.Token) > 256 {
				err = ErrSequence
				break
			}
			if l.session != nil && (l.session.id != value.SessionID || !tokenEqual(l.token, value.Token)) {
				err = ErrSequence
				break
			}
			if l.session == nil {
				l.session = NewSession(value.SessionID, m.budget)
				l.token = value.Token
				p.sessions[value.SessionID] = l
			}
			l.ready, l.attachedEpoch = true, c.epoch
			delete(p.pending, value.OpenID)
			close(l.changed)
			l.changed = make(chan struct{})
			err = l.session.Acknowledge(value.Ack)
		case "rejected":
			if l := p.pending[value.OpenID]; l != nil {
				closeLogical(l, fmt.Errorf("设备拒绝逻辑会话: %s", value.Error))
				delete(p.pending, value.OpenID)
			}
		case "resume":
			l := p.sessions[value.SessionID]
			if _, e := delegatedPrincipal(p.id, trusted.Tags, value.OAuthClient); e != nil {
				err = p.queueLocked(frame{Type: "ended", SessionID: value.SessionID, Error: "resume_rejected"})
				break
			}
			if l == nil || !l.incoming || l.err != nil || l.session.Closed() || !tokenEqual(l.token, value.Token) || l.oauth != value.OAuthClient || l.trustGeneration != trusted.Generation {
				err = p.queueLocked(frame{Type: "ended", SessionID: value.SessionID, Error: "resume_rejected"})
				break
			}
			if err = l.session.Acknowledge(value.Ack); err != nil {
				break
			}
			l.attachedEpoch = c.epoch
			l.detachedAt = time.Time{}
			err = p.queueLocked(frame{Type: "resumed", SessionID: value.SessionID, Ack: l.session.Received()})
		case "resumed":
			l := p.sessions[value.SessionID]
			if l == nil || l.incoming || l.err != nil {
				err = ErrSequence
				break
			}
			if err = l.session.Acknowledge(value.Ack); err != nil {
				break
			}
			l.attachedEpoch = c.epoch
		case "data":
			l := p.sessions[value.SessionID]
			if l == nil {
				err = p.queueLocked(frame{Type: "ended", SessionID: value.SessionID, Error: "session_closed"})
				break
			}
			if l.attachedEpoch != c.epoch || l.trustGeneration != trusted.Generation {
				err = ErrSequence
				break
			}
			payload, complete, e := join.add(value)
			if e != nil {
				err = e
				break
			}
			if !complete {
				break
			}
			ack, e := l.session.Receive(Packet{Sequence: value.Sequence, Payload: payload})
			if errors.Is(e, ErrBackpressure) {
				err = p.queueLocked(frame{Type: "wait", SessionID: value.SessionID, Sequence: value.Sequence})
				break
			}
			if e != nil {
				err = p.queueLocked(frame{Type: "ended", SessionID: value.SessionID, Error: "session_closed"})
				break
			}
			err = p.queueLocked(frame{Type: "ack", SessionID: value.SessionID, Ack: ack})
		case "ack":
			if l := p.sessions[value.SessionID]; l != nil && l.attachedEpoch == c.epoch && l.err == nil && !l.session.Closed() {
				err = l.session.Acknowledge(value.Ack)
			}
		case "wait":
			err = p.queueLocked(frame{Type: "retry-local", SessionID: value.SessionID, Sequence: value.Sequence})
		case "close":
			if l := p.sessions[value.SessionID]; l != nil {
				if !tokenEqual(l.token, value.Token) {
					err = ErrSequence
					break
				}
				closeLogical(l, context.Canceled)
				delete(p.sessions, value.SessionID)
				delete(p.opens, l.openID)
			}
			err = p.queueLocked(frame{Type: "closed", SessionID: value.SessionID})
		case "closed", "ended":
			if l := p.sessions[value.SessionID]; l != nil {
				closeLogical(l, ErrUnavailable)
				delete(p.sessions, value.SessionID)
				delete(p.opens, l.openID)
			}
		default:
			err = ErrSequence
		}
		p.signalLocked()
		p.mu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (m *Manager) writeCarrier(ctx context.Context, p *peerLink, c *carrier) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(m.opts.Heartbeat)
	defer heartbeat.Stop()
	c.seen.Store(time.Now().UnixNano())
	var pinging atomic.Bool
	// beat checks liveness and starts a ping. It also runs between the fragments of a
	// large packet, so a slow uplink cannot hide a dead link behind one long write.
	beat := func() error {
		silent := time.Since(time.Unix(0, c.seen.Load()))
		if silent >= m.opts.DeadAfter {
			return ErrLinkSilent
		}
		// Show the panel a warning well before the link is given up.
		m.setUnstable(p, c, silent >= m.opts.DeadAfter/3)
		// A pong only proves liveness. It is awaited off the write path, because a
		// slow peer must not stall data and a late pong is not a failure by itself.
		if pinging.CompareAndSwap(false, true) {
			go func() {
				defer pinging.Store(false)
				ping, cancel := context.WithTimeout(ctx, m.opts.DeadAfter)
				defer cancel()
				if c.conn.Ping(ping) == nil {
					c.seen.Store(time.Now().UnixNano())
				}
			}()
		}
		return nil
	}
	sent := map[string]uint64{}
	announced := map[string]bool{}
	resuming := map[string]bool{}
	closing := map[string]bool{}
	retryAfter := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeat.C:
			if err := beat(); err != nil {
				return err
			}
		case <-ticker.C:
		case <-p.wake:
		}
		p.mu.Lock()
		if p.active != c || p.closed {
			p.mu.Unlock()
			return ErrUnavailable
		}
		out := p.control
		p.control = nil
		for _, l := range p.pending {
			if l.err == nil && !announced[l.openID] {
				out = append(out, frame{Type: "open", OpenID: l.openID, OAuthClient: l.oauth})
				announced[l.openID] = true
			}
		}
		for sid, l := range p.sessions {
			if l.err != nil || l.session.Closed() {
				if !closing[sid] {
					out = append(out, frame{Type: "close", SessionID: sid, Token: l.token})
					closing[sid] = true
				}
				continue
			}
			if l.attachedEpoch != c.epoch {
				if !l.incoming && !resuming[sid] {
					out = append(out, frame{Type: "resume", SessionID: sid, Token: l.token, OAuthClient: l.oauth, Ack: l.session.Received()})
					resuming[sid] = true
				}
				continue
			}
			if packet, ok := l.session.Next(0); ok && packet.Sequence != sent[sid] && !time.Now().Before(retryAfter[sid]) {
				out = append(out, splitData(sid, packet)...)
				sent[sid] = packet.Sequence
			}
		}
		p.mu.Unlock()
		for _, value := range out {
			if value.Type == "retry-local" {
				sent[value.SessionID] = 0
				retryAfter[value.SessionID] = time.Now().Add(100 * time.Millisecond)
				continue
			}
			select {
			case <-heartbeat.C:
				if err := beat(); err != nil {
					return err
				}
			default:
			}
			value.Epoch = c.epoch
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := writeFrame(writeCtx, c.conn, value)
			cancel()
			if err != nil {
				return err
			}
			if value.Type == "revoked" {
				return device.ErrUntrusted
			}
		}
	}
}

// delegatedPrincipal authorizes OAuth delegation at the device trust boundary.
// An ingress session receives only its ingress grant, even when the device also
// holds broader peer or admin grants. Plain peer sessions keep their contract.
func delegatedPrincipal(node string, tags []string, oauth string) (identity.Principal, error) {
	if oauth != "" {
		allowed := false
		for _, tag := range tags {
			if tag == "tag:ingress" {
				allowed = true
				break
			}
		}
		if !allowed {
			return identity.Principal{}, device.ErrUntrusted
		}
		tags = []string{"tag:ingress"}
	}
	return identity.FromPeer(node, tags, oauth)
}

// setUnstable records that the carrier has gone quiet, or has recovered, and reports the
// change. It does nothing if c is no longer the active carrier.
func (m *Manager) setUnstable(p *peerLink, c *carrier, unstable bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active != c || !c.committed || p.unstable == unstable {
		return
	}
	p.unstable = unstable
	state := "online"
	if unstable {
		state = "unstable"
	}
	m.activity(Activity{DeviceID: p.id, Path: c.path, Epoch: c.epoch, State: state})
}
