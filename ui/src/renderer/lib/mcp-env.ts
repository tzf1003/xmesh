// The environment editor of an stdio backend (API keys and the like). A variable the kernel already
// holds is a "stored" row: the panel knows its name and never its value, so leaving the value empty
// keeps it, typing replaces it and removing deletes it. New rows need a name and a value. Nothing here
// keeps a value beyond the form that holds it: the page sends the result to the kernel and drops it.

import { BACKEND_ENV_MAX_VALUE_BYTES, BACKEND_ENV_MAX_VARS, ENV_VAR_NAME } from "../../shared/ipc";
import type { Problem } from "./mcp";

export interface EnvRow {
  /** Stable key for the list; not the variable name, which a new row's user is still typing. */
  id: number;
  name: string;
  /** What the user typed. Empty on a stored row means "keep the value the kernel has". */
  value: string;
  /** The kernel already holds a variable of this name. Its name cannot be edited. */
  stored: boolean;
  /** A stored row marked for deletion; saving removes the variable. */
  removed: boolean;
}

/** What one save sends for a backend: variables to write and variables to remove. */
export interface EnvChange {
  set: Record<string, string>;
  unset: string[];
}

let rowIds = 0;
export const nextRowId = (): number => ++rowIds;

export function storedRows(names: readonly string[]): EnvRow[] {
  return names.map((name) => ({ id: nextRowId(), name, value: "", stored: true, removed: false }));
}

export function blankRow(): EnvRow {
  return { id: nextRowId(), name: "", value: "", stored: false, removed: false };
}

/** A row the user has not touched: no name and no value. It is dropped silently rather than reported. */
const isBlank = (r: EnvRow): boolean => !r.stored && r.name.trim() === "" && r.value === "";

/** Problems with the rows, as codes the page words: `env.name`, `env.duplicate`, `env.value`, `env.tooLong`, `env.tooMany`. */
export function checkEnvRows(rows: readonly EnvRow[]): Problem[] {
  const problems: Problem[] = [];
  const seen = new Set<string>();
  let count = 0;
  for (const r of rows) {
    if (isBlank(r) || r.removed) continue;
    const name = r.name.trim();
    count++;
    if (!r.stored && !ENV_VAR_NAME.test(name)) problems.push({ field: "env", code: "env.name" });
    else if (seen.has(name)) problems.push({ field: "env", code: "env.duplicate" });
    seen.add(name);
    if (!r.stored && r.value === "" && ENV_VAR_NAME.test(name)) problems.push({ field: "env", code: "env.value" });
    if (new TextEncoder().encode(r.value).length > BACKEND_ENV_MAX_VALUE_BYTES || r.value.includes("\0")) problems.push({ field: "env", code: "env.tooLong" });
  }
  if (count > BACKEND_ENV_MAX_VARS) problems.push({ field: "env", code: "env.tooMany" });
  return dedupe(problems);
}

function dedupe(problems: Problem[]): Problem[] {
  const seen = new Set<string>();
  return problems.filter((p) => (seen.has(p.code) ? false : (seen.add(p.code), true)));
}

/**
 * The change a clean set of rows makes, or undefined when nothing changes. A stored row with a value is
 * overwritten; a removed one is deleted unless a new row brings the same name back, which is then just a write.
 */
export function envChangeOf(rows: readonly EnvRow[]): EnvChange | undefined {
  const set: Record<string, string> = {};
  const unset: string[] = [];
  for (const r of rows) {
    if (isBlank(r)) continue;
    const name = r.name.trim();
    if (r.removed) {
      if (r.stored) unset.push(name);
    } else if (r.value !== "") {
      set[name] = r.value;
    }
  }
  const kept = unset.filter((n) => !(n in set));
  return Object.keys(set).length === 0 && kept.length === 0 ? undefined : { set, unset: kept };
}

/** The names a backend will hold once the change lands, for the confirmation line. */
export function namesAfter(rows: readonly EnvRow[]): string[] {
  return rows.filter((r) => !isBlank(r) && !r.removed && (r.stored || r.value !== "")).map((r) => r.name.trim());
}
