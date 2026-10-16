import type { Peer, StatusResponse } from "../../shared/control";

export type ConnectionKind = Peer["connection"];

/** Self first, then online before offline, then by name. */
export function sortPeers(peers: readonly Peer[]): Peer[] {
  return peers.slice().sort((a, b) => {
    if (a.self !== b.self) return a.self ? -1 : 1;
    if (a.online !== b.online) return a.online ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
}

/** A peer without `tag:device` cannot be discovered as a device (CTR-AUTHZ-01); worth saying so. */
export function lacksDeviceTag(p: Peer): boolean {
  return !p.self && !p.tags.includes("tag:device");
}

export interface RelayStats {
  direct: number;
  derp: number;
  unknown: number;
}

export function relayStats(peers: readonly Peer[]): RelayStats {
  const s: RelayStats = { direct: 0, derp: 0, unknown: 0 };
  for (const p of peers) {
    if (p.self) continue;
    if (p.connection === "direct") s.direct++;
    else if (p.connection === "derp") s.derp++;
    else s.unknown++;
  }
  return s;
}

/** The text of the first warning of the highest severity, for the overview banner. */
export function worstWarning(status: StatusResponse): StatusResponse["warnings"][number] | undefined {
  const rank = { error: 2, warning: 1, info: 0 } as const;
  return status.warnings
    .slice()
    .sort((a, b) => (rank[b.severity] ?? 0) - (rank[a.severity] ?? 0))[0];
}
