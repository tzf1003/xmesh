import type { ConfigChange } from "../../shared/control";
import { formatValue } from "./format";

export interface DiffRow {
  pointer: string;
  op: "add" | "remove" | "replace";
  before: string;
  after: string;
  restartRequired: boolean;
  weakens: boolean;
}

/** The kernel's diff for the table. `before`/`after` can be any JSON value despite the generated type. */
export function diffRows(changes: readonly ConfigChange[]): DiffRow[] {
  return changes.map((c) => ({
    pointer: c.pointer === "" ? "/" : c.pointer,
    op: c.op,
    before: c.op === "add" ? "" : formatValue(c.before),
    after: c.op === "remove" ? "" : formatValue(c.after),
    restartRequired: c.restart_required,
    weakens: c.weakens_security,
  }));
}

export function weakening(changes: readonly ConfigChange[]): ConfigChange[] {
  return changes.filter((c) => c.weakens_security);
}

export function needsRestart(changes: readonly ConfigChange[]): boolean {
  return changes.some((c) => c.restart_required);
}

export interface AuthzRow {
  tag: string;
  tools: string[];
  backends: string[];
}

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

/** `agent.authz` from a parsed config, one row per tag in key order. Anything else yields no rows. */
export function authzRows(parsed: unknown): AuthzRow[] {
  const agent = (parsed as { agent?: { authz?: unknown } } | null)?.agent;
  const authz = agent?.authz;
  if (typeof authz !== "object" || authz === null || Array.isArray(authz)) return [];
  return Object.entries(authz as Record<string, unknown>).map(([tag, v]) => {
    const o = (typeof v === "object" && v !== null ? v : {}) as { tools?: unknown; backends?: unknown };
    return { tag, tools: strings(o.tools), backends: strings(o.backends) };
  });
}

/** `agent.cwd_policy` allow and deny lists from a parsed config. */
export function cwdPolicy(parsed: unknown): { allow: string[]; deny: string[] } {
  const p = (parsed as { agent?: { cwd_policy?: { allow?: unknown; deny?: unknown } } } | null)?.agent?.cwd_policy;
  return { allow: strings(p?.allow), deny: strings(p?.deny) };
}

export function isDirty(original: string, draft: string): boolean {
  return original !== draft;
}
