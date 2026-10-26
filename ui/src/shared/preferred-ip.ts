import type { PreferredIPInspectResponse, PreferredIPState } from "./control";

export type PreferredIpRequest = { op: "get" } | { op: "run"; task: "scan" | "refresh" } | { op: "inspect"; text: string };
export type PreferredIpResult = PreferredIPState | PreferredIPInspectResponse;
export type PreferredIpResultFor<R extends PreferredIpRequest> = R extends { op: "inspect" } ? PreferredIPInspectResponse : PreferredIPState;

/** The inspect body must fit the control channel's 64 KiB request limit once JSON-escaped. */
export const PREFERRED_IP_TEXT_MAX = 32768;

export function parsePreferredIpRequest(value: unknown): PreferredIpRequest | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const v = value as Record<string, unknown>;
  const keys = Object.keys(v);
  if (v.op === "get" && keys.length === 1) return { op: "get" };
  if (v.op === "run" && keys.length === 2 && (v.task === "scan" || v.task === "refresh")) return { op: "run", task: v.task };
  if (v.op === "inspect" && keys.length === 2 && typeof v.text === "string" && v.text.length <= PREFERRED_IP_TEXT_MAX) return { op: "inspect", text: v.text };
  return undefined;
}
