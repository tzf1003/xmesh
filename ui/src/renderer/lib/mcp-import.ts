// Reads pasted MCP server JSON, the way Claude Code, Claude Desktop and similar clients write it,
// and turns it into kernel backends. Nothing is written here: the result is a preview with the
// entries that can be imported, the ones that cannot (with a reason), and warnings. An stdio server's
// `env` block is kept apart from the backend value: it goes to the kernel's private variable file, not
// into the config. Values never reach a note: problems and warnings name variables only.

import { BACKEND_ENV_MAX_VALUE_BYTES, BACKEND_ENV_MAX_VARS, ENV_VAR_NAME } from "../../shared/ipc";
import { BACKEND_NAME, BACKEND_POLICIES, DURATION, checkBackendUrl, isPlainObject, type BackendPolicy, type BackendValue, type Transport } from "./mcp";

/** Larger pastes are refused before parsing. */
export const IMPORT_MAX_BYTES = 64 * 1024;

/** `name` is empty for problems about the whole text. `detail` is a list of names, never a value. */
export interface ImportNote {
  name: string;
  code: string;
  detail?: string;
}

export interface ImportEntry {
  name: string;
  value: BackendValue;
  /** The server's environment variables, for an stdio server that has any. Sent to the kernel's private file, not written to the config. */
  env?: Record<string, string>;
  warnings: ImportNote[];
}

export interface ImportResult {
  entries: ImportEntry[];
  problems: ImportNote[];
}

/** Fields the kernel's backend shape (and this importer) understands, outside the transport-specific ones. */
const COMMON_KEYS = ["type", "transport", "cwd", "policy", "idle_timeout", "favorite", "env"];
// The kernel has no SSE client, so an `sse` entry is refused rather than written as a backend that cannot connect.
const HTTP_TYPES = ["http", "streamable-http"];

export function parseMcpImport(text: string): ImportResult {
  if (new TextEncoder().encode(text).length > IMPORT_MAX_BYTES) return { entries: [], problems: [{ name: "", code: "tooBig" }] };
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { entries: [], problems: [{ name: "", code: "json" }] };
  }
  if (!isPlainObject(raw)) return { entries: [], problems: [{ name: "", code: "shape" }] };
  const wrapped = isPlainObject(raw.mcpServers) ? raw.mcpServers : undefined;
  // A single server pasted without its name: its own fields are strings, where a flat map holds objects.
  if (wrapped === undefined && (typeof raw.command === "string" || typeof raw.url === "string")) {
    return { entries: [], problems: [{ name: "", code: "unnamed" }] };
  }
  const servers = wrapped ?? raw;
  const names = Object.keys(servers);
  if (names.length === 0) return { entries: [], problems: [{ name: "", code: "empty" }] };

  const entries: ImportEntry[] = [];
  const problems: ImportNote[] = [];
  for (const name of names) {
    const r = convertEntry(name, servers[name]);
    if ("problem" in r) problems.push(r.problem);
    else entries.push(r.entry);
  }
  return { entries, problems };
}

type Converted = { entry: ImportEntry } | { problem: ImportNote };

function convertEntry(name: string, raw: unknown): Converted {
  const fail = (code: string, detail?: string): Converted => ({ problem: detail === undefined ? { name, code } : { name, code, detail } });
  if (!BACKEND_NAME.test(name)) return fail("name.invalid");
  if (!isPlainObject(raw)) return fail("entry");

  // `type` wins over `transport` (the kernel's own word). Without either, a url means http and anything else stdio.
  const kind = typeof raw.type === "string" ? raw.type : typeof raw.transport === "string" ? raw.transport : undefined;
  let transport: Transport;
  if (kind === undefined) transport = raw.url !== undefined ? "http" : "stdio";
  else if (kind === "stdio") transport = "stdio";
  else if (HTTP_TYPES.includes(kind)) transport = "http";
  else return fail("transport", kind);

  // Policy and the optional fields are checked the same way for both transports.
  let policy: BackendPolicy = "shared";
  if (raw.policy !== undefined) {
    if (typeof raw.policy !== "string" || !(BACKEND_POLICIES as readonly string[]).includes(raw.policy)) return fail("policy.invalid");
    policy = raw.policy as BackendPolicy;
  }
  let cwd: string | undefined;
  if (raw.cwd !== undefined) {
    if (typeof raw.cwd !== "string") return fail("cwd.invalid");
    if (raw.cwd.trim() !== "") cwd = raw.cwd.trim();
  }
  let idle: string | undefined;
  if (raw.idle_timeout !== undefined) {
    const s = typeof raw.idle_timeout === "string" ? raw.idle_timeout.trim() : "";
    if (s !== "" && !DURATION.test(s)) return fail("idle.invalid");
    if (s !== "") idle = s;
  }

  let value: BackendValue;
  const used: string[] = [];
  if (transport === "stdio") {
    used.push("command", "args");
    const argv = stdioArgv(raw);
    if ("problem" in argv) return fail(argv.problem);
    if (argv.argv.length === 0 || argv.argv[0]!.trim() === "") return fail("command.required");
    value = { transport, command: argv.argv, policy };
  } else {
    used.push("url");
    if (raw.url === undefined) return fail("url.required");
    if (typeof raw.url !== "string" || raw.url.trim() === "") return fail("url.invalid");
    const url = raw.url.trim();
    const code = checkBackendUrl(url);
    if (code !== undefined) return fail(code);
    value = { transport, url, policy };
  }
  if (cwd !== undefined) value.cwd = cwd;
  if (idle !== undefined) value.idle_timeout = idle;
  if (raw.favorite === true) value.favorite = true;

  const warnings: ImportNote[] = [];
  let env: Record<string, string> | undefined;
  if (raw.env !== undefined) {
    if (!isPlainObject(raw.env)) return fail("env.shape");
    const checked = envBlock(raw.env);
    if ("problem" in checked) return fail("env.invalid", checked.problem);
    if (checked.vars.length > BACKEND_ENV_MAX_VARS) return fail("env.tooMany");
    if (checked.vars.length > 0) {
      if (transport === "stdio") env = Object.fromEntries(checked.vars);
      // A remote server has no process of ours to give variables to: say so, by name.
      else warnings.push({ name, code: "envIgnored", detail: checked.vars.map(([k]) => k).join(", ") });
    }
  }
  const ignored = Object.keys(raw).filter((k) => !COMMON_KEYS.includes(k) && !used.includes(k));
  if (ignored.length > 0) warnings.push({ name, code: "ignored", detail: ignored.join(", ") });
  return { entry: { name, value, ...(env !== undefined && { env }), warnings } };
}

/** The `env` block as name and value pairs, or the names that cannot be used (never a value). */
function envBlock(raw: Record<string, unknown>): { vars: [string, string][] } | { problem: string } {
  const bad: string[] = [];
  const vars: [string, string][] = [];
  for (const [k, v] of Object.entries(raw)) {
    const fine = ENV_VAR_NAME.test(k) && typeof v === "string" && !v.includes("\0") && new TextEncoder().encode(v).length <= BACKEND_ENV_MAX_VALUE_BYTES;
    if (fine) vars.push([k, v]);
    else bad.push(k);
  }
  return bad.length > 0 ? { problem: bad.join(", ") } : { vars };
}

/** `command` (a string plus optional `args`, or an array that already holds the argv), or the code of what is wrong with it. */
function stdioArgv(raw: Record<string, unknown>): { argv: string[] } | { problem: string } {
  let argv: string[];
  if (Array.isArray(raw.command)) {
    if (!raw.command.every((x) => typeof x === "string")) return { problem: "command.required" };
    argv = [...(raw.command as string[])];
  } else if (typeof raw.command === "string" && raw.command.trim() !== "") {
    argv = [raw.command.trim()];
  } else {
    return { problem: "command.required" };
  }
  if (raw.args !== undefined) {
    if (!Array.isArray(raw.args) || !raw.args.every((x) => typeof x === "string")) return { problem: "args.invalid" };
    argv.push(...(raw.args as string[]));
  }
  return { argv };
}
