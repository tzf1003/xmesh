import type { CallEventData, Peer, StatusResponse } from "../../shared/control";

export type Point = { x: number; y: number };
export const CENTER: Point = { x: 400, y: 295 };
export const PEER_SLOTS: readonly Point[] = [
  { x: 170, y: 205 }, { x: 630, y: 205 }, { x: 665, y: 400 }, { x: 560, y: 505 },
  { x: 350, y: 515 }, { x: 155, y: 460 }, { x: 120, y: 330 }, { x: 470, y: 425 },
];
export const peerKey = (p: Peer): string => p.device_id || p.node_id || p.name;

/** Stable slots survive status/order changes and filtering for the mounted instance. */
export function assignSlots(peers: readonly Peer[], previous: ReadonlyMap<string, number>): Map<string, number> {
  const slots = new Map(previous);
  const keys = new Set(peers.filter((p) => !p.self).map(peerKey));
  for (const key of slots.keys()) if (!keys.has(key)) slots.delete(key);
  for (const key of [...keys].sort()) {
    if (slots.has(key)) continue;
    let hash = 0;
    for (const char of key) hash = (Math.imul(hash, 31) + char.charCodeAt(0)) >>> 0;
    const used = new Set(slots.values());
    let slot = hash % PEER_SLOTS.length;
    for (let n = 0; n < PEER_SLOTS.length && used.has(slot); n++) slot = (slot + 1) % PEER_SLOTS.length;
    if (!used.has(slot)) slots.set(key, slot);
  }
  return slots;
}

/** Self is always supplied by status, even when the peers capability is absent. */
export function topologyPeers(status: StatusResponse, peers: readonly Peer[]): Peer[] {
  const self: Peer = {
    name: status.hostname, self: true, node_id: status.network.node_id, tags: status.network.tags,
    online: true, connection: "local", derp_region: null,
    addrs: peers.find((p) => p.self)?.addrs ?? [], last_seen: null, os: status.os, version: status.control.kernel_version,
  };
  return [self, ...peers.filter((p) => !p.self)];
}

export type LinkKind = "direct" | "derp" | "offline" | "unknown";
export function linkKind(peer: Peer): LinkKind {
  if (!peer.online) return "offline";
  return peer.connection === "direct" ? "direct" : peer.connection === "derp" ? "derp" : "unknown";
}

/** Recent callers are observations, not a list of currently connected clients. */
export function recentCallers(calls: readonly CallEventData[]): string[] {
  return [...new Set(calls.slice().reverse().map((c) => c.oauth_client ?? c.caller))].filter(Boolean).slice(0, 3);
}

export function curve(from: Point, to: Point): string {
  const mid = (from.y + to.y) / 2;
  return `M ${from.x} ${from.y} C ${from.x} ${mid}, ${to.x} ${mid}, ${to.x} ${to.y}`;
}
