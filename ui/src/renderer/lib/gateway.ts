// The local gateway card's data: which addresses to list, what the reach editor holds, and how a
// typed entry is checked. Pure functions; the components only render what these return.

import type { GatewayAddress, GatewayInfo, GatewayUpdateRequest } from "../../shared/control";
import { ACCESS_KEYWORDS, buildListen, isGatewayHost, isIp, listenPort, listenScope, parseCidr } from "../../shared/gateway";
import type { Key } from "../messages";

/** The label of each address kind: 本机, 内网, Tailscale IP, Tailscale 名称, 域名. */
export const ADDRESS_KIND_KEY: Record<GatewayAddress["kind"], Key> = {
  localhost: "gateway.kind.localhost",
  lan: "gateway.kind.lan",
  tailnet_ip: "gateway.kind.tailnet_ip",
  tailnet_name: "gateway.kind.tailnet_name",
  host: "gateway.kind.host",
};

/** The rows to list: the kernel's reachable addresses, or only the localhost URL from a kernel that does not send them. */
export function addressRows(gw: GatewayInfo): GatewayAddress[] {
  return gw.addresses.length > 0 ? gw.addresses : [{ kind: "localhost", host: "127.0.0.1", url: gw.url }];
}

/** The localhost URL: what this machine's own clients use, and what the upgrade notice copies. */
export function localhostUrl(gw: GatewayInfo): string {
  return addressRows(gw).find((a) => a.kind === "localhost")?.url ?? gw.url;
}

/** The reach editor's state. `listen` is kept as the kernel wrote it until the scope toggle changes it. */
export interface ReachDraft {
  lan: boolean;
  tailnet: boolean;
  /** Single addresses and CIDR ranges, in the order they were added. */
  custom: string[];
  hosts: string[];
  listen: string;
}

export function draftFrom(gw: Pick<GatewayInfo, "listen" | "access" | "hosts">): ReachDraft {
  return {
    lan: gw.access.includes("lan"),
    tailnet: gw.access.includes("tailnet"),
    custom: gw.access.filter((a) => !(ACCESS_KEYWORDS as readonly string[]).includes(a)),
    hosts: [...gw.hosts],
    listen: gw.listen,
  };
}

/** The whole-replacement request for a draft. `localhost` is always listed: this machine cannot be switched off. */
export function requestFrom(d: ReachDraft): GatewayUpdateRequest {
  return {
    listen: d.listen,
    access: ["localhost", ...(d.lan ? ["lan"] : []), ...(d.tailnet ? ["tailnet"] : []), ...d.custom],
    hosts: [...d.hosts],
  };
}

export function sameReach(a: ReachDraft, b: ReachDraft): boolean {
  return JSON.stringify(requestFrom(a)) === JSON.stringify(requestFrom(b));
}

/** The draft with the listen scope changed. Choosing the scope it already has keeps the string as it was. */
export function withScope(d: ReachDraft, scope: "all" | "loopback"): ReachDraft {
  const port = listenPort(d.listen);
  if (port === undefined || listenScope(d.listen) === scope) return d;
  return { ...d, listen: buildListen(scope, port) };
}

export { listenScope, listenPort };

export type EntryCheck = { ok: true; value: string } | { ok: false; problem: "empty" | "invalid" | "duplicate" };

function sameEntry(a: string, b: string): boolean {
  return a.toLowerCase() === b.toLowerCase();
}

/** A single IP address or a CIDR range typed for the access list. Keywords are ticked boxes, not entries. */
export function checkAccessEntry(input: string, existing: readonly string[]): EntryCheck {
  const value = input.trim();
  if (value === "") return { ok: false, problem: "empty" };
  if (!isIp(value) && parseCidr(value) === undefined) return { ok: false, problem: "invalid" };
  if (existing.some((e) => sameEntry(e, value))) return { ok: false, problem: "duplicate" };
  return { ok: true, value };
}

/** A domain name typed for the host list. An IP address is refused here, and so is a mistyped one like 10.0.0. */
export function checkHostEntry(input: string, existing: readonly string[]): EntryCheck {
  const value = input.trim();
  if (value === "") return { ok: false, problem: "empty" };
  if (!isGatewayHost(value)) return { ok: false, problem: "invalid" };
  if (existing.some((e) => sameEntry(e, value))) return { ok: false, problem: "duplicate" };
  return { ok: true, value };
}
