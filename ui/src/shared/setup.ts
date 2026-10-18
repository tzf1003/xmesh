// First-run setup: parameters the wizard collects, their validation, and the
// config file generated from them. The renderer shows this exact text as a
// preview and the main process regenerates it from the validated parameters
// before writing, so what the user reviewed is what lands on disk and the
// renderer never supplies file content.

import type { SetupParams } from "./ipc";

export const NODE_NAME = /^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/;
const DEFAULT_CONTROL_URL = "https://controlplane.tailscale.com";

export { DEFAULT_CONTROL_URL };

/** Credential directories refused by default: the deny list the first run writes, and what "restore defaults" puts back. */
export const DEFAULT_DENY = ["~/.ssh", "~/.gnupg", "~/.aws", "~/.config/gcloud", "~/.azure", "~/.kube", "~/.docker"];

export type SetupProblem =
  | "node_name"
  | "mode"
  | "roles"
  | "workspace"
  | "control_url"
  | "authkey_file"
  | "authkey"
  | "auto_authkey";

/** Absolute POSIX path or Windows drive/UNC path, no control characters, no `..` segments. */
export function isAbsolutePath(p: string): boolean {
  if (p.length === 0 || p.length > 1024 || /[\x00-\x1f]/.test(p)) return false;
  if (p.split(/[\\/]/).includes("..")) return false;
  return p.startsWith("/") || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith("\\\\");
}

export function isHttpsUrl(v: string): boolean {
  if (v.length > 512 || /[\x00-\x20"'\\<>`{}|^]/.test(v)) return false;
  try {
    const u = new URL(v);
    return u.protocol === "https:" && u.username === "" && u.password === "" && u.hostname !== "";
  } catch {
    return false;
  }
}

/**
 * A pasted Tailscale/Headscale auth key. Printable ASCII only, bounded length:
 * the same shape the CLI accepts on stdin.
 */
const AUTHKEY = /^[\x21-\x7e]{8,4096}$/;

export function isAuthKey(v: string): boolean {
  return AUTHKEY.test(v);
}

/** Returns the parameters when valid, or the first offending field. */
export function parseSetup(v: unknown): { ok: true; value: SetupParams } | { ok: false; problem: SetupProblem } {
  const bad = (problem: SetupProblem) => ({ ok: false as const, problem });
  if (typeof v !== "object" || v === null || Array.isArray(v)) return bad("node_name");
  const r = v as Record<string, unknown>;
  const allowed = ["nodeName", "mode", "roles", "workspace", "controlUrl", "authkeyFile", "authkey", "autoAuthkey", "browserLogin", "authkeyUser"];
  if (!Object.keys(r).every((k) => allowed.includes(k))) return bad("node_name");
  if (typeof r.nodeName !== "string" || !NODE_NAME.test(r.nodeName)) return bad("node_name");
  if (r.mode !== "lan" && r.mode !== "tailnet") return bad("mode");
  if (!Array.isArray(r.roles) || r.roles.length === 0) return bad("roles");
  const roles: ("agent" | "gateway")[] = [];
  for (const role of r.roles) {
    if ((role !== "agent" && role !== "gateway") || roles.includes(role)) return bad("roles");
    roles.push(role);
  }
  // The config schema requires `agent.cwd_policy.allow` even on a gateway-only node, so the workspace is always needed.
  if (typeof r.workspace !== "string" || !isAbsolutePath(r.workspace)) return bad("workspace");
  const value: SetupParams = { nodeName: r.nodeName, mode: r.mode, roles, workspace: r.workspace };
  if (r.mode === "tailnet") {
    const url = r.controlUrl === undefined ? DEFAULT_CONTROL_URL : r.controlUrl;
    if (typeof url !== "string" || !isHttpsUrl(url)) return bad("control_url");
    // Exactly one source: create one with the saved credential, paste one, or
    // name a file the user already has.
    const sources = [r.authkey !== undefined, r.authkeyFile !== undefined, r.autoAuthkey === true, r.browserLogin === true].filter(Boolean).length;
    if (sources > 1) return bad("authkey");
    if (r.browserLogin !== undefined) {
      if (r.browserLogin !== true) return bad("auto_authkey");
      value.browserLogin = true;
    } else if (r.autoAuthkey !== undefined) {
      if (typeof r.autoAuthkey !== "boolean" || !r.autoAuthkey) return bad("auto_authkey");
      value.autoAuthkey = true;
    } else if (r.authkey !== undefined) {
      if (typeof r.authkey !== "string" || !isAuthKey(r.authkey)) return bad("authkey");
      value.authkey = r.authkey;
    } else {
      if (typeof r.authkeyFile !== "string" || !isAbsolutePath(r.authkeyFile)) return bad("authkey_file");
      value.authkeyFile = r.authkeyFile;
    }
    if (r.authkeyUser !== undefined) {
      if (r.autoAuthkey !== true || typeof r.authkeyUser !== "string" || !/^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$/.test(r.authkeyUser)) return bad("auto_authkey");
      value.authkeyUser = r.authkeyUser;
    }
    value.controlUrl = url;
  } else if (r.controlUrl !== undefined || r.authkeyFile !== undefined || r.authkey !== undefined || r.autoAuthkey !== undefined || r.browserLogin !== undefined || r.authkeyUser !== undefined) {
    return bad("mode");
  }
  return { ok: true, value };
}

/** Where the first run writes a pasted auth key, beside the config it belongs to. */
export const AUTHKEY_FILE_NAME = "authkey";

/**
 * The parameters as `generateConfig` needs them: a pasted key is replaced by
 * the path it will be written to. The returned value is what goes into the
 * config, so the key text itself never reaches it.
 */
export function resolveAuthkey(p: SetupParams, file: string): SetupParams {
  if (p.authkey === undefined && p.autoAuthkey !== true) return p;
  const { authkey: _authkey, autoAuthkey: _auto, ...rest } = p;
  return { ...rest, authkeyFile: file };
}

/** The deny list file's name, beside the config file it belongs to. */
export const DENY_FILE_NAME = "cwd-deny.yaml";

/** A YAML double-quoted scalar. JSON string syntax is a subset of it, so escaping is delegated to JSON. */
function q(s: string): string {
  return JSON.stringify(s);
}

/**
 * The deny list file's text for `entries`: a comment saying what the list does, then the list. Entries are
 * written as double-quoted scalars, so a path cannot end its line early. Shared by the wizard and the settings page.
 */
export function denyListText(entries: readonly string[]): string {
  const header = "# 禁止访问的目录。这里的目录始终拒绝，与工作目录允许列表是否为空无关。每行一项：绝对路径或以 ~ 开头的路径。\n";
  if (entries.length === 0) return `${header}[]\n`;
  return header + entries.map((e) => `- ${q(e.trim())}\n`).join("");
}

/** The deny list the first run writes: the defaults, as the settings page would save them. */
export function generateDenyFile(): string {
  return denyListText(DEFAULT_DENY);
}

/**
 * The config the wizard writes. Everything user-supplied goes through `q`, so a
 * value cannot add keys or end the scalar. `params` must already be valid, and
 * `denyFile` is the absolute path the first run writes the default deny list to.
 */
export function generateConfig(p: SetupParams, denyFile: string): string {
  const lines: string[] = [
    "# 由 xmesh 首次运行向导生成。之后可以在面板的「设置」页修改。",
    "node:",
    `  name: ${q(p.nodeName)}`,
    `  roles: [${p.roles.join(", ")}]`,
    "mesh:",
    `  mode: ${p.mode}`,
  ];
  if (p.mode === "tailnet") {
    lines.push(`  control_url: ${q(p.controlUrl ?? DEFAULT_CONTROL_URL)}`);
    if (p.browserLogin === true) lines.push("  auth_method: browser");
    else lines.push(`  authkey_file: ${q(p.authkeyFile ?? "")}`);
  } else {
    lines.push("  lan:", "    mdns: true");
  }
  if (p.roles.includes("gateway")) {
    // Every interface, with access limited per request by gateway.access (localhost, LAN and tailnet by default).
    lines.push("gateway:", '  listen: ":8080"');
  }
  // Always present: the schema requires it even when the agent role is off. LAN peers are trusted as
  // `tag:peer` (pairing); tailnet devices authenticate with the `tag:device` tag the control plane grants.
  lines.push(
    "agent:",
    `  default_cwd: ${q(p.workspace)}`,
    "  cwd_policy:",
    `    allow: [${q(p.workspace)}]`,
    `    deny_file: ${q(denyFile)}`,
    "  authz:",
    `    "${p.mode === "tailnet" ? "tag:device" : "tag:peer"}":`,
    '      tools: ["*"]',
    '      backends: ["*"]',
  );
  return `${lines.join("\n")}\n`;
}
