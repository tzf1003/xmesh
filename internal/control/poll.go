package control

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"time"
)

// poll samples node state and publishes what changed. Sampling keeps the Agent,
// Gateway and mesh code free of event plumbing: the stream is derived, so a
// missed tick only delays an event.
func (s *Server) poll() {
	ticker := time.NewTicker(s.o.Poll)
	defer ticker.Stop()
	st := s.snapshot()
	status := statusView(st)
	warnings := set(st.Warnings)
	ptys := ptyView(s.o.Host.Sessions())
	peers := map[string]Peer(nil)
	if p, ok := s.fetchPeers(); ok {
		peers = peerMap(p)
	}
	tick := 0
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
		tick++
		st = s.snapshot()
		if now := statusView(st); !reflect.DeepEqual(now, status) {
			status = now
			_ = s.bus.Publish("status", now)
		}
		for message := range diffSet(warnings, set(st.Warnings)) {
			_, active := set(st.Warnings)[message]
			_ = s.bus.Publish("warning", map[string]any{"message": message, "active": active})
		}
		warnings = set(st.Warnings)
		now := ptyView(s.o.Host.Sessions())
		for _, id := range sortedKeys(now) {
			if before, ok := ptys[id]; !ok {
				_ = s.bus.Publish("session", sessionEvent("opened", id, now[id]))
			} else if !reflect.DeepEqual(before, now[id]) {
				_ = s.bus.Publish("session", sessionEvent("updated", id, now[id]))
			}
		}
		for _, id := range sortedKeys(ptys) {
			if _, ok := now[id]; !ok {
				_ = s.bus.Publish("session", sessionEvent("released", id, ptys[id]))
			}
		}
		ptys = now
		if tick%5 == 0 {
			if p, ok := s.fetchPeers(); ok {
				next := peerMap(p)
				s.publishPeers(peers, next)
				peers = next
			}
		}
		s.watchConfigFile()
	}
}
func (s *Server) fetchPeers() (Peers, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := s.o.Host.Peers(ctx)
	return p, err == nil
}

func statusView(st Status) map[string]any {
	// Round-trip through JSON so DeepEqual compares plain values.
	raw, _ := json.Marshal(map[string]any{"health": st.Health, "draining": st.Draining, "restart_required": st.RestartRequired, "warnings": st.Warnings, "limits": st.Limits})
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
func set(items []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

// diffSet returns the elements present in exactly one of a and b.
func diffSet(a, b map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range a {
		if _, ok := b[k]; !ok {
			out[k] = struct{}{}
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out[k] = struct{}{}
		}
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

// ptyView keeps only the fields that identify a terminal's lifecycle. idle_s and
// the like change every second and would flood the stream.
func ptyView(v Sessions) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, row := range v.PTY {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}
		out[id] = map[string]any{"name": row["name"], "owner": row["owner"], "state": row["state"]}
	}
	return out
}
func sessionEvent(change, id string, row map[string]any) map[string]any {
	data := map[string]any{"change": change, "session_id": id}
	for _, key := range []string{"name", "owner", "state"} {
		if text, ok := row[key].(string); ok && text != "" {
			data[key] = text
		}
	}
	return data
}
func peerMap(p Peers) map[string]Peer {
	out := map[string]Peer{}
	for _, peer := range p.Peers {
		out[peer.ID] = peer
	}
	return out
}
func (s *Server) publishPeers(before, after map[string]Peer) {
	if before == nil {
		return
	}
	event := func(change string, p Peer) map[string]any {
		return map[string]any{"change": change, "id": p.ID, "name": p.Name, "online": p.Online, "direct": p.Direct, "relay": p.Relay, "tags": p.Tags}
	}
	for _, id := range sortedKeys(after) {
		p := after[id]
		old, ok := before[id]
		switch {
		case !ok:
			_ = s.bus.Publish("peer", event("joined", p))
		case old.Online != p.Online || old.Direct != p.Direct || old.Relay != p.Relay || !reflect.DeepEqual(old.Tags, p.Tags):
			_ = s.bus.Publish("peer", event("updated", p))
		}
	}
	for _, id := range sortedKeys(before) {
		if _, ok := after[id]; !ok {
			p := before[id]
			p.Online = false
			_ = s.bus.Publish("peer", event("left", p))
		}
	}
}

// watchConfigFile announces edits made outside the channel (editor, CLI).
func (s *Server) watchConfigFile() {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	now := s.fileDigest()
	if now == "" || now == s.digest {
		return
	}
	s.digest = now
	restart, _ := s.o.Store.State()
	_ = s.bus.Publish("config", map[string]any{"etag": `"` + now + `"`, "source": "file", "restart_required": restart})
}

// fileDigest is the SHA-256 of the configuration file, or "" if unreadable or static.
func (s *Server) fileDigest() string {
	path := s.o.Store.Path()
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return sum(raw)
}
