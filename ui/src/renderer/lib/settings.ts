// The settings page's forms: what the first run asked for (network mode, node name, roles, locations) plus
// the ingress and working-directory settings, mapped to and from the config file, and the deny list file.
// Edits go through a YAML document, so comments and the sections the form does not own stay as the user
// wrote them. The kernel validates the result before anything is written; the checks here only catch what
// the page can explain before the dry run.

import { Document, parseDocument } from "yaml";
import { DEFAULT_CONTROL_URL, NODE_NAME, isAbsolutePath, isAuthKey, isHttpsUrl } from "../../shared/setup";
import { configYaml } from "../../shared/config-yaml";

export const ROLES = ["agent", "gateway", "ingress"] as const;
export type Role = (typeof ROLES)[number];

export const MODES = ["lan", "tailnet", "cloudflare", "coexist", "offline"] as const;
export type Mode = (typeof MODES)[number];

export const LISTEN_MODES = ["tunnel", "public:443", "funnel"] as const;
export type ListenMode = (typeof LISTEN_MODES)[number];

/** Where a reverse tunnel reaches the ingress when `ingress.tunnel_listen` is absent. */
export const DEFAULT_TUNNEL_PORT = 8443;

/** `ingress.oauth.dynamic_client_scopes` when the key is absent: a dynamically registered client may only list devices. */
export const DEFAULT_DYNAMIC_SCOPES: readonly string[] = ["devices:read"];

/** The kernel's default refresh token lifetime and the longest it accepts, in days. */
export const DEFAULT_REFRESH_DAYS = 30;
export const MAX_REFRESH_DAYS = 365;

/** A whole number of days from a duration such as `720h`, rounded up; undefined for anything the page cannot show. */
function refreshDaysOf(value: unknown): string | undefined {
  const m = typeof value === "string" ? /^([0-9]+)h$/.exec(value) : null;
  return m ? String(Math.max(1, Math.ceil(Number(m[1]) / 24))) : undefined;
}

/** The scopes the page offers as choices, in the order the kernel lists them as supported. */
export const SCOPE_CHOICES = [
  "devices:read",
  "sys:*",
  "fs:read:*",
  "fs:write:*",
  "exec:*",
  "pty:*",
  "backend:*:*",
  "computer:observe:*",
  "computer:control:*",
  "computer:clipboard-read:*",
  "computer:clipboard-write:*",
] as const;

/** The shape of one scope: the kernel checks the device and backend names, this only catches what is plainly not one. */
const SCOPE = /^[a-z]+(:(\*|[A-Za-z0-9][A-Za-z0-9-]{0,62})){1,2}$/;

export const TLS_MODES = ["autocert", "file"] as const;
export type TLSMode = (typeof TLS_MODES)[number];

/** The sections of the settings page; a link to the page names one. */
export const SECTIONS = ["node", "roles", "workdirs", "transfers", "denylist"] as const;
export type Section = (typeof SECTIONS)[number] | "mcp";

export interface SettingsForm {
  nodeName: string;
  mode: Mode;
  controlUrl: string;
  /** A replacement key, held only until the settings are saved. */
  authkey: string;
  authkeyFile: string;
  browserLogin: boolean;
  roles: Role[];
  listen: ListenMode;
  /** The loopback port a reverse tunnel reaches (`ingress.tunnel_listen`); empty leaves the default. */
  tunnelPort: string;
  /** `ingress.oauth.dynamic_client_scopes`: the most a dynamically registered client may hold. */
  dynamicScopes: string[];
  /** `ingress.oauth.refresh_token_ttl` in days; empty leaves the kernel's default of 30. */
  refreshDays: string;
  issuer: string;
  domain: string;
  tls: TLSMode;
  certFile: string;
  keyFile: string;
  /** `agent.default_cwd`: where commands run when a call names no directory. */
  defaultCwd: string;
  /** `agent.cwd_policy.allow`. Empty means every directory the deny list does not cover. */
  allowDirs: string[];
  maxFileSize: string;
}

export type SettingsProblem =
  | "node_name"
  | "roles"
  | "tailnet"
  | "control_url"
  | "authkey"
  | "issuer"
  | "tunnel_port"
  | "dynamic_scopes"
  | "refresh_days"
  | "domain"
  | "cert_files"
  | "default_cwd"
  | "allow_dirs"
  | "max_file_size";

/** The wizard's loopback default, so a gateway added here works without a second edit. */
const GATEWAY_LISTEN = "127.0.0.1:8080";
/** The schema's shape for the issuer: an https origin with no path. */
const ISSUER = /^https:\/\/[^/]+$/;

function record(v: unknown): Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v) ? (v as Record<string, unknown>) : {};
}

function text(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

/** The form as the parsed config describes it. A node without ingress starts from the defaults the page offers. */
export function formFromConfig(parsed: unknown): SettingsForm {
  const cfg = record(parsed);
  const node = record(cfg.node);
  const mesh = record(cfg.mesh);
  const lan = record(mesh.lan);
  const tailnet = cfg.schema_version === 2 ? record(mesh.tailnet) : mesh;
  const mode: Mode = cfg.schema_version === 2 ? (lan.enabled !== false ? (tailnet.enabled === true ? "coexist" : "lan") : (tailnet.enabled === true ? "tailnet" : record(mesh.cloudflare).enabled === true ? "cloudflare" : "offline")) : (mesh.mode === "tailnet" ? "tailnet" : "lan");
  const agent = record(cfg.agent);
  const policy = record(agent.cwd_policy);
  const ingress = record(cfg.ingress);
  const oauth = record(ingress.oauth);
  const roles = Array.isArray(node.roles) ? node.roles : [];
  return {
    nodeName: text(node.name),
    mode,
    controlUrl: text(tailnet.control_url) || DEFAULT_CONTROL_URL,
    authkey: "",
    authkeyFile: text(tailnet.authkey_file),
    browserLogin: tailnet.auth_method === "browser",
    roles: ROLES.filter((r) => roles.includes(r)),
    listen: LISTEN_MODES.find((m) => m === ingress.listen) ?? "tunnel",
    tunnelPort: /^127\.0\.0\.1:([0-9]{1,5})$/.exec(text(ingress.tunnel_listen))?.[1] ?? "",
    dynamicScopes: Array.isArray(oauth.dynamic_client_scopes) ? strings(oauth.dynamic_client_scopes) : [...DEFAULT_DYNAMIC_SCOPES],
    refreshDays: refreshDaysOf(oauth.refresh_token_ttl) ?? "",
    issuer: text(oauth.issuer),
    domain: text(ingress.domain),
    tls: TLS_MODES.find((m) => m === ingress.tls) ?? "autocert",
    certFile: text(ingress.cert_file),
    keyFile: text(ingress.key_file),
    defaultCwd: text(agent.default_cwd),
    allowDirs: strings(policy.allow),
    maxFileSize: text(record(agent.fs).max_file_size) || "1GiB",
  };
}

/** `roles` with `role` set on or off, in the order the roles are listed in. */
export function toggleRole(roles: readonly Role[], role: Role, on: boolean): Role[] {
  return ROLES.filter((r) => (r === role ? on : roles.includes(r)));
}

/** Whether the form asks for something other than what the file says. */
export function isChanged(current: SettingsForm, next: SettingsForm): boolean {
  return JSON.stringify({ ...current, authkey: current.authkey.trim() }) !== JSON.stringify({ ...next, authkey: next.authkey.trim() });
}

function validFileSize(value: string): boolean {
  const m = /^([1-9][0-9]*)([KMGT]iB|B)?$/.exec(value);
  if (!m) return false;
  const shift = m[2] && m[2] !== "B" ? ("KMGT".indexOf(m[2][0]!) + 1) * 10 : 0;
  return (BigInt(m[1]!) << BigInt(shift)) <= 9223372036854775807n;
}

function validPort(value: string): boolean {
  return /^[0-9]{1,5}$/.test(value) && Number(value) >= 1 && Number(value) <= 65535;
}

/** Scopes typed as a list, split on commas, whitespace and newlines, without blanks or repeats. */
export function parseScopes(value: string): string[] {
  return [...new Set(value.split(/[\s,]+/).filter((scope) => scope !== ""))];
}

const isChoice = (scope: string): boolean => (SCOPE_CHOICES as readonly string[]).includes(scope);

/** The scopes the page has no checkbox for, such as ones limited to one device. */
export function otherScopes(scopes: readonly string[]): string[] {
  return scopes.filter((scope) => !isChoice(scope));
}

/** `scopes` with one offered choice ticked or cleared; the choices keep their listed order and the other scopes follow them. */
export function toggleScope(scopes: readonly string[], scope: string, on: boolean): string[] {
  return [...SCOPE_CHOICES.filter((c) => (c === scope ? on : scopes.includes(c))), ...otherScopes(scopes)];
}

/** `scopes` with its other scopes replaced by the ones typed in `value`. */
export function withOtherScopes(scopes: readonly string[], value: string): string[] {
  return [...SCOPE_CHOICES.filter((c) => scopes.includes(c)), ...parseScopes(value).filter((scope) => !isChoice(scope))];
}

/** The first thing the form gets wrong, found before the kernel is asked. */
export function checkSettingsForm(f: SettingsForm): SettingsProblem | undefined {
  if (f.maxFileSize !== "unlimited" && !validFileSize(f.maxFileSize.trim())) return "max_file_size";
  if (!NODE_NAME.test(f.nodeName.trim())) return "node_name";
  if (!f.defaultCwd.trim() || /[\x00-\x1f]/.test(f.defaultCwd)) return "default_cwd";
  if (f.allowDirs.some((d) => !d.trim() || /[\x00-\x1f]/.test(d))) return "allow_dirs";
  if (f.roles.length === 0) return "roles";
  if (f.mode === "tailnet" || f.mode === "coexist") {
    if (!isHttpsUrl(f.controlUrl.trim())) return "control_url";
    if (f.authkey.trim() ? !isAuthKey(f.authkey.trim()) : !f.browserLogin && !isAbsolutePath(f.authkeyFile.trim())) return "authkey";
  }
  if (!f.roles.includes("ingress")) return undefined;
  return checkIngressSettings(f);
}

/** Validate the fields owned by a module, independently of other module drafts. */
export function checkSettingsScope(f: SettingsForm, scope: "node" | "agent" | "gateway" | "ingress" | "transfers"): SettingsProblem | undefined {
  if (scope === "transfers") return f.maxFileSize !== "unlimited" && !validFileSize(f.maxFileSize.trim()) ? "max_file_size" : undefined;
  if (scope === "agent") {
    if (!f.defaultCwd.trim() || /[\x00-\x1f]/.test(f.defaultCwd)) return "default_cwd";
    if (f.allowDirs.some(d => !d.trim() || /[\x00-\x1f]/.test(d))) return "allow_dirs";
  }
  if (scope === "ingress") return checkIngressSettings(f);
  if (scope === "node") {
    if (!NODE_NAME.test(f.nodeName.trim())) return "node_name";
    if (f.mode === "tailnet" || f.mode === "coexist") {
      if (!isHttpsUrl(f.controlUrl.trim())) return "control_url";
      if (f.authkey.trim() ? !isAuthKey(f.authkey.trim()) : !f.browserLogin && !isAbsolutePath(f.authkeyFile.trim())) return "authkey";
    }
  }
  return undefined;
}

/** Fields needed to enable ingress, shared by the form and the global role switch. */
export function checkIngressSettings(f: Pick<SettingsForm, "mode" | "issuer" | "listen" | "tunnelPort" | "dynamicScopes" | "refreshDays" | "domain" | "tls" | "certFile" | "keyFile">): SettingsProblem | undefined {
  if (f.listen === "funnel" && f.mode !== "tailnet" && f.mode !== "coexist") return "tailnet";
  const issuer = f.issuer.trim();
  if (!ISSUER.test(issuer) || !isHttpsUrl(issuer)) return "issuer";
  if (f.listen === "tunnel" && f.tunnelPort.trim() !== "" && !validPort(f.tunnelPort.trim())) return "tunnel_port";
  if (f.dynamicScopes.some((scope) => !SCOPE.test(scope))) return "dynamic_scopes";
  if (f.refreshDays.trim() !== "" && !(/^[0-9]{1,3}$/.test(f.refreshDays.trim()) && Number(f.refreshDays.trim()) >= 1 && Number(f.refreshDays.trim()) <= MAX_REFRESH_DAYS)) return "refresh_days";
  if (f.listen !== "public:443") return undefined;
  if (f.domain.trim() === "") return "domain";
  if (f.tls === "file" && (!isAbsolutePath(f.certFile.trim()) || !isAbsolutePath(f.keyFile.trim()))) return "cert_files";
  return undefined;
}

/**
 * The config text with the form applied. Only the keys the form owns change: `node.name` and `node.roles`,
 * the v2 network path switches and Tailnet credentials, the gateway listen address when the gateway role
 * is new, the ingress keys, and `agent.default_cwd`, `agent.cwd_policy.allow` and `agent.fs.max_file_size`. The deny list lives in its
 * own file and is not touched here. Undefined when the text is not YAML at all.
 */
export function applySettingsForm(content: string, f: SettingsForm): string | undefined {
  const doc = parseDocument(content);
  if (doc.errors.length > 0) return undefined;
  doc.setIn(["node", "name"], f.nodeName.trim());
  doc.setIn(["node", "roles"], [...f.roles]);
  doc.setIn(["schema_version"], 2);
  const ephemeral = doc.getIn(["mesh", "ephemeral"]);
  if (typeof ephemeral === "boolean" && doc.getIn(["mesh", "tailnet", "ephemeral"]) === undefined) {
    doc.setIn(["mesh", "tailnet", "ephemeral"], ephemeral);
  }
  for (const key of ["mode", "control_url", "authkey_file", "auth_method", "ephemeral"]) deleteExisting(doc, ["mesh", key]);
  doc.setIn(["mesh", "lan", "enabled"], f.mode === "lan" || f.mode === "coexist");
  doc.setIn(["mesh", "tailnet", "enabled"], f.mode === "tailnet" || f.mode === "coexist");
  // Cloudflare is configured in the file; the form only turns it on for its own mode and never turns off a tunnel it did not select.
  if (f.mode === "cloudflare") doc.setIn(["mesh", "cloudflare", "enabled"], true);
  if (f.mode === "tailnet" || f.mode === "coexist") {
    doc.setIn(["mesh", "tailnet", "control_url"], f.controlUrl.trim());
    if (f.browserLogin && !f.authkey.trim()) {
      doc.setIn(["mesh", "tailnet", "auth_method"], "browser");
      deleteExisting(doc, ["mesh", "tailnet", "authkey_file"]);
    } else {
      deleteExisting(doc, ["mesh", "tailnet", "auth_method"]);
      doc.setIn(["mesh", "tailnet", "authkey_file"], f.authkeyFile.trim());
    }
  }
  if (f.roles.includes("gateway") && doc.getIn(["gateway", "listen"]) === undefined) {
    doc.setIn(["gateway", "listen"], GATEWAY_LISTEN);
  }
  if (f.roles.includes("ingress")) applyIngress(doc, f);
  doc.setIn(["agent", "default_cwd"], f.defaultCwd.trim());
  doc.setIn(["agent", "cwd_policy", "allow"], f.allowDirs.map((d) => d.trim()));
  doc.setIn(["agent", "fs", "max_file_size"], f.maxFileSize.trim());
  return configYaml(doc);
}

function deleteExisting(doc: Document, path: string[]): void {
  if (doc.hasIn(path)) doc.deleteIn(path);
}

function applyIngress(doc: Document, f: SettingsForm): void {
  doc.setIn(["ingress", "listen"], f.listen);
  doc.setIn(["ingress", "oauth", "issuer"], f.issuer.trim());
  // Only a reverse tunnel has a local port to choose; an empty field leaves the kernel's default.
  if (f.listen === "tunnel" && f.tunnelPort.trim() !== "") doc.setIn(["ingress", "tunnel_listen"], `127.0.0.1:${Number(f.tunnelPort.trim())}`);
  else deleteExisting(doc, ["ingress", "tunnel_listen"]);
  // An absent cap already means devices:read, so the default is only written once the key exists.
  const scopes = [...new Set(f.dynamicScopes)];
  const isDefault = scopes.length === DEFAULT_DYNAMIC_SCOPES.length && scopes.every((scope) => DEFAULT_DYNAMIC_SCOPES.includes(scope));
  if (!isDefault || doc.hasIn(["ingress", "oauth", "dynamic_client_scopes"])) doc.setIn(["ingress", "oauth", "dynamic_client_scopes"], scopes);
  // The lifetime is written in hours, the unit the kernel's duration syntax has for days. A value the kernel already
  // has that shows as the same number of days is left alone, so saving other fields never rounds it.
  const days = f.refreshDays.trim() === "" ? "" : String(Number(f.refreshDays.trim()));
  if (days === "") deleteExisting(doc, ["ingress", "oauth", "refresh_token_ttl"]);
  else if (refreshDaysOf(doc.getIn(["ingress", "oauth", "refresh_token_ttl"])) !== days) doc.setIn(["ingress", "oauth", "refresh_token_ttl"], `${Number(days) * 24}h`);
  // Only public:443 takes a domain and a certificate; the kernel refuses them with the other two listen modes.
  if (f.listen === "public:443") {
    doc.setIn(["ingress", "domain"], f.domain.trim());
    doc.setIn(["ingress", "tls"], f.tls);
  } else {
    deleteExisting(doc, ["ingress", "domain"]);
    deleteExisting(doc, ["ingress", "tls"]);
  }
  if (f.listen === "public:443" && f.tls === "file") {
    doc.setIn(["ingress", "cert_file"], f.certFile.trim());
    doc.setIn(["ingress", "key_file"], f.keyFile.trim());
  } else {
    deleteExisting(doc, ["ingress", "cert_file"]);
    deleteExisting(doc, ["ingress", "key_file"]);
  }
}

/**
 * The config text with its deny list moved to a file: `agent.cwd_policy.deny_file` names `denyPath` and the inline
 * `deny` list goes, since the file holds those entries now. The rest stays as the user wrote it. Undefined when the
 * text is not YAML at all.
 */
export function applyDenyFile(content: string, denyPath: string): string | undefined {
  const doc = parseDocument(content);
  if (doc.errors.length > 0) return undefined;
  doc.setIn(["agent", "cwd_policy", "deny_file"], denyPath);
  deleteExisting(doc, ["agent", "cwd_policy", "deny"]);
  return configYaml(doc);
}

/** The entries of a deny list file: a YAML list of strings. Undefined when the text is anything else. */
export function parseDenyList(text: string): string[] | undefined {
  const doc = parseDocument(text);
  if (doc.errors.length > 0) return undefined;
  const v = doc.toJS() as unknown;
  if (v === null || v === undefined) return [];
  if (!Array.isArray(v) || !v.every((x) => typeof x === "string")) return undefined;
  return v;
}

/** The deny list entries that would be refused: blank ones, and ones the kernel's path rules can never match. */
export function checkDenyList(entries: readonly string[]): "blank" | "control" | undefined {
  for (const e of entries) {
    const v = e.trim();
    if (v === "") return "blank";
    if (/[\x00-\x1f]/.test(v) || v.length > 1024) return "control";
  }
  return undefined;
}

/** Writes only the selected module's fields, preserving the other configuration sections. */
export function applySettingsScope(content: string, form: SettingsForm, scope: "node" | "agent" | "gateway" | "ingress" | "transfers", extras?: {listen: string; execLimit: number; ptyLimit: number}): string | undefined {
  const transformed = applySettingsForm(content, scope === "ingress" ? { ...form, roles: [...new Set([...form.roles, "ingress" as const])] } : form);
  if (!transformed) return undefined;
  const original = parseDocument(content), next = parseDocument(transformed);
  if (original.errors.length || next.errors.length) return undefined;
  const paths: Record<typeof scope, string[][]> = {
    node: [["schema_version"], ["node", "name"], ["mesh"]],
    agent: [["agent", "default_cwd"], ["agent", "cwd_policy", "allow"]],
    gateway: [],
    ingress: [["ingress"]],
    transfers: [["agent", "fs", "max_file_size"]],
  };
  if (scope === "gateway" && extras) original.setIn(["gateway", "listen"], extras.listen);
  if (scope === "agent" && extras) { original.setIn(["agent", "exec", "max_concurrent"], extras.execLimit); original.setIn(["agent", "pty", "max_sessions"], extras.ptyLimit); }
  for (const path of paths[scope]) {
    const value = next.getIn(path, true);
    if (value === undefined) deleteExisting(original, path);
    else original.setIn(path, value);
  }
  return configYaml(original);
}
