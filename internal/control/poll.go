package control

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// poll samples node state and publishes what changed. Sampling keeps the Agent, Gateway and mesh
// code free of event plumbing: the stream is derived, so a missed tick only delays an event, and
// a run shorter than one tick can be missed altogether.
type baseline struct {
	status   string
	warnings map[string]v1.Warning
	sessions map[string]v1.Session
}

// baseline records the state events are measured against. Start takes it before serving, so a
// change made right after Start returns is still announced.
func (s *Server) baseline() *baseline {
	st := s.snapshot()
	return &baseline{status: statusKey(st), warnings: warningMap(st.Warnings), sessions: sessionMap(s.sessionsView())}
}

func (s *Server) poll(b *baseline) {
	ticker := time.NewTicker(s.o.Poll)
	defer ticker.Stop()
	status, warnings, sessions := b.status, b.warnings, b.sessions
	s.refreshPeersQuietly()
	peers := peerMap(s.cachedPeers())
	tick := 0
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
		tick++
		st := s.snapshot()
		if key := statusKey(st); key != status {
			status = key
			_ = s.bus.Publish(v1.EventStatus, st)
		}
		next := warningMap(st.Warnings)
		for _, code := range sortedKeys(next) {
			if _, ok := warnings[code]; !ok {
				_ = s.bus.Publish(v1.EventWarning, v1.WarningEventData{Change: "raised", Warning: next[code]})
			}
		}
		for _, code := range sortedKeys(warnings) {
			if _, ok := next[code]; !ok {
				_ = s.bus.Publish(v1.EventWarning, v1.WarningEventData{Change: "cleared", Warning: warnings[code]})
			}
		}
		warnings = next
		now := sessionMap(s.sessionsView())
		s.publishSessions(sessions, now)
		sessions = now
		if tick%5 == 0 {
			s.refreshPeersQuietly()
			after := peerMap(s.cachedPeers())
			s.publishPeers(peers, after)
			peers = after
		}
		s.watchConfigFile()
	}
}

func (s *Server) refreshPeersQuietly() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = s.refreshPeers(ctx)
}

// statusKey is the status without what changes on every read, so unchanged state is not re-sent.
func statusKey(st v1.StatusResponse) string {
	raw, _ := json.Marshal(st)
	return string(raw)
}

func warningMap(list []v1.Warning) map[string]v1.Warning {
	out := map[string]v1.Warning{}
	for _, w := range list {
		out[string(w.Code)] = w
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sessionMap indexes the three views by kind and id. Fields that change on every sample (idle_s,
// expires_at) are zeroed in the copy kept for comparison.
func sessionMap(v v1.SessionsResponse) map[string]v1.Session {
	out := map[string]v1.Session{}
	for _, p := range v.PTY {
		p := p
		out["pty:"+p.ID] = v1.Session{PTY: &p}
	}
	for _, e := range v.Exec {
		e := e
		out["exec:"+e.ID] = v1.Session{Exec: &e}
	}
	for _, l := range v.Leases {
		l := l
		out["lease:"+l.Resource] = v1.Session{Lease: &l}
	}
	return out
}

func sessionComparable(s v1.Session) v1.Session {
	switch {
	case s.PTY != nil:
		c := *s.PTY
		c.IdleS = 0
		return v1.Session{PTY: &c}
	case s.Lease != nil:
		c := *s.Lease
		c.ExpiresAt = v1.Time{}
		return v1.Session{Lease: &c}
	}
	return s
}

func (s *Server) publishSessions(before, after map[string]v1.Session) {
	emit := func(change string, session v1.Session) {
		_ = s.bus.Publish(v1.EventSession, v1.SessionEventData{Change: change, Session: session})
	}
	for _, key := range sortedKeys(after) {
		now := after[key]
		old, ok := before[key]
		switch {
		case !ok:
			emit("opened", now)
		case !reflect.DeepEqual(sessionComparable(old), sessionComparable(now)):
			if now.PTY != nil && now.PTY.State == "closed" && old.PTY.State != "closed" {
				emit("closed", now)
			} else {
				emit("updated", now)
			}
		}
	}
	for _, key := range sortedKeys(before) {
		if _, ok := after[key]; ok {
			continue
		}
		old := before[key]
		if old.Exec != nil {
			emit("closed", old)
		} else {
			emit("released", old)
		}
	}
}

func peerMap(list []v1.Peer) map[string]v1.Peer {
	out := map[string]v1.Peer{}
	for _, p := range list {
		if !p.Self {
			out[p.Name] = p
		}
	}
	return out
}

func (s *Server) publishPeers(before, after map[string]v1.Peer) {
	emit := func(change string, p v1.Peer) {
		_ = s.bus.Publish(v1.EventPeer, v1.PeerEventData{Change: change, Peer: p})
	}
	stable := func(p v1.Peer) v1.Peer { p.LastSeen = nil; return p }
	for _, name := range sortedKeys(after) {
		p := after[name]
		old, ok := before[name]
		switch {
		case !ok:
			emit("added", p)
		case old.Online != p.Online && p.Online:
			emit("online", p)
		case old.Online != p.Online:
			emit("offline", p)
		case !reflect.DeepEqual(stable(old), stable(p)):
			emit("updated", p)
		}
	}
	for _, name := range sortedKeys(before) {
		if _, ok := after[name]; !ok {
			emit("removed", before[name])
		}
	}
}
