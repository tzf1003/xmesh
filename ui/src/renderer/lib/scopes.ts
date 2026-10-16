import { parseScope } from "../../shared/ipc";

export interface ScopePreset {
  id: "read" | "shell" | "developer";
  scopes: string[];
}

/** Starting points for a new PAT; the user can edit the list before creating it. */
export const SCOPE_PRESETS: readonly ScopePreset[] = [
  { id: "read", scopes: ["devices:read"] },
  { id: "shell", scopes: ["devices:read", "exec:*", "pty:*"] },
  { id: "developer", scopes: ["devices:read", "exec:*", "pty:*", "fs:*"] },
];

/** Splits user input on commas, whitespace and newlines; drops empties and duplicates, keeps order. */
export function splitScopes(text: string): string[] {
  const out: string[] = [];
  for (const part of text.split(/[\s,，;；]+/)) {
    if (part !== "" && !out.includes(part)) out.push(part);
  }
  return out;
}

export interface ScopeCheck {
  scopes: string[];
  invalid: string[];
}

export function checkScopes(text: string): ScopeCheck {
  const scopes = splitScopes(text);
  return { scopes, invalid: scopes.filter((s) => parseScope(s) === undefined) };
}

/** A scope that grants a whole area (`exec:*`, `fs:*`, `backend:*:*`): worth a visible hint. */
export function isBroadScope(scope: string): boolean {
  return scope !== "devices:read" && /(^|:)\*(:|$)/.test(scope);
}
