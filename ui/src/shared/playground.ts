import type { DataResult } from "./ipc";

export interface ToolDescriptor {
  name: string;
  description?: string;
  inputSchema: Record<string, unknown>;
  annotations?: { readOnlyHint?: boolean; destructiveHint?: boolean; [key: string]: unknown };
}

export interface ToolResult {
  content: { type: string; text?: string; mimeType?: string; data?: string; [key: string]: unknown }[];
  structuredContent?: Record<string, unknown>;
  isError?: boolean;
  [key: string]: unknown;
}

export interface CallOutcome {
  result: ToolResult;
  durationMs: number;
}

export type PlaygroundRequest =
  | { op: "list"; instance: string; device: string; pattern?: string }
  | { op: "call"; instance: string; id: string; name: string; arguments: Record<string, unknown>; timeoutMs: number; pat?: string }
  | { op: "cancel"; instance: string; id: string }
  | { op: "status"; instance: string }
  | { op: "copy" | "save"; instance: string; text: string };

export type PlaygroundResult<R extends PlaygroundRequest> =
  R extends { op: "list" } ? { tools: ToolDescriptor[]; truncated: boolean; revision: number } :
  R extends { op: "call" } ? CallOutcome :
  R extends { op: "status" } ? { revision: number; progress: Record<string, { progress: number; total?: number; message?: string }> } :
  R extends { op: "copy" | "save" } ? { done: boolean } :
  { cancelled: boolean };

export type PlaygroundApi = <R extends PlaygroundRequest>(request: R) => Promise<DataResult<PlaygroundResult<R>>>;

const plain = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const id = (v: unknown): v is string => typeof v === "string" && /^[A-Za-z0-9._-]{1,128}$/.test(v);

/** Bounds apply before any network request; URL, method, credential and origin are main-process choices. */
export function parsePlayground(v: unknown): PlaygroundRequest | undefined {
  if (!plain(v) || !id(v.instance)) return undefined;
  const keys = Object.keys(v);
  const only = (allowed: string[]): boolean => keys.every((k) => ["op", "instance", ...allowed].includes(k));
  if ((v.op === "copy" || v.op === "save") && only(["text"]) && typeof v.text === "string" && v.text.length <= 16 * 1024 * 1024) return v as PlaygroundRequest;
  if (v.op === "status" && only([])) return v as PlaygroundRequest;
  if (v.op === "cancel" && only(["id"]) && id(v.id)) return v as PlaygroundRequest;
  if (v.op === "list" && only(["device", "pattern"]) && (v.pattern === undefined || (typeof v.pattern === "string" && v.pattern.length > 0 && v.pattern.length <= 200)) && typeof v.device === "string" && /^(local|gateway|[A-Za-z0-9][A-Za-z0-9-]{0,62})$/.test(v.device)) return v as PlaygroundRequest;
  if (v.op !== "call" || !only(["id", "name", "arguments", "timeoutMs", "pat"]) || !id(v.id) || (v.pat !== undefined && !id(v.pat)) || typeof v.name !== "string" || v.name.length > 512 || !/^[A-Za-z0-9_./-]+$/.test(v.name) || !plain(v.arguments)) return undefined;
  if (!Number.isInteger(v.timeoutMs) || (v.timeoutMs as number) < 1000 || (v.timeoutMs as number) > 1_800_000) return undefined;
  try { if (new TextEncoder().encode(JSON.stringify(v.arguments)).length > 256 * 1024) return undefined; } catch { return undefined; }
  return v as PlaygroundRequest;
}

/** History keeps the argument shape; tool values may carry secrets under any key. */
export function historyArgs(value: unknown): unknown {
  if (Array.isArray(value)) return value.map((x) => historyArgs(x));
  if (plain(value)) return Object.fromEntries(Object.entries(value).map(([k, x]) => [k, historyArgs(x)]));
  return "[redacted]";
}
