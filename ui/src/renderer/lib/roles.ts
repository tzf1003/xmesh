// The roles page's form: the network mode, the roles the node runs and what the ingress role needs,
// mapped to and from the config file. Edits go through a YAML document, so comments and the sections
// the form does not own stay as the user wrote them. The kernel validates the result before anything
// is written; the checks here only catch what the page can explain before the dry run.

import { parseDocument, type Document, type YAMLSeq } from "yaml";
import { DEFAULT_CONTROL_URL, isAbsolutePath, isHttpsUrl } from "../../shared/setup";

export const ROLES = ["agent", "gateway", "ingress"] as const;
export type Role = (typeof ROLES)[number];

export const MODES = ["lan", "tailnet"] as const;
export type Mode = (typeof MODES)[number];

export const LISTEN_MODES = ["tunnel", "public:443", "funnel"] as const;
export type ListenMode = (typeof LISTEN_MODES)[number];

export const TLS_MODES = ["autocert", "file"] as const;
export type TLSMode = (typeof TLS_MODES)[number];

export interface RolesForm {
  mode: Mode;
  controlUrl: string;
  authkeyFile: string;
  roles: Role[];
  listen: ListenMode;
  issuer: string;
  domain: string;
  tls: TLSMode;
  certFile: string;
  keyFile: string;
}

export type RolesProblem = "roles" | "tailnet" | "control_url" | "authkey_file" | "issuer" | "domain" | "cert_files";

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

/** The form as the parsed config describes it. A node without ingress starts from the defaults the page offers. */
export function formFromConfig(parsed: unknown): RolesForm {
  const cfg = record(parsed);
  const node = record(cfg.node);
  const mesh = record(cfg.mesh);
  const ingress = record(cfg.ingress);
  const oauth = record(ingress.oauth);
  const roles = Array.isArray(node.roles) ? node.roles : [];
  return {
    mode: mesh.mode === "tailnet" ? "tailnet" : "lan",
    controlUrl: text(mesh.control_url) || DEFAULT_CONTROL_URL,
    authkeyFile: text(mesh.authkey_file),
    roles: ROLES.filter((r) => roles.includes(r)),
    listen: LISTEN_MODES.find((m) => m === ingress.listen) ?? "tunnel",
    issuer: text(oauth.issuer),
    domain: text(ingress.domain),
    tls: TLS_MODES.find((m) => m === ingress.tls) ?? "autocert",
    certFile: text(ingress.cert_file),
    keyFile: text(ingress.key_file),
  };
}

/** `roles` with `role` set on or off, in the order the roles are listed in. */
export function toggleRole(roles: readonly Role[], role: Role, on: boolean): Role[] {
  return ROLES.filter((r) => (r === role ? on : roles.includes(r)));
}

/** Whether the form asks for something other than what the file says. */
export function isChanged(current: RolesForm, next: RolesForm): boolean {
  return JSON.stringify(current) !== JSON.stringify(next);
}

/** The first thing the form gets wrong, found before the kernel is asked. */
export function checkRolesForm(f: RolesForm): RolesProblem | undefined {
  if (f.roles.length === 0) return "roles";
  if (f.mode === "tailnet") {
    if (!isHttpsUrl(f.controlUrl.trim())) return "control_url";
    if (!isAbsolutePath(f.authkeyFile.trim())) return "authkey_file";
  }
  if (!f.roles.includes("ingress")) return undefined;
  // The kernel refuses ingress outside tailnet mode, since OAuth and Funnel need the control plane.
  if (f.mode === "lan") return "tailnet";
  const issuer = f.issuer.trim();
  if (!ISSUER.test(issuer) || !isHttpsUrl(issuer)) return "issuer";
  if (f.listen !== "public:443") return undefined;
  if (f.domain.trim() === "") return "domain";
  if (f.tls === "file" && (!isAbsolutePath(f.certFile.trim()) || !isAbsolutePath(f.keyFile.trim()))) return "cert_files";
  return undefined;
}

/**
 * The config text with the form applied. Only the keys the form owns change: `mesh.mode`, and for tailnet its
 * control URL and key file; `node.roles`; the gateway listen address when the gateway role is new; and the
 * ingress keys. Undefined when the text is not YAML at all.
 */
export function applyRolesForm(content: string, f: RolesForm): string | undefined {
  const doc = parseDocument(content);
  if (doc.errors.length > 0) return undefined;
  doc.setIn(["mesh", "mode"], f.mode);
  if (f.mode === "tailnet") {
    doc.setIn(["mesh", "control_url"], f.controlUrl.trim());
    doc.setIn(["mesh", "authkey_file"], f.authkeyFile.trim());
  }
  // Flow style, like the wizard writes it, so the roles stay on one line.
  const roles = doc.createNode(f.roles) as YAMLSeq;
  roles.flow = true;
  doc.setIn(["node", "roles"], roles);
  if (f.roles.includes("gateway") && doc.getIn(["gateway", "listen"]) === undefined) {
    doc.setIn(["gateway", "listen"], GATEWAY_LISTEN);
  }
  if (f.roles.includes("ingress")) applyIngress(doc, f);
  // No padding inside brackets, as the wizard writes them.
  return doc.toString({ flowCollectionPadding: false });
}

function applyIngress(doc: Document, f: RolesForm): void {
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
