import * as fs from "node:fs";
import * as path from "node:path";
import type { ControlEvent, Peer, StatusResponse, Warning } from "../src/shared/control";

// The fake kernel replays the contract's own fixtures
// (docs/contracts/schemas/examples/control/), so it cannot drift from the schemas.

const EXAMPLES = path.join("docs", "contracts", "schemas", "examples", "control");

/** Repository root, found by walking up from this file (source and compiled copies sit at different depths). */
export function repoRoot(from: string = __dirname): string {
  let dir = from;
  for (;;) {
    if (fs.existsSync(path.join(dir, EXAMPLES))) return dir;
    const up = path.dirname(dir);
    if (up === dir) throw new Error(`cannot find ${EXAMPLES} above ${from}`);
    dir = up;
  }
}

export function schemaDir(): string {
  return path.join(repoRoot(), "docs", "contracts", "schemas");
}

export function fixture<T>(name: string): T {
  return JSON.parse(fs.readFileSync(path.join(repoRoot(), EXAMPLES, `${name}.json`), "utf8")) as T;
}

export function statusFixture(variant: "healthy" | "degraded-draining" = "healthy"): StatusResponse {
  return fixture<StatusResponse>(`StatusResponse.${variant}`);
}

export function peersFixture(): Peer[] {
  return fixture<{ peers: Peer[] }>("PeersResponse.mixed").peers;
}

export function eventFixture<T extends ControlEvent>(variant: string): T {
  return fixture<T>(`ControlEvent.${variant}`);
}

/** The healthy fixture with top-level fields replaced. */
export function statusWith(patch: Partial<StatusResponse> = {}): StatusResponse {
  return { ...statusFixture(), ...patch };
}

export function warningOf(
  code: Warning["code"] = "peer_offline",
  severity: Warning["severity"] = "warning",
  message = "对端 mac-dev-01 离线",
): Warning {
  return { code, severity, message };
}

export function peerNamed(name: string, patch: Partial<Peer> = {}): Peer {
  const p = peersFixture().find((x) => x.name === name);
  if (!p) throw new Error(`no fixture peer ${name}`);
  return { ...p, ...patch };
}
