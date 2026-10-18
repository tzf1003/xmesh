// Validation of the gateway's reach settings (CTR-CTL-13 and config.schema.json gateway.*). Pure
// functions with no Node or DOM dependency: the main process checks what the renderer sends, the
// renderer checks what the user types, and the unit tests check both against the same rules.

/** Keywords of `gateway.access`; anything else in the list is a single IP address or a CIDR range. */
export const ACCESS_KEYWORDS = ["localhost", "lan", "tailnet"] as const;

/** `gateway.listen` as the contract allows it: all interfaces or loopback only, with a port 1..65535. */
const LISTEN = /^(:|0\.0\.0\.0:|\[::\]:|127\.0\.0\.1:|\[::1\]:)([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$/;

const IPV4_OCTET = /^(0|[1-9][0-9]{0,2})$/;
const HOST_LABEL = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;
/** Characters a token may contain: base64url, plus the few symbols a kernel might use. Nothing that means something in a shell or TOML string. */
const TOKEN = /^[A-Za-z0-9._~+/=_-]{8,512}$/;

export function isIPv4(s: string): boolean {
  const parts = s.split(".");
  return parts.length === 4 && parts.every((p) => IPV4_OCTET.test(p) && Number(p) <= 255);
}

/** Number of 16-bit groups in a run of hex groups (no "::" inside), or undefined when a group is malformed. */
function ipv6Groups(part: string): number | undefined {
  if (part === "") return 0;
  const groups = part.split(":");
  return groups.every((g) => /^[0-9A-Fa-f]{1,4}$/.test(g)) ? groups.length : undefined;
}

/** RFC 4291 text form, with an optional embedded IPv4 tail. Zone identifiers ("%eth0") are refused. */
export function isIPv6(s: string): boolean {
  if (!/^[0-9A-Fa-f:.]+$/.test(s) || !s.includes(":")) return false;
  let text = s;
  const lastColon = text.lastIndexOf(":");
  const tail = text.slice(lastColon + 1);
  if (tail.includes(".")) {
    if (!isIPv4(tail)) return false;
    // An IPv4 tail takes the place of two groups.
    text = `${text.slice(0, lastColon + 1)}0:0`;
  }
  const halves = text.split("::");
  if (halves.length > 2) return false;
  if (halves.length === 2) {
    const head = ipv6Groups(halves[0]!);
    const rest = ipv6Groups(halves[1]!);
    // "::" stands for at least one group.
    return head !== undefined && rest !== undefined && head + rest <= 7;
  }
  return ipv6Groups(text) === 8;
}

export function isIp(s: string): boolean {
  return isIPv4(s) || isIPv6(s);
}

/** `address/prefix` with a valid address and a prefix within the family's bit count. */
export function parseCidr(s: string): { ip: string; prefix: number } | undefined {
  const m = /^([^/]+)\/([0-9]{1,3})$/.exec(s);
  if (!m) return undefined;
  const ip = m[1]!;
  const prefix = Number(m[2]);
  if (isIPv4(ip) && prefix <= 32) return { ip, prefix };
  if (isIPv6(ip) && prefix <= 128) return { ip, prefix };
  return undefined;
}

/** A DNS host name for `gateway.hosts`. Anything that is an IP address is refused, and so is a name whose last label is all digits (a mistyped address such as 10.0.0). */
export function isGatewayHost(s: string): boolean {
  if (s.length === 0 || s.length > 253 || isIp(s)) return false;
  const labels = s.split(".");
  if (!labels.every((l) => HOST_LABEL.test(l))) return false;
  return !/^[0-9]+$/.test(labels[labels.length - 1]!);
}

/** One entry of `gateway.access`: a keyword, a single address, or a CIDR range. */
export function isAccessEntry(s: string): boolean {
  return (ACCESS_KEYWORDS as readonly string[]).includes(s) || isIp(s) || parseCidr(s) !== undefined;
}

export function isListen(s: string): boolean {
  return LISTEN.test(s);
}

/** "all" for every interface (`:8080`, `0.0.0.0:8080`, `[::]:8080`), "loopback" for `127.0.0.1` and `[::1]`, undefined for anything else. */
export function listenScope(listen: string): "all" | "loopback" | undefined {
  if (!isListen(listen)) return undefined;
  return /^(:|0\.0\.0\.0:|\[::\]:)/.test(listen) ? "all" : "loopback";
}

/** The port of a valid `listen` value. */
export function listenPort(listen: string): string | undefined {
  return isListen(listen) ? listen.slice(listen.lastIndexOf(":") + 1) : undefined;
}

/** The canonical `listen` string for a scope and port: `:8080` or `127.0.0.1:8080`. */
export function buildListen(scope: "all" | "loopback", port: string): string {
  return scope === "all" ? `:${port}` : `127.0.0.1:${port}`;
}

/** Whether `s` is safe to write inside the copied connect commands (double-quoted in a shell, a TOML string). */
export function isSafeToken(s: string): boolean {
  return TOKEN.test(s);
}
