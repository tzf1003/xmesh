// Pure logic for the MCP backends, the built-in tool switches and the tag grants on the
// authorization page: read helpers over the parsed config, validators that mirror the kernel's
// rules, and builders that turn a form into one batch of JSON Pointer edits. No DOM, no kernel.
// Problems are returned as codes (`name.invalid`, `url.host`, ...); the page owns the wording.

import { BUILTIN_TOOLS, BUILTIN_TOOL_NAMES } from "../../shared/builtin-tools";
import type { ConfigPatchEdit } from "../../shared/ipc";
import type { AuthzRow } from "./config";

/** The schema's backendName and nodeName grammar. */
export const BACKEND_NAME = /^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/;
/** The schema's agent.authz tag key grammar. */
export const TAG_KEY = /^tag:[A-Za-z0-9_*-]+$/;
/** The schema's duration grammar (`idle_timeout`): compound values such as 1h30m are allowed. */
export const DURATION = /^([0-9]+(\.[0-9]+)?)(ns|us|µs|ms|s|m|h)(([0-9]+(\.[0-9]+)?)(ns|us|µs|ms|s|m|h))*$/;
export const BACKEND_POLICIES = ["shared", "exclusive", "per-caller"] as const;
export type BackendPolicy = (typeof BACKEND_POLICIES)[number];
export type Transport = "stdio" | "http";

/** The contract's limit on edits in one PATCH. */
export const MAX_EDITS = 64;

/** The stored shape of one `agent.backends` entry, as this page writes it. There is no env field: the kernel has none. */
export interface BackendValue {
  transport: Transport;
  command?: string[];
  url?: string;
  cwd?: string;
  policy: BackendPolicy;
  idle_timeout?: string;
  favorite?: boolean;
}

/** One row of the MCP table, read from the parsed config. Values the kernel holds that the UI does not know are shown as text. */
export interface BackendRow {
  name: string;
  transport: string;
  command: string[];
  url: string;
  cwd: string;
  policy: string;
  idleTimeout: string;
  favorite: boolean;
}

/** A field the user got wrong. `code` is a stable identifier, such as `url.host`; the page maps it to text. */
export interface Problem {
  field: string;
  code: string;
}

export function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

function agentOf(parsed: unknown): Record<string, unknown> | undefined {
  const agent = isPlainObject(parsed) ? parsed.agent : undefined;
  return isPlainObject(agent) ? agent : undefined;
}

/** `agent.backends` as name and object pairs, in file order. Non-object entries are skipped. */
export function backendEntries(parsed: unknown): [string, Record<string, unknown>][] {
  const backends = agentOf(parsed)?.backends;
  if (!isPlainObject(backends)) return [];
  return Object.entries(backends).filter((e): e is [string, Record<string, unknown>] => isPlainObject(e[1]));
}

export function backendRows(parsed: unknown): BackendRow[] {
  return backendEntries(parsed).map(([name, b]) => ({
    name,
    transport: typeof b.transport === "string" ? b.transport : "",
    command: strings(b.command),
    url: typeof b.url === "string" ? b.url : "",
    cwd: typeof b.cwd === "string" ? b.cwd : "",
    policy: typeof b.policy === "string" ? b.policy : "",
    idleTimeout: typeof b.idle_timeout === "string" ? b.idle_timeout : "",
    favorite: b.favorite === true,
  }));
}

export function backendNames(parsed: unknown): string[] {
  return backendEntries(parsed).map(([name]) => name);
}

/** `agent.disabled_tools`. Absent or empty means every built-in tool is on. */
export function disabledTools(parsed: unknown): string[] {
  return strings(agentOf(parsed)?.disabled_tools);
}

/** A pattern as the authz lists use them: `*` matches any run of characters, everything else is literal. */
export function matchesPattern(pattern: string, name: string): boolean {
  const body = pattern.split("*").map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(".*");
  return new RegExp(`^${body}$`).test(name);
}

/** Tags whose backend list grants `name`, by exact name or by a pattern. Shown in the MCP table. */
export function grantedTags(rows: readonly AuthzRow[], name: string): string[] {
  return rows.filter((r) => r.backends.some((b) => matchesPattern(b, name))).map((r) => r.tag);
}

/** Tags whose backend list holds `name` exactly: what the grant checkboxes show and toggle. */
export function exactGrants(rows: readonly AuthzRow[], name: string): string[] {
  return rows.filter((r) => r.backends.includes(name)).map((r) => r.tag);
}

/** The catalog's order first, then names the kernel holds that this build does not know, in file order. */
export function orderTools(names: readonly string[]): string[] {
  const unique = [...new Set(names)];
  return [...BUILTIN_TOOL_NAMES.filter((n) => unique.includes(n)), ...unique.filter((n) => !BUILTIN_TOOL_NAMES.includes(n))];
}

/** Whether a built-in tool is switched on, given the parsed config. */
export function toolEnabled(parsed: unknown, tool: string): boolean {
  return !disabledTools(parsed).includes(tool);
}

// ---- Kernel rules for http URLs (internal/config: scheme, no userinfo, loopback or tailnet host) ----

/** Returns a problem code, or undefined when the kernel would accept the URL. */
export function checkBackendUrl(raw: string): string | undefined {
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)/.exec(raw);
  if (!m || (m[1]!.toLowerCase() !== "http" && m[1]!.toLowerCase() !== "https")) return "url.scheme";
  try {
    new URL(raw);
  } catch {
    return "url.invalid";
  }
  const authority = m[2]!;
  if (authority.includes("@")) return "url.userinfo";
  let host: string;
  if (authority.startsWith("[")) {
    const end = authority.indexOf("]");
    if (end === -1) return "url.invalid";
    host = authority.slice(1, end);
  } else {
    const colon = authority.lastIndexOf(":");
    host = colon === -1 ? authority : authority.slice(0, colon);
  }
  if (host === "localhost") return undefined;
  const v4 = parseIPv4(host);
  if (v4) return isLoopback4(v4) || isTailnet4(v4) ? undefined : "url.host";
  const v6 = host.includes(":") ? parseIPv6(host) : undefined;
  if (v6) return isLoopback6(v6) || isTailnet6(v6) ? undefined : "url.host";
  return "url.host";
}

/** Dotted quad with no leading zeros, as Go's netip accepts. */
function parseIPv4(s: string): number[] | undefined {
  const m = /^(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$/.exec(s);
  if (!m) return undefined;
  const octets = m.slice(1).map(Number);
  return octets.every((o) => o <= 255) ? octets : undefined;
}

/** Eight 16-bit groups. Zone identifiers and embedded IPv4 are refused, which only narrows what passes. */
function parseIPv6(s: string): number[] | undefined {
  if (s.includes("%") || s.includes(".")) return undefined;
  const halves = s.split("::");
  if (halves.length > 2) return undefined;
  const groups = (part: string): number[] | undefined => {
    if (part === "") return [];
    const out: number[] = [];
    for (const g of part.split(":")) {
      if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return undefined;
      out.push(parseInt(g, 16));
    }
    return out;
  };
  const head = groups(halves[0]!);
  if (!head) return undefined;
  if (halves.length === 1) return head.length === 8 ? head : undefined;
  const tail = groups(halves[1]!);
  if (!tail) return undefined;
  const fill = 8 - head.length - tail.length;
  if (fill < 1) return undefined;
  return [...head, ...new Array<number>(fill).fill(0), ...tail];
}

const isLoopback4 = (o: number[]): boolean => o[0] === 127;
const isTailnet4 = (o: number[]): boolean => o[0] === 100 && o[1]! >= 64 && o[1]! <= 127;
const isLoopback6 = (g: number[]): boolean => g.slice(0, 7).every((x) => x === 0) && g[7] === 1;
const isTailnet6 = (g: number[]): boolean => g[0] === 0xfd7a && g[1] === 0x115c && g[2] === 0xa1e0;

// ---- Form validation ----

/** What the add and edit dialog holds. `grants` are the tags checked; `newTag` is the free-text tag. */
export interface BackendForm {
  name: string;
  transport: Transport;
  /** One argv item per line; the first line is the program. */
  command: string;
  url: string;
  cwd: string;
  policy: string;
  idleTimeout: string;
  grants: string[];
  newTag: string;
  /** Kept from the stored entry: the form does not show it, but saving must not drop it. */
  favorite: boolean;
}

export type BackendCheck = { ok: true; name: string; value: BackendValue; tags: string[] } | { ok: false; problems: Problem[] };

/** Non-empty lines, trimmed. Blank lines are dropped; an argv item cannot be a blank line. */
export function argvLines(text: string): string[] {
  return text
    .split(/\r\n|\r|\n/)
    .map((l) => l.trim())
    .filter((l) => l !== "");
}

/** Validates the dialog and, when clean, returns the stored value and the final tag list. `editing` is the name being edited (renaming is not offered). */
export function checkBackendForm(form: BackendForm, existingNames: readonly string[], editing?: string): BackendCheck {
  const problems: Problem[] = [];
  const name = editing ?? form.name.trim();
  if (editing === undefined) {
    if (!BACKEND_NAME.test(name)) problems.push({ field: "name", code: "name.invalid" });
    else if (existingNames.includes(name)) problems.push({ field: "name", code: "name.duplicate" });
  }
  if (!(BACKEND_POLICIES as readonly string[]).includes(form.policy)) problems.push({ field: "policy", code: "policy.invalid" });
  const idle = form.idleTimeout.trim();
  if (idle !== "" && !DURATION.test(idle)) problems.push({ field: "idle", code: "idle.invalid" });
  const url = form.url.trim();
  const argv = argvLines(form.command);
  if (form.transport === "stdio") {
    if (argv.length === 0) problems.push({ field: "command", code: "command.required" });
  } else if (url === "") {
    problems.push({ field: "url", code: "url.required" });
  } else {
    const code = checkBackendUrl(url);
    if (code !== undefined) problems.push({ field: "url", code });
  }
  const tags = [...form.grants];
  const newTag = form.newTag.trim();
  if (newTag !== "") {
    if (!TAG_KEY.test(newTag)) problems.push({ field: "tag", code: "tag.invalid" });
    else if (!tags.includes(newTag)) tags.push(newTag);
  }
  if (problems.length > 0) return { ok: false, problems };

  const policy = form.policy as BackendPolicy;
  const value: BackendValue =
    form.transport === "stdio" ? { transport: "stdio", command: argv, policy } : { transport: "http", url, policy };
  if (form.cwd.trim() !== "") value.cwd = form.cwd.trim();
  if (idle !== "") value.idle_timeout = idle;
  if (form.favorite) value.favorite = true;
  return { ok: true, name, value, tags };
}

// ---- Edit batches ----

/** A JSON Pointer from segments: `~` and `/` are escaped as the RFC requires. */
export function pointer(...segments: string[]): string {
  return segments.map((s) => `/${s.replace(/~/g, "~0").replace(/\//g, "~1")}`).join("");
}

export const agentBackendPath = (name: string): string => pointer("agent", "backends", name);
export const authzPath = (tag: string): string => pointer("agent", "authz", tag);
export const authzBackendsPath = (tag: string): string => pointer("agent", "authz", tag, "backends");
export const DISABLED_TOOLS_PATH = pointer("agent", "disabled_tools");

/**
 * The tag grants, held as a working copy so that several backends in one batch compose: each edit
 * writes a whole list computed from the previous one, and edits apply in order.
 */
export type AuthzState = Map<string, { tools: string[]; backends: string[] }>;

export function authzState(rows: readonly AuthzRow[]): AuthzState {
  return new Map(rows.map((r): [string, { tools: string[]; backends: string[] }] => [r.tag, { tools: [...r.tools], backends: [...r.backends] }]));
}

/**
 * Edits that put `name` in exactly the tags in `wanted`. With `prune` false, tags that already list
 * it stay as they are. A wanted tag that does not exist yet is created with empty tools.
 */
export function grantEdits(state: AuthzState, name: string, wanted: readonly string[], prune: boolean): ConfigPatchEdit[] {
  const edits: ConfigPatchEdit[] = [];
  for (const [tag, entry] of state) {
    const has = entry.backends.includes(name);
    const want = wanted.includes(tag);
    if (want && !has) {
      entry.backends = [...entry.backends, name];
      edits.push({ path: authzBackendsPath(tag), value: entry.backends });
    } else if (!want && has && prune) {
      entry.backends = entry.backends.filter((b) => b !== name);
      edits.push({ path: authzBackendsPath(tag), value: entry.backends });
    }
  }
  for (const tag of wanted) {
    if (state.has(tag)) continue;
    state.set(tag, { tools: [], backends: [name] });
    edits.push({ path: authzPath(tag), value: { backends: [name], tools: [] } });
  }
  return edits;
}

/** Add or edit one backend: its value, then the grant changes. */
export function backendEdits(state: AuthzState, name: string, value: BackendValue, wanted: readonly string[], prune: boolean): ConfigPatchEdit[] {
  return [{ path: agentBackendPath(name), value }, ...grantEdits(state, name, wanted, prune)];
}

/** Delete one backend and remove its exact name from every tag list in the same batch. Patterns that merely match it stay. */
export function backendDeleteEdits(rows: readonly AuthzRow[], name: string): ConfigPatchEdit[] {
  const edits: ConfigPatchEdit[] = [{ path: agentBackendPath(name), delete: true }];
  for (const r of rows) {
    if (r.backends.includes(name)) edits.push({ path: authzBackendsPath(r.tag), value: r.backends.filter((b) => b !== name) });
  }
  return edits;
}

/**
 * Switches one built-in tool on or off by rewriting `agent.disabled_tools` as a whole array. An empty
 * result deletes the key, since absent means every tool is on. No change gives no edits.
 */
export function toolToggleEdits(current: readonly string[], tool: string, enabled: boolean): ConfigPatchEdit[] {
  const isDisabled = current.includes(tool);
  if (isDisabled === !enabled) return [];
  const next = orderTools(enabled ? current.filter((t) => t !== tool) : [...current, tool]);
  if (next.length === 0) return [{ path: DISABLED_TOOLS_PATH, delete: true }];
  return [{ path: DISABLED_TOOLS_PATH, value: next }];
}

// ---- Authorization (agent.authz) form ----

/** What the tag dialog holds. `original` is the tag being edited (undefined for a new tag). */
export interface AuthzForm {
  original?: string;
  tag: string;
  builtin: string[];
  extraTools: string;
  backends: string[];
  extraBackends: string;
}

export type AuthzCheck = { ok: true; tag: string; tools: string[]; backends: string[] } | { ok: false; problems: Problem[] };

/** Non-empty trimmed lines; a pattern cannot hold a space. */
function patternLines(text: string): { items: string[]; spaced: boolean } {
  const items: string[] = [];
  let spaced = false;
  for (const raw of text.split(/\r\n|\r|\n/)) {
    const line = raw.trim();
    if (line === "") continue;
    if (/\s/.test(line)) {
      spaced = true;
      continue;
    }
    if (!items.includes(line)) items.push(line);
  }
  return { items, spaced };
}

export function checkAuthzForm(form: AuthzForm, existingTags: readonly string[]): AuthzCheck {
  const problems: Problem[] = [];
  const tag = form.tag.trim();
  if (!TAG_KEY.test(tag)) problems.push({ field: "tag", code: "tag.invalid" });
  else if (tag !== form.original && existingTags.includes(tag)) problems.push({ field: "tag", code: "tag.duplicate" });
  const tools = BUILTIN_TOOLS.map((t) => t.name).filter((n) => form.builtin.includes(n));
  const extraTools = patternLines(form.extraTools);
  const extraBackends = patternLines(form.extraBackends);
  if (extraTools.spaced) problems.push({ field: "extraTools", code: "extra.space" });
  if (extraBackends.spaced) problems.push({ field: "extraBackends", code: "extra.space" });
  for (const t of extraTools.items) if (!tools.includes(t)) tools.push(t);
  const backends = [...new Set(form.backends)];
  for (const b of extraBackends.items) if (!backends.includes(b)) backends.push(b);
  if (problems.length > 0) return { ok: false, problems };
  return { ok: true, tag, tools, backends };
}

/** Save a tag: the new value, and removal of the old key when the tag was renamed. */
export function authzSaveEdits(original: string | undefined, tag: string, tools: readonly string[], backends: readonly string[]): ConfigPatchEdit[] {
  const edits: ConfigPatchEdit[] = [];
  if (original !== undefined && original !== tag) edits.push({ path: authzPath(original), delete: true });
  edits.push({ path: authzPath(tag), value: { backends: [...backends], tools: [...tools] } });
  return edits;
}

export function authzDeleteEdits(tag: string): ConfigPatchEdit[] {
  return [{ path: authzPath(tag), delete: true }];
}
