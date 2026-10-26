import type { PreferredIPEntry, PreferredIPInspectItem, PreferredIPInspectResponse, PreferredIPState } from "../src/shared/control";

// The fake kernel's preferred-address pool. The pool follows the configuration the panel writes
// (mesh.cloudflare.preferred_ip), so what the page saves shows up in what it reads: manual and legacy
// addresses join the pool, a subscription "fetches" one fixed address on refresh, and a scan adds three.

const PREFIXES = ["173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22"];

function v4(ip: string): number | undefined {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(ip.trim());
  if (!m) return undefined;
  const parts = m.slice(1).map(Number);
  if (parts.some((n) => n > 255)) return undefined;
  return ((parts[0]! << 24) | (parts[1]! << 16) | (parts[2]! << 8) | parts[3]!) >>> 0;
}

export function inCloudflare(ip: string): boolean {
  const n = v4(ip);
  if (n === undefined) return false;
  return PREFIXES.some((p) => {
    const [base, bits] = p.split("/");
    const mask = bits === "0" ? 0 : (~0 << (32 - Number(bits))) >>> 0;
    return (n & mask) >>> 0 === ((v4(base!)! & mask) >>> 0);
  });
}

export interface FakeCloudflare {
  hostname?: string;
  peers?: Record<string, string>;
  preferred_ips?: string[];
  preferred_ips_file?: string;
  preferred_ip?: {
    enabled?: boolean;
    mode?: "smart" | "fixed";
    fixed_ips?: string[];
    no_dns_fallback?: boolean;
    sources?: { type: string; disabled?: boolean; name?: string; ips?: string[]; path?: string; url?: string }[];
    scan?: { enabled?: boolean; interval?: string; sample?: number; keep?: number; host?: string; region_mode?: string; region_count?: number; regions?: string[] };
    peers?: Record<string, { mode?: string; ips?: string[] }>;
  };
}

const iso = (d: Date): string => d.toISOString().replace(/\.\d{3}Z$/, ".000Z");

const COLOS: Record<string, [string, string]> = { "104.18.32.77": ["HKG", "hk"], "172.64.153.21": ["NRT", "jp"], "162.159.136.5": ["LAX", "us-w"] };

export class PreferredFake {
  private scanAt: Date | undefined;
  private fetched = new Map<string, Date>();
  private cooling = new Set<string>();
  /** Test hook: makes the next scan fail the way a missing hostname does. */
  scanError = "";

  setCooling(ip: string, on: boolean): void { if (on) this.cooling.add(ip); else this.cooling.delete(ip); }

  state(cf: FakeCloudflare): PreferredIPState {
    const pp = cf.preferred_ip ?? {};
    const pool: PreferredIPEntry[] = [];
    const seen = new Set<string>();
    const add = (ip: string, source: PreferredIPEntry["source"]): void => {
      if (seen.has(ip) || !inCloudflare(ip)) return;
      seen.add(ip);
      const cooling = this.cooling.has(ip);
      const measured = source === "scan";
      pool.push({ ip, source, status: cooling ? "cooling" : measured ? "ok" : "untested", latency_ms: measured ? 120 + pool.length * 20 : null, loss_percent: measured ? 0 : null, failures: cooling ? 3 : 0, cooldown_until: cooling ? iso(new Date(Date.now() + 1800_000)) : null, last_used_at: null, colo: this.scanAt ? COLOS[ip]?.[0] ?? "" : "", region: this.scanAt ? COLOS[ip]?.[1] ?? "" : "" });
    };
    for (const ip of cf.preferred_ips ?? []) add(ip, "manual");
    const sources = pp.sources ?? [];
    for (const s of sources) {
      if (s.disabled) continue;
      if (s.type === "manual") for (const ip of s.ips ?? []) add(ip, "manual");
      if (s.type === "url" && s.url && this.fetched.has(s.url)) add("104.19.0.7", "url");
    }
    if (pp.scan?.enabled && this.scanAt) for (const ip of ["104.18.32.77", "172.64.153.21", "162.159.136.5"].slice(0, pp.scan.keep ?? 8)) add(ip, "scan");
    const rows = [
      ...((cf.preferred_ips ?? []).length > 0 ? [{ index: -1, type: "manual" as const, name: "preferred_ips", disabled: false, count: (cf.preferred_ips ?? []).length, fetched_at: null, error: "" }] : []),
      ...sources.map((s, index) => {
        const at = s.url ? this.fetched.get(s.url) : undefined;
        const name = s.name ?? (s.url ? new URL(s.url).host : s.path ?? s.type);
        return { index, type: s.type as "manual" | "file" | "url", name, disabled: s.disabled === true, count: s.type === "manual" ? (s.ips ?? []).filter(inCloudflare).length : s.type === "url" && at ? 1 : 0, fetched_at: at ? iso(at) : null, error: "" };
      }),
    ];
    const peers = Object.entries(cf.peers ?? {}).sort().map(([id, hostname]) => {
      const o = pp.peers?.[id];
      const override = o?.mode !== undefined && o.mode !== "default";
      const mode = (override ? o!.mode : pp.mode ?? "smart") as "smart" | "fixed" | "off";
      const ips = mode === "fixed" ? (override ? o!.ips : pp.fixed_ips) ?? [] : [];
      return { device_id: id, hostname, mode: pp.enabled === false ? ("off" as const) : mode, override, ips, current: pool[0] && mode !== "off" ? pool[0].ip : "", last_at: pool[0] ? iso(new Date()) : null };
    });
    const interval = pp.scan?.interval ?? "6h";
    return {
      enabled: pp.enabled !== false, mode: pp.mode ?? "smart", no_dns_fallback: pp.no_dns_fallback === true, pool, sources: rows,
      scan: { enabled: pp.scan?.enabled === true, running: false, sample: pp.scan?.sample ?? 200, host: pp.scan?.host ?? Object.values(cf.peers ?? {}).sort()[0] ?? "", started_at: null, last_at: this.scanAt ? iso(this.scanAt) : null, next_at: this.scanAt && pp.scan?.enabled ? iso(new Date(this.scanAt.getTime() + (interval.endsWith("m") ? 60_000 : 3600_000) * parseInt(interval, 10))) : null, kept: this.scanAt && pp.scan?.enabled ? Math.min(3, pp.scan.keep ?? 8) : 0, error: this.scanError,
        regions: this.scanAt ? [{ key: "hk", name: "港澳台", count: 9, best_ms: 120, colos: ["HKG"] }, { key: "jp", name: "日本", count: 6, best_ms: 140, colos: ["KIX", "NRT"] }, { key: "us-w", name: "美西", count: 4, best_ms: 180, colos: ["LAX"] }] : [],
        chosen: this.scanAt ? ["hk", "jp"] : [], unidentified: this.scanAt ? 2 : 0 },
      peers,
    };
  }

  run(cf: FakeCloudflare, task: string): PreferredIPState {
    if (task === "scan") { if (this.scanError === "") this.scanAt = new Date(); }
    else for (const s of cf.preferred_ip?.sources ?? []) if (s.type === "url" && s.url && !s.disabled) this.fetched.set(s.url, new Date());
    return this.state(cf);
  }

  inspect(cf: FakeCloudflare, text: string): PreferredIPInspectResponse {
    const have = new Set(this.state(cf).pool.map((e) => e.ip));
    const items: PreferredIPInspectItem[] = [];
    const accepted: string[] = [];
    let first = true;
    text.split("\n").forEach((raw, i) => {
      const line = raw.trim();
      if (line === "" || line.startsWith("#")) return;
      const [field = ""] = line.split(",");
      const ip = field.trim();
      const header = first && line.includes(",") && v4(ip) === undefined;
      first = false;
      if (header) return;
      if (!inCloudflare(ip)) items.push({ line: i + 1, ip, status: "rejected", note: `${ip} 不在 Cloudflare 公布的 IP 段内` });
      else if (have.has(ip) || accepted.includes(ip)) items.push({ line: i + 1, ip, status: "existing", note: "已在池中或重复" });
      else { accepted.push(ip); items.push({ line: i + 1, ip, status: "new", note: "" }); }
    });
    return { items, accepted };
  }
}
