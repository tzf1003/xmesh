// First-run setup: parameters the wizard collects, their validation, and the
// config file generated from them. The renderer shows this exact text as a
// preview and the main process regenerates it from the validated parameters
// before writing, so what the user reviewed is what lands on disk and the
// renderer never supplies file content.

import type { SetupParams } from "./ipc";

const NODE_NAME = /^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/;
const DEFAULT_CONTROL_URL = "https://controlplane.tailscale.com";

export { DEFAULT_CONTROL_URL };

export type SetupProblem =
  | "node_name"
  | "mode"
  | "roles"
  | "workspace"
  | "control_url"
  | "authkey_file";

/** Absolute POSIX path or Windows drive/UNC path, no control characters, no `..` segments. */
export function isAbsolutePath(p: string): boolean {
  if (p.length === 0 || p.length > 1024 || /[\x00-\x1f]/.test(p)) return false;
  if (p.split(/[\\/]/).includes("..")) return false;
  return p.startsWith("/") || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith("\\\\");
}

function isHttpsUrl(v: string): boolean {
  if (v.length > 512 || /[\x00-\x20"'\\<>`{}|^]/.test(v)) return false;
  try {
    const u = new URL(v);
    return u.protocol === "https:" && u.username === "" && u.password === "" && u.hostname !== "";
  } catch {
    return false;
  }
}

/** Returns the parameters when valid, or the first offending field. */
export function parseSetup(v: unknown): { ok: true; value: SetupParams } | { ok: false; problem: SetupProblem } {
  const bad = (problem: SetupProblem) => ({ ok: false as const, problem });
  if (typeof v !== "object" || v === null || Array.isArray(v)) return bad("node_name");
  const r = v as Record<string, unknown>;
  const allowed = ["nodeName", "mode", "roles", "workspace", "controlUrl", "authkeyFile"];
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
    if (typeof r.authkeyFile !== "string" || !isAbsolutePath(r.authkeyFile)) return bad("authkey_file");
    value.controlUrl = url;
    value.authkeyFile = r.authkeyFile;
  } else if (r.controlUrl !== undefined || r.authkeyFile !== undefined) {
    return bad("mode");
  }
  return { ok: true, value };
}

/** A YAML double-quoted scalar. JSON string syntax is a subset of it, so escaping is delegated to JSON. */
function q(s: string): string {
  return JSON.stringify(s);
}

/**
 * The config the wizard writes. Everything user-supplied goes through `q`, so a
 * value cannot add keys or end the scalar. `params` must already be valid.
 */
export function generateConfig(p: SetupParams): string {
  const lines: string[] = [
    "# 由 xmesh 首次运行向导生成。之后可以在面板的「配置」页修改。",
    "node:",
    `  name: ${q(p.nodeName)}`,
    `  roles: [${p.roles.join(", ")}]`,
    "mesh:",
    `  mode: ${p.mode}`,
  ];
  if (p.mode === "tailnet") {
    lines.push(`  control_url: ${q(p.controlUrl ?? DEFAULT_CONTROL_URL)}`, `  authkey_file: ${q(p.authkeyFile ?? "")}`);
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
    '    deny: ["~/.ssh", "~/.config/gcloud", "~/.azure"]',
    "  authz:",
    `    "${p.mode === "tailnet" ? "tag:device" : "tag:peer"}":`,
    '      tools: ["*"]',
    '      backends: ["*"]',
  );
  return `${lines.join("\n")}\n`;
}
