import type { ComputerState } from "./control";

export type ComputerRequest = { op: "get" } | { op: "setup" } | { op: "pause"; paused: boolean } | { op: "revoke"; session_id: string };
export type ComputerResult = ComputerState;

export function parseComputerRequest(value: unknown): ComputerRequest | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const v = value as Record<string, unknown>;
  const keys = Object.keys(v);
  if (v.op === "get" && keys.length === 1) return { op: "get" };
  if (v.op === "setup" && keys.length === 1) return { op: "setup" };
  if (v.op === "pause" && keys.length === 2 && typeof v.paused === "boolean") return { op: "pause", paused: v.paused };
  if (v.op === "revoke" && keys.length === 2 && typeof v.session_id === "string" && /^cs-[A-Za-z0-9_-]{8,128}$/.test(v.session_id)) return { op: "revoke", session_id: v.session_id };
  return undefined;
}
