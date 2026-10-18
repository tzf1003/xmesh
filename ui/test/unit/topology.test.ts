import assert from "node:assert/strict";
import { test } from "node:test";
import { assignSlots, linkKind, peerKey, PEER_SLOTS, recentCallers, topologyPeers } from "../../src/renderer/lib/topology";
import { peersFixture, statusFixture } from "../../testserver/fixtures";

test("peer positions survive order, online state and insertion; slots do not collide", () => {
  const peers = peersFixture();
  const first = assignSlots(peers, new Map());
  const added = { ...peers[1]!, name: "added", node_id: "added" };
  const next = assignSlots([added, ...peers.slice().reverse().map((p) => ({ ...p, online: !p.online }))], first);
  for (const [id, slot] of first) assert.equal(next.get(id), slot);
  assert.equal(new Set(next.values()).size, next.size);
  const overflow = assignSlots(Array.from({ length: 30 }, (_, i) => ({ ...added, node_id: `p${i}` })), new Map());
  assert.equal(overflow.size, PEER_SLOTS.length);
});

test("offline is distinct from pending, and sys.info failure does not change a direct path", () => {
  const p = peersFixture()[1]!;
  assert.equal(linkKind({ ...p, online: false }), "offline");
  assert.equal(linkKind({ ...p, connection: "unknown" }), "unknown");
  assert.equal(linkKind({ ...p, info: { refreshed_at: null, error: "timeout" } }), "direct");
});

test("self is derived from the current kernel rather than stale peer data", () => {
  const status = statusFixture();
  const self = topologyPeers(status, [])[0]!;
  assert.equal(self.name, status.hostname);
  assert.equal(self.self, true);
  assert.equal(topologyPeers(status, peersFixture()).filter((p) => p.self).length, 1);
  assert.equal(peerKey(self), status.network.node_id);
});

test("caller observations are unique, limited and ordered by most recent call", () => {
  const call = { tool: "fs.read", device: "test", caller: "cli", oauth_client: null, status: "ok" as const, error: null, duration_ms: 1 };
  assert.deepEqual(recentCallers([call, { ...call, oauth_client: "codex" }, { ...call, oauth_client: "claude" }, call]), ["cli", "claude", "codex"]);
});
