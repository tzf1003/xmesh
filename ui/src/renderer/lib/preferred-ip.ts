// The preferred-address settings (mesh.cloudflare.preferred_ip) as the page edits them. The kernel
// owns the pool and the scores (GET /v2/preferred-ip); this file only reads and writes the settings.

export type PeerMode = "default" | "smart" | "fixed" | "off";
export type SourceType = "manual" | "file" | "url";

export interface Source {
  type: SourceType;
  disabled: boolean;
  name: string;
  ips: string[];
  path: string;
  url: string;
  refresh: string;
}

export interface ScanSettings {
  enabled: boolean;
  interval: string;
  sample: number;
  keep: number;
  ipv6: boolean;
  host: string;
  /** Which data centres a scan keeps addresses from: the fastest regions, only the picked ones, or any. */
  regionMode: RegionMode;
  /** How many regions "auto" keeps. */
  regionCount: number;
  /** Region keys or three-letter colo codes for "pick". */
  regions: string[];
}

export type RegionMode = "auto" | "none" | "pick";

/** Cloudflare data centres grouped for picking; the kernel owns the colo table and sends the key on each address. */
export const REGIONS: readonly { key: string; zh: string; en: string }[] = [
  { key: "hk", zh: "港澳台", en: "HK / Macau / Taiwan" },
  { key: "jp", zh: "日本", en: "Japan" },
  { key: "kr", zh: "韩国", en: "Korea" },
  { key: "sg", zh: "新加坡", en: "Singapore" },
  { key: "sea", zh: "东南亚", en: "Southeast Asia" },
  { key: "us-w", zh: "美西", en: "US West" },
  { key: "us-e", zh: "美东", en: "US East" },
  { key: "eu", zh: "欧洲", en: "Europe" },
  { key: "other", zh: "其他地区", en: "Other" },
];
export const DEFAULT_REGION_COUNT = 2;
export const MAX_REGION_COUNT = 6;

export interface PeerPolicy {
  mode: PeerMode;
  ips: string[];
}

export interface Policy {
  enabled: boolean;
  mode: "smart" | "fixed";
  fixedIps: string[];
  /** Go durations as written in the configuration, kept so a save does not reformat what the user did not touch. */
  attemptTimeout: string;
  failThreshold: number;
  cooldown: string;
  noDnsFallback: boolean;
  sources: Source[];
  scan: ScanSettings;
  peers: Record<string, PeerPolicy>;
}

export const DEFAULT_REFRESH = "12h";
export const SCAN_INTERVALS = ["1h", "3h", "6h", "12h", "24h", "72h", "168h"] as const;
export const REFRESH_INTERVALS = ["1h", "6h", "12h", "24h", "72h", "168h"] as const;
export const MAX_SOURCES = 8;
/** Most addresses one manual source (or fixed list) may hold. */
export const MAX_SOURCE_IPS = 64;

export const defaultPolicy = (): Policy => ({
  enabled: true,
  mode: "smart",
  fixedIps: [],
  attemptTimeout: "4s",
  failThreshold: 3,
  cooldown: "30m",
  noDnsFallback: false,
  sources: [],
  scan: { enabled: false, interval: "6h", sample: 200, keep: 8, ipv6: false, host: "", regionMode: "auto", regionCount: DEFAULT_REGION_COUNT, regions: [] },
  peers: {},
});

export const blankSource = (type: SourceType): Source => ({ type, disabled: false, name: "", ips: [], path: "", url: "", refresh: DEFAULT_REFRESH });

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
const text = (v: unknown, fallback = ""): string => (typeof v === "string" ? v : fallback);
const int = (v: unknown, fallback: number): number => (typeof v === "number" && Number.isFinite(v) ? Math.trunc(v) : fallback);

/** "30m", "4s", "1h30m" to seconds; undefined when it is not a duration. */
export function durationSeconds(value: string): number | undefined {
  const re = /(\d+(?:\.\d+)?)(ms|h|m|s)/g;
  let total = 0;
  let used = 0;
  for (const m of value.matchAll(re)) {
    total += Number(m[1]) * { h: 3600, m: 60, s: 1, ms: 0.001 }[m[2] as "h" | "m" | "s" | "ms"];
    used += m[0].length;
  }
  return used > 0 && used === value.length ? total : undefined;
}

export interface Loaded {
  /** False when the node does not run the schema that has the Cloudflare section. */
  supported: boolean;
  cloudflareEnabled: boolean;
  /** Paired devices that have a tunnel hostname, by device ID. */
  peers: Record<string, string>;
  policy: Policy;
  /** The older preferred_ips and preferred_ips_file fields, which stay as they are. */
  legacyIps: string[];
  legacyFile: string;
}

export function readPolicy(parsed: unknown): Loaded {
  const cfg = isObject(parsed) ? parsed : {};
  const mesh = isObject(cfg.mesh) ? cfg.mesh : {};
  const cf = isObject(mesh.cloudflare) ? mesh.cloudflare : {};
  const pp = isObject(cf.preferred_ip) ? cf.preferred_ip : {};
  const d = defaultPolicy();
  const scan = isObject(pp.scan) ? pp.scan : {};
  const peers: Record<string, string> = {};
  if (isObject(cf.peers)) for (const [id, host] of Object.entries(cf.peers)) if (typeof host === "string") peers[id] = host;
  const policy: Policy = {
    enabled: pp.enabled !== false,
    mode: pp.mode === "fixed" ? "fixed" : "smart",
    fixedIps: strings(pp.fixed_ips),
    attemptTimeout: durationSeconds(text(pp.attempt_timeout)) !== undefined ? text(pp.attempt_timeout) : d.attemptTimeout,
    failThreshold: int(pp.fail_threshold, 0) > 0 ? int(pp.fail_threshold, 0) : d.failThreshold,
    cooldown: durationSeconds(text(pp.cooldown)) !== undefined ? text(pp.cooldown) : d.cooldown,
    noDnsFallback: pp.no_dns_fallback === true,
    sources: (Array.isArray(pp.sources) ? pp.sources : []).filter(isObject).map((s) => ({
      type: s.type === "file" || s.type === "url" ? s.type : "manual",
      disabled: s.disabled === true,
      name: text(s.name),
      ips: strings(s.ips),
      path: text(s.path),
      url: text(s.url),
      refresh: text(s.refresh, DEFAULT_REFRESH),
    })),
    scan: { enabled: scan.enabled === true, interval: text(scan.interval, d.scan.interval), sample: int(scan.sample, 0) > 0 ? int(scan.sample, 0) : d.scan.sample, keep: int(scan.keep, 0) > 0 ? int(scan.keep, 0) : d.scan.keep, ipv6: scan.ipv6 === true, host: text(scan.host), regionMode: scan.region_mode === "none" || scan.region_mode === "pick" ? scan.region_mode : "auto", regionCount: Math.min(MAX_REGION_COUNT, Math.max(1, int(scan.region_count, 0) || DEFAULT_REGION_COUNT)), regions: strings(scan.regions) },
    peers: {},
  };
  if (isObject(pp.peers)) {
    for (const [id, o] of Object.entries(pp.peers)) {
      if (!isObject(o)) continue;
      const mode = o.mode === "smart" || o.mode === "fixed" || o.mode === "off" ? o.mode : "default";
      policy.peers[id] = { mode, ips: strings(o.ips) };
    }
  }
  return { supported: cfg.schema_version === 2, cloudflareEnabled: cf.enabled === true, peers, policy, legacyIps: strings(cf.preferred_ips), legacyFile: text(cf.preferred_ips_file) };
}

/** The value written at /mesh/cloudflare/preferred_ip: only what differs from the defaults. */
export function policyValue(p: Policy): Record<string, unknown> {
  const d = defaultPolicy();
  const out: Record<string, unknown> = {};
  if (!p.enabled) out.enabled = false;
  if (p.mode === "fixed") out.mode = "fixed";
  if (p.fixedIps.length) out.fixed_ips = p.fixedIps;
  if (p.attemptTimeout !== d.attemptTimeout) out.attempt_timeout = p.attemptTimeout;
  if (p.failThreshold !== d.failThreshold) out.fail_threshold = p.failThreshold;
  if (p.cooldown !== d.cooldown) out.cooldown = p.cooldown;
  if (p.noDnsFallback) out.no_dns_fallback = true;
  if (p.sources.length) {
    out.sources = p.sources.map((s) => ({
      type: s.type,
      ...(s.disabled && { disabled: true }),
      ...(s.name !== "" && { name: s.name }),
      ...(s.type === "manual" && { ips: s.ips }),
      ...(s.type === "file" && { path: s.path }),
      ...(s.type === "url" && { url: s.url, ...(s.refresh !== DEFAULT_REFRESH && { refresh: s.refresh }) }),
    }));
  }
  const sc = p.scan;
  if (sc.enabled || sc.host !== "" || sc.ipv6 || sc.interval !== d.scan.interval || sc.sample !== d.scan.sample || sc.keep !== d.scan.keep || sc.regionMode !== "auto" || sc.regionCount !== DEFAULT_REGION_COUNT) {
    out.scan = {
      enabled: sc.enabled,
      ...(sc.interval !== d.scan.interval && { interval: sc.interval }),
      ...(sc.sample !== d.scan.sample && { sample: sc.sample }),
      ...(sc.keep !== d.scan.keep && { keep: sc.keep }),
      ...(sc.ipv6 && { ipv6: true }),
      ...(sc.host !== "" && { host: sc.host }),
      ...(sc.regionMode !== "auto" && { region_mode: sc.regionMode }),
      ...(sc.regionCount !== DEFAULT_REGION_COUNT && { region_count: sc.regionCount }),
      ...(sc.regionMode === "pick" && sc.regions.length > 0 && { regions: sc.regions }),
    };
  }
  const peers = Object.entries(p.peers).filter(([, o]) => o.mode !== "default");
  if (peers.length) out.peers = Object.fromEntries(peers.map(([id, o]) => [id, { mode: o.mode, ...(o.mode === "fixed" && { ips: o.ips }) }]));
  return out;
}

export const samePolicy = (a: Policy, b: Policy): boolean => JSON.stringify(policyValue(a)) === JSON.stringify(policyValue(b));

/** Addresses typed or pasted into a box: separated by newlines, spaces or commas. Validation is the kernel's. */
export function parseIps(input: string): string[] {
  const out: string[] = [];
  for (const part of input.split(/[\s,;]+/)) if (part !== "" && !out.includes(part)) out.push(part);
  return out;
}

/** What a draft cannot be saved with, as a message key the page shows; undefined when it is fine. */
export function policyProblem(p: Policy): "fixed" | "url" | "file" | "peer" | "sources" | "ips" | "region" | undefined {
  if (p.mode === "fixed" && p.fixedIps.length === 0) return "fixed";
  if (p.scan.regionMode === "pick" && p.scan.regions.length === 0) return "region";
  if (p.sources.length > MAX_SOURCES) return "sources";
  for (const s of p.sources) {
    if (s.type === "url" && !/^https:\/\/[^/\s@]+/.test(s.url)) return "url";
    if (s.type === "manual" && s.ips.length > MAX_SOURCE_IPS) return "ips";
    if (s.type === "file" && s.path.trim() === "") return "file";
  }
  for (const o of Object.values(p.peers)) if (o.mode === "fixed" && o.ips.length === 0) return "peer";
  return undefined;
}
