// The settings page's forms: what the first run asked for (network mode, node name, roles, locations) plus
// the ingress and working-directory settings, mapped to and from the config file, and the deny list file.
// Edits go through a YAML document, so comments and the sections the form does not own stay as the user
// wrote them. The kernel validates the result before anything is written; the checks here only catch what
// the page can explain before the dry run.

import { Document, parseDocument, type YAMLSeq } from "yaml";
import { DEFAULT_CONTROL_URL, NODE_NAME, isAbsolutePath, isHttpsUrl } from "../../shared/setup";

export const ROLES = ["agent", "gateway", "ingress"] as const;
export type Role = (typeof ROLES)[number];

export const MODES = ["lan", "tailnet", "coexist", "offline"] as const;
export type Mode = (typeof MODES)[number];

export const LISTEN_MODES = ["tunnel", "public:443", "funnel"] as const;
export type ListenMode = (typeof LISTEN_MODES)[number];

export const TLS_MODES = ["autocert", "file"] as const;
export type TLSMode = (typeof TLS_MODES)[number];

/** The sections of the settings page; a link to the page names one. */
export const SECTIONS = ["node", "roles", "workdirs", "transfers", "denylist"] as const;
export type Section = (typeof SECTIONS)[number] | "mcp";

export interface SettingsForm {
  nodeName: string;
  mode: Mode;
  controlUrl: string;
  authkeyFile: string;
  browserLogin: boolean;
  roles: Role[];
  listen: ListenMode;
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
  | "authkey_file"
  | "issuer"
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
  const mode: Mode = cfg.schema_version === 2 ? (lan.enabled !== false ? (tailnet.enabled === true ? "coexist" : "lan") : (tailnet.enabled === true ? "tailnet" : "offline")) : (mesh.mode === "tailnet" ? "tailnet" : "lan");
  const agent = record(cfg.agent);
  const policy = record(agent.cwd_policy);
  const ingress = record(cfg.ingress);
  const oauth = record(ingress.oauth);
  const roles = Array.isArray(node.roles) ? node.roles : [];
  return {
    nodeName: text(node.name),
    mode,
    controlUrl: text(tailnet.control_url) || DEFAULT_CONTROL_URL,
    authkeyFile: text(tailnet.authkey_file),
    browserLogin: tailnet.auth_method === "browser",
    roles: ROLES.filter((r) => roles.includes(r)),
    listen: LISTEN_MODES.find((m) => m === ingress.listen) ?? "tunnel",
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
  return JSON.stringify(current) !== JSON.stringify(next);
}

function validFileSize(value: string): boolean {
  const m = /^([1-9][0-9]*)([KMGT]iB|B)?$/.exec(value);
  if (!m) return false;
  const shift = m[2] && m[2] !== "B" ? ("KMGT".indexOf(m[2][0]!) + 1) * 10 : 0;
  return (BigInt(m[1]!) << BigInt(shift)) <= 9223372036854775807n;
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
    if (!f.browserLogin && !isAbsolutePath(f.authkeyFile.trim())) return "authkey_file";
  }
  if (!f.roles.includes("ingress")) return undefined;
  return checkIngressSettings(f);
}

/** Fields needed to enable ingress, shared by the form and the global role switch. */
export function checkIngressSettings(f: Pick<SettingsForm, "mode" | "issuer" | "listen" | "domain" | "tls" | "certFile" | "keyFile">): SettingsProblem | undefined {
  if (f.listen === "funnel" && f.mode !== "tailnet" && f.mode !== "coexist") return "tailnet";
  const issuer = f.issuer.trim();
  if (!ISSUER.test(issuer) || !isHttpsUrl(issuer)) return "issuer";
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
  doc.setIn(["node", "roles"], flowList(doc, f.roles));
  doc.setIn(["schema_version"], 2);
  const ephemeral = doc.getIn(["mesh", "ephemeral"]);
  if (typeof ephemeral === "boolean" && doc.getIn(["mesh", "tailnet", "ephemeral"]) === undefined) {
    doc.setIn(["mesh", "tailnet", "ephemeral"], ephemeral);
  }
  for (const key of ["mode", "control_url", "authkey_file", "auth_method", "ephemeral"]) doc.deleteIn(["mesh", key]);
  doc.setIn(["mesh", "lan", "enabled"], f.mode === "lan" || f.mode === "coexist");
  doc.setIn(["mesh", "tailnet", "enabled"], f.mode === "tailnet" || f.mode === "coexist");
  if (f.mode === "tailnet" || f.mode === "coexist") {
    doc.setIn(["mesh", "tailnet", "control_url"], f.controlUrl.trim());
    if (f.browserLogin) {
      doc.setIn(["mesh", "tailnet", "auth_method"], "browser");
      doc.deleteIn(["mesh", "tailnet", "authkey_file"]);
    } else {
      doc.deleteIn(["mesh", "tailnet", "auth_method"]);
      doc.setIn(["mesh", "tailnet", "authkey_file"], f.authkeyFile.trim());
    }
  }
  if (f.roles.includes("gateway") && doc.getIn(["gateway", "listen"]) === undefined) {
    doc.setIn(["gateway", "listen"], GATEWAY_LISTEN);
  }
  if (f.roles.includes("ingress")) applyIngress(doc, f);
  doc.setIn(["agent", "default_cwd"], f.defaultCwd.trim());
  doc.setIn(["agent", "cwd_policy", "allow"], flowList(doc, f.allowDirs.map((d) => d.trim())));
  doc.setIn(["agent", "fs", "max_file_size"], f.maxFileSize.trim());
  // No padding inside brackets, as the wizard writes them.
  return doc.toString({ flowCollectionPadding: false });
}

/** A flow-style sequence, as the wizard writes lists; an empty one prints as `[]`. */
function flowList(doc: Document, items: readonly string[]): YAMLSeq {
  const seq = doc.createNode([...items]) as YAMLSeq;
  seq.flow = true;
  return seq;
}

function applyIngress(doc: Document, f: SettingsForm): void {
  doc.setIn(["ingress", "listen"], f.listen);
  doc.setIn(["ingress", "oauth", "issuer"], f.issuer.trim());
  // Only public:443 takes a domain and a certificate; the kernel refuses them with the other two listen modes.
  if (f.listen === "public:443") {
    doc.setIn(["ingress", "domain"], f.domain.trim());
    doc.setIn(["ingress", "tls"], f.tls);
  } else {
    doc.deleteIn(["ingress", "domain"]);
    doc.deleteIn(["ingress", "tls"]);
  }
  if (f.listen === "public:443" && f.tls === "file") {
    doc.setIn(["ingress", "cert_file"], f.certFile.trim());
    doc.setIn(["ingress", "key_file"], f.keyFile.trim());
  } else {
    doc.deleteIn(["ingress", "cert_file"]);
    doc.deleteIn(["ingress", "key_file"]);
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
  doc.deleteIn(["agent", "cwd_policy", "deny"]);
  return doc.toString({ flowCollectionPadding: false });
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
