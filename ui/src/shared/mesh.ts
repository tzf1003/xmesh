// Tailnet administration uses fixed xmesh mesh commands. A login submits its
// secret once through stdin; the CLI validates and stores it in a private file.
// Other requests carry validated values and the credential file path.

import { isAbsolutePath } from "./setup";

// ---- requests ----

export const MESH_TEMPLATES = ["personal", "team", "restricted"] as const;
export type MeshTemplate = (typeof MESH_TEMPLATES)[number];

export interface DerpRegionInput {
  /** Short region code, for example `lab`. */
  code: string;
  name: string;
  host: string;
  ipv4?: string;
  ipv6?: string;
  /** Custom region ids are 900-999; left out, the CLI picks a free one. */
  id?: number;
  derpPort?: number;
  stunPort?: number;
  certName?: string;
}

export type MeshControlPlane = "tailscale" | "headscale";
export type MeshAccess = "read" | "write";

export interface PolicyDocumentEdit {
  text: string;
  template?: MeshTemplate;
  owner?: string;
  port?: number;
  regions?: DerpRegionInput[];
  region?: { id: number; text: string };
  removeID?: number;
  omitDefault?: boolean;
}

export const POLICY_TEXT_MAX_BYTES = 256 * 1024;
const policyTextWithinLimit = (text: string): boolean => new TextEncoder().encode(text).byteLength <= POLICY_TEXT_MAX_BYTES;

export interface MeshPolicyDocument {
  text: string;
  regions: { id: number; code: string; name: string; hosts: string[]; text: string }[];
  omitDefault: boolean;
}

export type MeshRequest =
  | { op: "login"; kind: MeshControlPlane; access: MeshAccess; url?: string; tailnet?: string; clientId?: string; secret: string }
  | { op: "status" }
  | { op: "tags"; device: string; tags: string[]; apply: boolean }
  | { op: "onboard"; tags: string[]; ttl: string; ephemeral: boolean; user?: string; file: string; overwrite: boolean }
  | { op: "policy.show" }
  | { op: "policy.document"; edit: PolicyDocumentEdit }
  | { op: "policy.publish"; text: string; expectVersion: string; apply: boolean }
  | { op: "policy.snapshots" }
  | { op: "policy.template"; template: MeshTemplate; owner: string; port?: number; apply: boolean; expectVersion?: string }
  | { op: "policy.rollback"; snapshot: string; apply: boolean; expectVersion?: string }
  | { op: "matrix"; port?: number; mismatchOnly: boolean }
  | { op: "derp.plan"; regions: DerpRegionInput[]; omitDefault: boolean; apply: boolean; expectVersion?: string; skipStun: boolean }
  | { op: "derp.probe"; regions: DerpRegionInput[]; skipStun: boolean; compareOfficial: boolean }
  | { op: "derp.headscale"; regions: DerpRegionInput[]; omitDefault: boolean }
  | { op: "derp.sync"; target: { allowlist: string } | { url: string; tokenFile: string } | { url: string; token: string }; allDevices: boolean };

export type MeshOp = MeshRequest["op"];

export const MESH_OPS = [
  "login",
  "status",
  "tags",
  "onboard",
  "policy.show",
  "policy.document",
  "policy.publish",
  "policy.snapshots",
  "policy.template",
  "policy.rollback",
  "matrix",
  "derp.plan",
  "derp.probe",
  "derp.headscale",
  "derp.sync",
] as const satisfies readonly MeshOp[];

// ---- validation ----

const DEVICE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const TAG = /^tag:[A-Za-z0-9][A-Za-z0-9-]{0,62}$/;
const TTL = /^[1-9][0-9]{0,3}(m|h)$/;
const OWNER = /^(autogroup:[a-z]{1,32}|group:[A-Za-z0-9._-]{1,64}|tag:[A-Za-z0-9-]{1,63}|[A-Za-z0-9._%+-]{1,64}@([A-Za-z0-9.-]{1,128})?)$/;
const USER = /^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$/;
const SNAPSHOT_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const VERSION = /^[\x21-\x7e]{1,256}$/;
const REGION_CODE = /^[a-z0-9][a-z0-9-]{0,31}$/;
const REGION_NAME = /^[^,=\x00-\x1f\x7f]{1,64}$/;
const HOSTNAME = /^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$/;
const IPV4 = /^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/;
const IPV6 = /^[0-9A-Fa-f:.]{2,45}$/;
const SECRET = /^[\x21-\x7e]{1,4096}$/;
const TAILNET = /^[A-Za-z0-9-][A-Za-z0-9._@-]{0,127}$/;
const CLIENT_ID = /^[A-Za-z0-9._-]{1,128}$/;

export const MESH_TAGS_MAX = 16;
export const MESH_REGIONS_MAX = 8;
export const MESH_PORT_MIN = 1;
export const MESH_PORT_MAX = 65535;
export const MESH_REGION_ID_MIN = 900;
export const MESH_REGION_ID_MAX = 999;

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function onlyKeys(v: Record<string, unknown>, allowed: readonly string[]): boolean {
  return Object.keys(v).every((k) => allowed.includes(k));
}

function port(v: unknown): number | undefined {
  return typeof v === "number" && Number.isInteger(v) && v >= MESH_PORT_MIN && v <= MESH_PORT_MAX ? v : undefined;
}

export function parseMeshTags(v: unknown, allowEmpty: boolean): string[] | undefined {
  if (!Array.isArray(v) || v.length > MESH_TAGS_MAX || (!allowEmpty && v.length === 0)) return undefined;
  const out: string[] = [];
  for (const t of v) {
    if (typeof t !== "string" || !TAG.test(t) || out.includes(t)) return undefined;
    out.push(t);
  }
  return out;
}

export function parseDerpRegion(v: unknown): DerpRegionInput | undefined {
  if (!isRecord(v) || !onlyKeys(v, ["code", "name", "host", "ipv4", "ipv6", "id", "derpPort", "stunPort", "certName"])) return undefined;
  const { code, name, host } = v;
  if (typeof code !== "string" || !REGION_CODE.test(code)) return undefined;
  if (typeof name !== "string" || !REGION_NAME.test(name)) return undefined;
  if (typeof host !== "string" || !HOSTNAME.test(host)) return undefined;
  const out: DerpRegionInput = { code, name, host };
  if (v.ipv4 !== undefined) {
    if (typeof v.ipv4 !== "string" || !IPV4.test(v.ipv4)) return undefined;
    out.ipv4 = v.ipv4;
  }
  if (v.ipv6 !== undefined) {
    if (typeof v.ipv6 !== "string" || !IPV6.test(v.ipv6) || !v.ipv6.includes(":")) return undefined;
    out.ipv6 = v.ipv6;
  }
  if (v.id !== undefined) {
    if (typeof v.id !== "number" || !Number.isInteger(v.id) || v.id < MESH_REGION_ID_MIN || v.id > MESH_REGION_ID_MAX) return undefined;
    out.id = v.id;
  }
  if (v.derpPort !== undefined) {
    const p = port(v.derpPort);
    if (p === undefined) return undefined;
    out.derpPort = p;
  }
  if (v.stunPort !== undefined) {
    const p = port(v.stunPort);
    if (p === undefined) return undefined;
    out.stunPort = p;
  }
  if (v.certName !== undefined) {
    if (typeof v.certName !== "string" || !HOSTNAME.test(v.certName)) return undefined;
    out.certName = v.certName;
  }
  return out;
}

function parseRegions(v: unknown): DerpRegionInput[] | undefined {
  if (!Array.isArray(v) || v.length === 0 || v.length > MESH_REGIONS_MAX) return undefined;
  const out: DerpRegionInput[] = [];
  for (const r of v) {
    const p = parseDerpRegion(r);
    if (p === undefined || out.some((x) => x.code === p.code)) return undefined;
    out.push(p);
  }
  return out;
}

/** An http(s) address with no credentials, query or fragment: the admission service's management endpoint. */
export function parseAdmissionUrl(v: unknown): string | undefined {
  if (typeof v !== "string" || v.length > 512) return undefined;
  let u: URL;
  try {
    u = new URL(v);
  } catch {
    return undefined;
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") return undefined;
  if (u.username !== "" || u.password !== "" || u.search !== "" || u.hash !== "" || u.hostname === "") return undefined;
  return u.href;
}

/** A control-plane address. An API key or OAuth secret is only ever sent over https. */
function httpsUrl(v: unknown): string | undefined {
  const u = parseAdmissionUrl(v);
  return u?.startsWith("https://") ? u : undefined;
}

function absPath(v: unknown): string | undefined {
  return typeof v === "string" && v.length <= 1024 && isAbsolutePath(v) ? v : undefined;
}

function expectVersion(v: Record<string, unknown>): { ok: true; value: string | undefined } | { ok: false } {
  if (v.expectVersion === undefined) return { ok: true, value: undefined };
  return typeof v.expectVersion === "string" && VERSION.test(v.expectVersion) ? { ok: true, value: v.expectVersion } : { ok: false };
}

export type MeshParse = { ok: true; value: MeshRequest } | { ok: false; field: string };

const bad = (field: string): MeshParse => ({ ok: false, field });

/** Parses what the renderer sent. A stray key is an error; nothing is copied through unchecked. */
export function parseMeshRequest(v: unknown): MeshParse {
  if (!isRecord(v) || typeof v.op !== "string") return bad("op");
  switch (v.op) {
    case "policy.document": {
      if (!onlyKeys(v, ["op", "edit"]) || !isRecord(v.edit)) return bad("edit");
      const e = v.edit;
      if (!onlyKeys(e, ["text", "template", "owner", "port", "regions", "region", "removeID", "omitDefault"])) return bad("keys");
      if (typeof e.text !== "string" || !policyTextWithinLimit(e.text) || e.text.trim() === "") return bad("text");
      if (e.template !== undefined && !MESH_TEMPLATES.includes(e.template as MeshTemplate)) return bad("template");
      if (e.owner !== undefined && (typeof e.owner !== "string" || !OWNER.test(e.owner))) return bad("owner");
      if (e.template !== undefined && e.owner === undefined) return bad("owner");
      if (e.port !== undefined && port(e.port) === undefined) return bad("port");
      if (e.regions !== undefined && parseRegions(e.regions) === undefined) return bad("regions");
      if (e.region !== undefined && (!isRecord(e.region) || !onlyKeys(e.region, ["id", "text"]) || !Number.isInteger(e.region.id) || Number(e.region.id) <= 0 || typeof e.region.text !== "string" || !policyTextWithinLimit(e.region.text))) return bad("region");
      if (e.removeID !== undefined && (!Number.isInteger(e.removeID) || Number(e.removeID) <= 0)) return bad("removeID");
      if (e.omitDefault !== undefined && typeof e.omitDefault !== "boolean") return bad("omitDefault");
      return { ok: true, value: { op: "policy.document", edit: e as unknown as PolicyDocumentEdit } };
    }
    case "policy.publish":
      if (!onlyKeys(v, ["op", "text", "expectVersion", "apply"])) return bad("keys");
      if (typeof v.text !== "string" || v.text.trim() === "" || !policyTextWithinLimit(v.text)) return bad("text");
      if (typeof v.expectVersion !== "string" || !VERSION.test(v.expectVersion)) return bad("expectVersion");
      if (typeof v.apply !== "boolean") return bad("apply");
      return { ok: true, value: { op: "policy.publish", text: v.text, expectVersion: v.expectVersion, apply: v.apply } };
    case "login": {
      if (!onlyKeys(v, ["op", "kind", "access", "url", "tailnet", "clientId", "secret"])) return bad("keys");
      if (v.kind !== "tailscale" && v.kind !== "headscale") return bad("kind");
      if (v.access !== "read" && v.access !== "write") return bad("access");
      if (typeof v.secret !== "string" || !SECRET.test(v.secret)) return bad("secret");
      if (v.kind === "headscale") {
        const url = httpsUrl(v.url);
        if (url === undefined) return bad("url");
        if (v.tailnet !== undefined) return bad("tailnet");
        if (v.clientId !== undefined) return bad("clientId");
        return { ok: true, value: { op: "login", kind: "headscale", access: v.access, url, secret: v.secret } };
      }
      if (v.url !== undefined) return bad("url");
      const out: MeshRequest = { op: "login", kind: "tailscale", access: v.access, secret: v.secret };
      if (v.tailnet !== undefined) {
        if (typeof v.tailnet !== "string" || !TAILNET.test(v.tailnet)) return bad("tailnet");
        out.tailnet = v.tailnet;
      }
      if (v.clientId !== undefined) {
        if (typeof v.clientId !== "string" || !CLIENT_ID.test(v.clientId)) return bad("clientId");
        out.clientId = v.clientId;
      }
      return { ok: true, value: out };
    }
    case "status":
    case "policy.show":
    case "policy.snapshots":
      return onlyKeys(v, ["op"]) ? { ok: true, value: { op: v.op } } : bad("keys");
    case "tags": {
      if (!onlyKeys(v, ["op", "device", "tags", "apply"])) return bad("keys");
      if (typeof v.device !== "string" || !DEVICE_ID.test(v.device)) return bad("device");
      const tags = parseMeshTags(v.tags, true);
      if (tags === undefined) return bad("tags");
      if (typeof v.apply !== "boolean") return bad("apply");
      return { ok: true, value: { op: "tags", device: v.device, tags, apply: v.apply } };
    }
    case "onboard": {
      if (!onlyKeys(v, ["op", "tags", "ttl", "ephemeral", "user", "file", "overwrite"])) return bad("keys");
      const tags = parseMeshTags(v.tags, false);
      if (tags === undefined) return bad("tags");
      if (typeof v.ttl !== "string" || !TTL.test(v.ttl)) return bad("ttl");
      if (typeof v.ephemeral !== "boolean") return bad("ephemeral");
      if (typeof v.overwrite !== "boolean") return bad("overwrite");
      const file = absPath(v.file);
      if (file === undefined) return bad("file");
      const out: MeshRequest = { op: "onboard", tags, ttl: v.ttl, ephemeral: v.ephemeral, file, overwrite: v.overwrite };
      if (v.user !== undefined) {
        if (typeof v.user !== "string" || !USER.test(v.user)) return bad("user");
        out.user = v.user;
      }
      return { ok: true, value: out };
    }
    case "policy.template": {
      if (!onlyKeys(v, ["op", "template", "owner", "port", "apply", "expectVersion"])) return bad("keys");
      if (!(MESH_TEMPLATES as readonly unknown[]).includes(v.template)) return bad("template");
      if (typeof v.owner !== "string" || !OWNER.test(v.owner)) return bad("owner");
      if (typeof v.apply !== "boolean") return bad("apply");
      const ev = expectVersion(v);
      if (!ev.ok) return bad("expectVersion");
      const out: MeshRequest = { op: "policy.template", template: v.template as MeshTemplate, owner: v.owner, apply: v.apply };
      if (v.port !== undefined) {
        const p = port(v.port);
        if (p === undefined) return bad("port");
        out.port = p;
      }
      if (ev.value !== undefined) out.expectVersion = ev.value;
      return { ok: true, value: out };
    }
    case "policy.rollback": {
      if (!onlyKeys(v, ["op", "snapshot", "apply", "expectVersion"])) return bad("keys");
      if (typeof v.snapshot !== "string" || !SNAPSHOT_ID.test(v.snapshot)) return bad("snapshot");
      if (typeof v.apply !== "boolean") return bad("apply");
      const ev = expectVersion(v);
      if (!ev.ok) return bad("expectVersion");
      const out: MeshRequest = { op: "policy.rollback", snapshot: v.snapshot, apply: v.apply };
      if (ev.value !== undefined) out.expectVersion = ev.value;
      return { ok: true, value: out };
    }
    case "matrix": {
      if (!onlyKeys(v, ["op", "port", "mismatchOnly"])) return bad("keys");
      if (typeof v.mismatchOnly !== "boolean") return bad("mismatchOnly");
      const out: MeshRequest = { op: "matrix", mismatchOnly: v.mismatchOnly };
      if (v.port !== undefined) {
        const p = port(v.port);
        if (p === undefined) return bad("port");
        out.port = p;
      }
      return { ok: true, value: out };
    }
    case "derp.plan": {
      if (!onlyKeys(v, ["op", "regions", "omitDefault", "apply", "expectVersion", "skipStun"])) return bad("keys");
      const regions = parseRegions(v.regions);
      if (regions === undefined) return bad("regions");
      if (typeof v.omitDefault !== "boolean") return bad("omitDefault");
      if (typeof v.apply !== "boolean") return bad("apply");
      if (typeof v.skipStun !== "boolean") return bad("skipStun");
      const ev = expectVersion(v);
      if (!ev.ok) return bad("expectVersion");
      const out: MeshRequest = { op: "derp.plan", regions, omitDefault: v.omitDefault, apply: v.apply, skipStun: v.skipStun };
      if (ev.value !== undefined) out.expectVersion = ev.value;
      return { ok: true, value: out };
    }
    case "derp.probe": {
      if (!onlyKeys(v, ["op", "regions", "skipStun", "compareOfficial"])) return bad("keys");
      const regions = parseRegions(v.regions);
      if (regions === undefined) return bad("regions");
      if (typeof v.skipStun !== "boolean") return bad("skipStun");
      if (typeof v.compareOfficial !== "boolean") return bad("compareOfficial");
      return { ok: true, value: { op: "derp.probe", regions, skipStun: v.skipStun, compareOfficial: v.compareOfficial } };
    }
    case "derp.headscale": {
      if (!onlyKeys(v, ["op", "regions", "omitDefault"])) return bad("keys");
      const regions = parseRegions(v.regions);
      if (regions === undefined) return bad("regions");
      if (typeof v.omitDefault !== "boolean") return bad("omitDefault");
      return { ok: true, value: { op: "derp.headscale", regions, omitDefault: v.omitDefault } };
    }
    case "derp.sync": {
      if (!onlyKeys(v, ["op", "target", "allDevices"])) return bad("keys");
      if (typeof v.allDevices !== "boolean") return bad("allDevices");
      const t = v.target;
      if (!isRecord(t)) return bad("target");
      if (onlyKeys(t, ["allowlist"]) && t.allowlist !== undefined) {
        const p = absPath(t.allowlist);
        if (p === undefined) return bad("allowlist");
        return { ok: true, value: { op: "derp.sync", target: { allowlist: p }, allDevices: v.allDevices } };
      }
      // A pasted token is an alternative to a file; the main process writes it
      // to a 0600 temp file before the CLI sees it, so argv never carries it.
      if (onlyKeys(t, ["url", "tokenFile", "token"])) {
        const url = parseAdmissionUrl(t.url);
        if (url === undefined) return bad("url");
        if (t.token !== undefined) {
          if (t.tokenFile !== undefined || typeof t.token !== "string" || !SECRET.test(t.token)) return bad("token");
          return { ok: true, value: { op: "derp.sync", target: { url, token: t.token }, allDevices: v.allDevices } };
        }
        const tokenFile = absPath(t.tokenFile);
        if (tokenFile === undefined) return bad("tokenFile");
        return { ok: true, value: { op: "derp.sync", target: { url, tokenFile }, allDevices: v.allDevices } };
      }
      return bad("target");
    }
  }
  return bad("op");
}

// ---- results ----

export type Decision = "allow" | "deny" | "unknown";

export interface MeshDevice {
  id: string;
  name: string;
  hostname?: string;
  addresses: string[];
  tags: string[];
  user?: string;
  os?: string;
  version?: string;
  online: boolean;
  authorized: boolean;
  lastSeen?: string;
  expires?: string;
}

export interface MeshIssue {
  severity: "info" | "warn" | "error";
  code: string;
  message: string;
}

export interface MeshDeviceReport {
  device: MeshDevice;
  roles: string[];
  node?: string;
  issues: MeshIssue[];
}

export interface MeshCapabilities {
  policyWrite: boolean;
  atomicPolicyWrite: boolean;
  derpViaPolicy: boolean;
}

/** Which credential answered `status`: the file or keychain, and its access level. */
export interface MeshStatusCredential {
  access: MeshAccess | "";
  profile: string;
  source: "file" | "keychain" | "";
  file?: string;
}

export interface MeshStatus {
  provider: string;
  capabilities: MeshCapabilities;
  devices: MeshDeviceReport[];
  credential?: MeshStatusCredential;
}

export interface MeshTagPlan {
  device: string;
  from: string[];
  to: string[];
  missingTagOwners: string[];
  applied: boolean;
  now?: string[];
}

/** What `xmesh mesh login --json` reports. The secret is never part of it. */
export interface MeshLoginResult {
  kind: MeshControlPlane;
  access: MeshAccess;
  stored: boolean;
}

export interface MeshKeyResult {
  id: string;
  expires?: string;
  tags: string[];
  file: string;
}

export interface MeshPolicy {
  text: string;
  version: string;
  exists: boolean;
  managedSections: string[];
}

export interface MeshSnapshot {
  id: string;
  provider: string;
  time?: string;
  label: string;
  version: string;
}

export interface MeshPlan {
  provider: string;
  label: string;
  baseVersion: string;
  baseExists: boolean;
  before: string;
  after: string;
  diff: string;
  changed: boolean;
  warnings: string[];
  /** false when the control plane keeps its policy in a file (Headscale policy.mode=file): copy `after` to the server. */
  writable: boolean;
}

export interface MeshPlanResult {
  plan: MeshPlan;
  applied: boolean;
  snapshotId?: string;
  version?: string;
}

export interface MeshCell {
  src: string;
  dst: string;
  network: Decision;
  networkNote?: string;
  tools: Decision;
  allowedTools: string[];
  backends: string[];
  mismatch?: string;
}

export interface MeshMatrix {
  port: number;
  cells: MeshCell[];
}

export interface MeshProbeCheck {
  name: string;
  ok: boolean;
  detail: string;
  latencyMs?: number;
}

export interface MeshProbe {
  host: string;
  ok: boolean;
  checks: MeshProbeCheck[];
}

export interface MeshOfficialLatency {
  regionId: number;
  code: string;
  host: string;
  latencyMs?: number;
  error?: string;
}

export interface MeshDerpResult {
  plan: MeshPlan;
  applied: boolean;
  probes: MeshProbe[];
  rolledBack: boolean;
  snapshotId?: string;
  version?: string;
}

export interface MeshDerpProbe {
  probes: MeshProbe[];
  official: MeshOfficialLatency[];
  warnings: string[];
  compareError?: string;
}

export interface MeshHeadscaleDerp {
  file: string;
  snippet: string;
  warnings: string[];
}

export interface MeshSync {
  added: number;
  removed: number;
  total: number;
}

export interface MeshResults {
  login: MeshLoginResult;
  status: MeshStatus;
  tags: MeshTagPlan;
  onboard: MeshKeyResult;
  "policy.show": MeshPolicy;
  "policy.document": MeshPolicyDocument;
  "policy.publish": MeshPlanResult;
  "policy.snapshots": { snapshots: MeshSnapshot[] };
  "policy.template": MeshPlanResult;
  "policy.rollback": MeshPlanResult;
  matrix: MeshMatrix;
  "derp.plan": MeshDerpResult;
  "derp.probe": MeshDerpProbe;
  "derp.headscale": MeshHeadscaleDerp;
  "derp.sync": MeshSync;
}

export type MeshResultFor<R extends MeshRequest> = MeshResults[R["op"]];

// ---- tolerant normalisers ----
// The CLI is a separate binary and Go encodes nil slices as `null`, zero times
// as year 1 and so on. Anything missing or mistyped becomes an empty value
// instead of an exception in the page.

const rec = (v: unknown): Record<string, unknown> => (isRecord(v) ? v : {});
const str = (v: unknown): string => (typeof v === "string" ? v : "");
const optStr = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);
const bool = (v: unknown): boolean => v === true;
const num = (v: unknown): number => (typeof v === "number" && Number.isFinite(v) ? v : 0);
const list = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const strs = (v: unknown): string[] => list(v).filter((x): x is string => typeof x === "string");

/** Go's zero `time.Time` (year 1) means "never"; so does anything unparsable. */
export function realTime(v: unknown): string | undefined {
  if (typeof v !== "string") return undefined;
  const t = Date.parse(v);
  return Number.isNaN(t) || new Date(t).getUTCFullYear() <= 1 ? undefined : v;
}

function device(v: unknown): MeshDevice {
  const d = rec(v);
  const out: MeshDevice = {
    id: str(d.id),
    name: str(d.name),
    addresses: strs(d.addresses),
    tags: strs(d.tags),
    online: bool(d.online),
    authorized: bool(d.authorized),
  };
  const set = <K extends "hostname" | "user" | "os" | "version" | "lastSeen" | "expires">(k: K, value: string | undefined): void => {
    if (value !== undefined) out[k] = value;
  };
  set("hostname", optStr(d.hostname));
  set("user", optStr(d.user));
  set("os", optStr(d.os));
  set("version", optStr(d.version));
  set("lastSeen", realTime(d.last_seen));
  set("expires", realTime(d.expires));
  return out;
}

function severity(v: unknown): MeshIssue["severity"] {
  return v === "error" || v === "warn" ? v : "info";
}

export function normalizeStatus(v: unknown): MeshStatus {
  const r = rec(v);
  const caps = rec(r.capabilities);
  const c = rec(r.credential);
  const access = str(c.access);
  const source = str(c.source);
  const credential: MeshStatusCredential = {
    access: access === "read" || access === "write" ? access : "",
    profile: str(c.profile),
    source: source === "file" || source === "keychain" ? source : "",
  };
  const file = optStr(c.file);
  if (file !== undefined) credential.file = file;
  return {
    provider: str(r.provider),
    capabilities: {
      policyWrite: bool(caps.policy_write),
      atomicPolicyWrite: bool(caps.atomic_policy_write),
      derpViaPolicy: bool(caps.derp_via_policy),
    },
    credential,
    devices: list(r.devices).map((x) => {
      const d = rec(x);
      const out: MeshDeviceReport = {
        device: device(d.device),
        roles: strs(d.roles),
        issues: list(d.issues).map((i) => {
          const issue = rec(i);
          return { severity: severity(issue.severity), code: str(issue.code), message: str(issue.message) };
        }),
      };
      const node = optStr(d.node);
      if (node !== undefined) out.node = node;
      return out;
    }),
  };
}

export function normalizeTagPlan(v: unknown): MeshTagPlan {
  const r = rec(v);
  const out: MeshTagPlan = {
    device: str(r.device),
    from: strs(r.from),
    to: strs(r.to),
    missingTagOwners: strs(r.missing_tag_owners),
    applied: bool(r.applied),
  };
  if (Array.isArray(r.now)) out.now = strs(r.now);
  return out;
}

export function normalizeLogin(v: unknown): MeshLoginResult {
  const r = rec(v);
  return { kind: r.kind === "headscale" ? "headscale" : "tailscale", access: r.access === "write" ? "write" : "read", stored: r.stored === true };
}

export function normalizeKey(v: unknown): MeshKeyResult {
  const r = rec(v);
  const k = rec(r.key);
  const out: MeshKeyResult = { id: str(k.id), tags: strs(k.tags), file: str(r.file) };
  const expires = realTime(k.expires);
  if (expires !== undefined) out.expires = expires;
  return out;
}

export function normalizePolicy(v: unknown): MeshPolicy {
  const r = rec(v);
  const p = rec(r.policy);
  return { text: str(p.text), version: str(p.version), exists: bool(p.exists), managedSections: strs(r.managed_sections) };
}

export function normalizeSnapshots(v: unknown): { snapshots: MeshSnapshot[] } {
  return {
    snapshots: list(rec(v).snapshots).map((x) => {
      const s = rec(x);
      const out: MeshSnapshot = { id: str(s.id), provider: str(s.provider), label: str(s.label), version: str(s.version) };
      const time = realTime(s.time);
      if (time !== undefined) out.time = time;
      return out;
    }),
  };
}

function plan(v: unknown): MeshPlan {
  const p = rec(v);
  return {
    provider: str(p.provider),
    label: str(p.label),
    baseVersion: str(p.base_version),
    baseExists: bool(p.base_exists),
    before: str(p.before),
    after: str(p.after),
    diff: str(p.diff),
    changed: bool(p.changed),
    warnings: strs(p.warnings),
    writable: bool(p.writable),
  };
}

export function normalizePlanResult(v: unknown): MeshPlanResult {
  const r = rec(v);
  const out: MeshPlanResult = { plan: plan(r.plan), applied: bool(r.applied) };
  const res = rec(r.result);
  const snap = optStr(res.snapshot_id);
  if (snap !== undefined) out.snapshotId = snap;
  const version = optStr(res.version);
  if (version !== undefined) out.version = version;
  return out;
}

function decision(v: unknown): Decision {
  return v === "allow" || v === "deny" ? v : "unknown";
}

export function normalizeMatrix(v: unknown): MeshMatrix {
  const r = rec(v);
  return {
    port: num(r.port),
    cells: list(r.cells).map((x) => {
      const c = rec(x);
      const out: MeshCell = {
        src: str(c.src),
        dst: str(c.dst),
        network: decision(c.network),
        tools: decision(c.tools),
        allowedTools: strs(c.allowed_tools),
        backends: strs(c.backends),
      };
      const note = optStr(c.network_note);
      if (note !== undefined) out.networkNote = note;
      const mismatch = optStr(c.mismatch);
      if (mismatch !== undefined) out.mismatch = mismatch;
      return out;
    }),
  };
}

function probes(v: unknown): MeshProbe[] {
  return list(v).map((x) => {
    const p = rec(x);
    return {
      host: str(p.host),
      ok: bool(p.ok),
      checks: list(p.checks).map((c) => {
        const ch = rec(c);
        const out: MeshProbeCheck = { name: str(ch.name), ok: bool(ch.ok), detail: str(ch.detail) };
        if (num(ch.latency_ns) > 0) out.latencyMs = Math.round(num(ch.latency_ns) / 1e6);
        return out;
      }),
    };
  });
}

export function normalizeDerpResult(v: unknown): MeshDerpResult {
  const r = rec(v);
  const res = rec(r.result);
  const out: MeshDerpResult = {
    plan: plan(r.plan),
    // `apply` emits `result` only after writing; a plan-only run has none.
    applied: isRecord(r.result) || bool(r.applied),
    probes: probes(res.probes),
    rolledBack: bool(res.rolled_back),
  };
  const snap = optStr(res.snapshot_id);
  if (snap !== undefined) out.snapshotId = snap;
  const version = optStr(res.version);
  if (version !== undefined) out.version = version;
  return out;
}

export function normalizeDerpProbe(v: unknown): MeshDerpProbe {
  const r = rec(v);
  const out: MeshDerpProbe = {
    probes: probes(r.probes),
    warnings: strs(r.warnings),
    official: list(r.official).map((x) => {
      const o = rec(x);
      const out: MeshOfficialLatency = { regionId: num(o.region_id), code: str(o.code), host: str(o.host) };
      if (num(o.latency_ns) > 0) out.latencyMs = Math.round(num(o.latency_ns) / 1e6);
      const err = optStr(o.error);
      if (err !== undefined) out.error = err;
      return out;
    }),
  };
  const compareError = optStr(r.compare_error);
  if (compareError !== undefined) out.compareError = compareError;
  return out;
}

export function normalizeHeadscaleDerp(v: unknown): MeshHeadscaleDerp {
  const r = rec(v);
  return { file: str(r.file), snippet: str(r.snippet), warnings: strs(r.warnings) };
}

export function normalizeSync(v: unknown): MeshSync {
  const r = rec(v);
  return { added: num(r.added), removed: num(r.removed), total: num(r.total) };
}

/** Normalises the CLI's JSON for `op`. */
export function normalizeResult<O extends MeshOp>(op: O, json: unknown): MeshResults[O] {
  const out = ((): MeshResults[MeshOp] => {
    switch (op) {
      case "login":
        return normalizeLogin(json);
      case "status":
        return normalizeStatus(json);
      case "tags":
        return normalizeTagPlan(json);
      case "onboard":
        return normalizeKey(json);
      case "policy.show":
        return normalizePolicy(json);
      case "policy.document": {
        if (!isRecord(json) || typeof json.text !== "string" || typeof json.omitDefault !== "boolean" || !Array.isArray(json.regions)) throw new Error("Invalid policy document response");
        const regions = json.regions.map((r) => {
          if (!isRecord(r) || !Number.isInteger(r.id) || Number(r.id) <= 0 || typeof r.code !== "string" || typeof r.name !== "string" || typeof r.text !== "string" || !Array.isArray(r.hosts) || !r.hosts.every((h) => typeof h === "string")) throw new Error("Invalid policy region response");
          return { id: r.id as number, code: r.code, name: r.name, text: r.text, hosts: r.hosts as string[] };
        });
        return { text: json.text, omitDefault: json.omitDefault, regions };
      }
      case "policy.publish":
        return normalizePlanResult(json);
      case "policy.snapshots":
        return normalizeSnapshots(json);
      case "policy.template":
      case "policy.rollback":
        return normalizePlanResult(json);
      case "matrix":
        return normalizeMatrix(json);
      case "derp.plan":
        return normalizeDerpResult(json);
      case "derp.probe":
        return normalizeDerpProbe(json);
      case "derp.headscale":
        return normalizeHeadscaleDerp(json);
      case "derp.sync":
        return normalizeSync(json);
    }
    throw new Error(`unknown mesh op ${String(op)}`);
  })();
  return out as MeshResults[O];
}

// ---- helpers for the page ----

/**
 * Terminal commands the page shows when no credential is saved, run through the CLI at `cli` (see cliCommandText).
 * They run as written (no shell metacharacters beyond the CLI path); the secret is typed at the prompt or piped
 * on stdin. The --panel flag selects the desktop credential file. The Headscale URL is a placeholder to edit.
 */
export function meshLoginCommands(cli = "xmesh") {
  return {
    read: `${cli} mesh login --kind tailscale --access read --panel`,
    write: `${cli} mesh login --kind tailscale --access write --panel`,
    readHeadscale: `${cli} mesh login --kind headscale --url https://headscale.example.com --access read --panel`,
    writeHeadscale: `${cli} mesh login --kind headscale --url https://headscale.example.com --access write --panel`,
  };
}
export type MeshLoginKind = keyof ReturnType<typeof meshLoginCommands>;
