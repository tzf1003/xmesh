import type { CallEventData, Peer, StatusResponse } from "../../shared/control";

export type Point = { x: number; y: number };
export const MAX_TOPOLOGY_PEERS = 8;
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
    let slot = hash % MAX_TOPOLOGY_PEERS;
    for (let n = 0; n < MAX_TOPOLOGY_PEERS && used.has(slot); n++) slot = (slot + 1) % MAX_TOPOLOGY_PEERS;
    if (!used.has(slot)) slots.set(key, slot);
  }
  return slots;
}

/** Self is always supplied by status, even when the peers capability is absent. */
export function topologyPeers(status: StatusResponse, peers: readonly Peer[]): Peer[] {
  const self: Peer = {
    name: status.hostname, self: true, node_id: status.network.node_id, device_id: peers.find((p) => p.self)?.device_id, tags: status.network.tags,
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

export type CallDirection = "all" | "initiated" | "executed";

/** Caller IDs contain a device identity followed by a per-client session ID. */
export function callBelongsTo(call: CallEventData, peer: Peer, direction: CallDirection): boolean {
  const identities = [peer.device_id, peer.node_id, peer.name].filter((id): id is string => Boolean(id));
  const initiated = identities.includes(call.caller.split("/")[0] ?? "");
  const executed = identities.includes(call.device);
  return direction === "initiated" ? initiated : direction === "executed" ? executed : initiated || executed;
}

export const toolName = (tool: string): string => tool.slice(tool.lastIndexOf("/") + 1);

export interface TopologyNode extends Point { id: string; width: number; height: number; peer?: Peer; region?: string }
export interface TopologyEdge { id: string; from: string; to: string; kind: LinkKind; points: Point[]; path: string }
export interface TopologyLayout { width: number; height: number; nodes: TopologyNode[]; edges: TopologyEdge[] }

/** Route groups occupy separate horizontal bands. Shared trunks belong to one source;
 * second-column branches use the space below their group, clear of device cards.
 * This tree layout avoids unrelated crossings without an exponential graph search.
 */
export function layoutTopology(peers: readonly Peer[], slots: ReadonlyMap<string, number>, availableWidth: number): TopologyLayout {
  const width = Math.max(560, availableWidth);
  const ordered = peers.filter(p => !p.self && slots.has(peerKey(p)))
    .sort((a, b) => slots.get(peerKey(a))! - slots.get(peerKey(b))!);
  const groupKey = (p: Peer): string => {
    const kind = linkKind(p);
    return kind === "direct" ? "0:direct" : kind === "derp" ? `1:${p.derp_region ?? "?"}` : `2:${kind}`;
  };
  const groups = [...new Set(ordered.map(groupKey))].sort().map(key => ({
    key, peers: ordered.filter(p => groupKey(p) === key), start: 0, rows: 0,
  }));
  let rows = 0;
  for (const group of groups) {
    const capacity = Math.min(4, 6 - rows);
    group.peers = group.peers.slice(0, capacity * (width >= 700 ? 2 : 1));
    group.rows = Math.min(capacity, group.peers.length);
    group.start = rows;
    rows += group.rows;
  }
  const shown = groups.filter(g => g.rows > 0);
  const hasRelay = shown.some(g => g.key.startsWith("1:"));
  const twoColumns = shown.some(g => g.peers.length > g.rows);
  const widths = [154, ...(hasRelay ? [104] : []), ...(rows ? [132] : []), ...(twoColumns ? [132] : [])];
  const gap = Math.min(90, (width - 48 - widths.reduce((a, b) => a + b, 0)) / Math.max(1, widths.length - 1));
  const contentWidth = widths.reduce((a, b) => a + b, 0) + gap * (widths.length - 1);
  let left = (width-contentWidth)/2;
  const xs = widths.map(w => {
    const x = left + w / 2;
    left += w + gap;
    return x;
  });
  const height = Math.max(360, rows * 96 + 64);
  const top = (height - Math.max(0,rows-1)*96)/2;
  const y = (row: number): number => top + row*96;
  const firstRelay = shown.find(g => g.key.startsWith("1:"));
  const self = peers.find(p => p.self)!;
  const local: TopologyNode = {
    id: peerKey(self), peer: self, x: xs[0]!,
    y: firstRelay ? y(firstRelay.start + (firstRelay.rows - 1) / 2) : height / 2,
    width: 154, height: 58,
  };
  const nodes: TopologyNode[] = [local];
  const edges: TopologyEdge[] = [];
  const addEdge = (from: TopologyNode, to: TopologyNode, kind: LinkKind, via: Point[]): void => {
    const points = [{ x: from.x + from.width / 2, y: from.y }, ...via, { x: to.x - to.width / 2, y: to.y }]
      .filter((point, index, all) => !index || point.x !== all[index - 1]!.x || point.y !== all[index - 1]!.y);
    const path = points.map((point, index) => `${index ? "L" : "M"} ${point.x} ${point.y}`).join(" ");
    edges.push({ id: to.id, from: from.id, to: to.id, kind, points, path });
  };
  const localLane = local.x + local.width / 2 + gap / 2;
  for (const group of shown) {
    let source = local;
    if (group.key.startsWith("1:")) {
      const region = group.key.slice(2);
      source = { id: `relay:${region}`, region, x: xs[1]!, y: y(group.start + (group.rows - 1) / 2), width: 104, height: 48 };
      nodes.push(source);
      addEdge(local, source, "derp", [{ x: localLane, y: local.y }, { x: localLane, y: source.y }]);
    }
    const firstColumn = hasRelay ? 2 : 1;
    const lane = source === local ? localLane : source.x + source.width / 2 + gap / 2;
    group.peers.forEach((peer, index) => {
      const column = Math.floor(index / group.rows);
      const node: TopologyNode = {
        id: peerKey(peer), peer, x: xs[firstColumn + column]!,
        y: y(group.start + index % group.rows), width: 132, height: 58,
      };
      nodes.push(node);
      const via = [{ x: lane, y: source.y }];
      if (column) {
        const bottom = y(group.start + group.rows - 1) + 44;
        const farLane = node.x - node.width / 2 - gap / 2;
        via.push({ x: lane, y: bottom }, { x: farLane, y: bottom }, { x: farLane, y: node.y });
      } else via.push({ x: lane, y: node.y });
      addEdge(source, node, linkKind(peer), via);
    });
  }
  return {width,height,nodes,edges};
}
