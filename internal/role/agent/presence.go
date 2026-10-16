package agent

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/identity"
)

// PresenceFunc reports which node ids are online. authoritative means every
// node on the network is listed, so a missing node has really left; otherwise a
// missing node is only unobserved and is acted on only after it was seen online.
type PresenceFunc func(context.Context) (online map[string]bool, authoritative bool, err error)

// peerAbsentPolls is how many consecutive polls a node must be missing before
// its sessions end, so a single flapping status read does not tear down work.
const peerAbsentPolls = 2

// PeerLeft ends every MCP session whose caller_id belongs to node, exactly as if
// each client had disconnected: PTYs are orphaned, per-caller backends released
// and leases stop being renewed (CTR-ID-01, node leaves the network). It
// returns the number of sessions closed.
func (a *Agent) PeerLeft(node string) int {
	a.mu.Lock()
	victims := []*mcp.ServerSession{}
	for s := range a.servers {
		if c, ok := a.sessions[s.ID()]; ok && identity.Node(c.ID) == node {
			victims = append(victims, s)
		}
	}
	a.mu.Unlock()
	for _, s := range victims {
		_ = s.Close()
	}
	return len(victims)
}

// WatchPeers polls presence until the agent closes and ends the sessions of
// nodes that dropped off the network. A failed poll is treated as "unknown", so
// an outage of the control plane never orphans anyone.
func (a *Agent) WatchPeers(presence PresenceFunc, interval time.Duration) {
	a.watchers.Add(1)
	go func() {
		defer a.watchers.Done()
		seen, absent := map[string]bool{}, map[string]int{}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-a.stop:
				return
			case <-ticker.C:
			}
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			online, authoritative, err := presence(ctx)
			cancel()
			if err != nil {
				continue
			}
			a.reconcilePeers(online, authoritative, seen, absent)
		}
	}()
}

func (a *Agent) reconcilePeers(online map[string]bool, authoritative bool, seen map[string]bool, absent map[string]int) {
	self := ""
	if network := a.networkInfo.Load(); network != nil {
		self = network.Node
	}
	a.mu.Lock()
	nodes := map[string]bool{}
	for _, c := range a.sessions {
		nodes[identity.Node(c.ID)] = true
	}
	a.mu.Unlock()
	for node := range nodes {
		switch {
		case node == "local" || node == self:
			// The loopback path and this node's own sessions never leave the network.
		case online[node]:
			seen[node], absent[node] = true, 0
		case authoritative || seen[node]:
			if absent[node]++; absent[node] >= peerAbsentPolls {
				a.PeerLeft(node)
				delete(seen, node)
				delete(absent, node)
			}
		}
	}
	for node := range seen {
		if !nodes[node] {
			delete(seen, node)
			delete(absent, node)
		}
	}
}
