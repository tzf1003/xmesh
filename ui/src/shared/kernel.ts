export const KERNEL_OPERATIONS = ["install", "uninstall", "reinstall", "start", "stop", "restart"] as const;
export type KernelOperation = (typeof KERNEL_OPERATIONS)[number];
export type KernelRequest = { op: "list" } | { op: KernelOperation; node: string };
export interface KernelNode { id: string; name: string; running: boolean; managed: boolean }
export interface KernelResult { nodes: KernelNode[] }

export function parseKernelRequest(raw: unknown): KernelRequest | undefined {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
  const v = raw as Record<string, unknown>;
  if (v.op === "list" && Object.keys(v).length === 1) return { op: "list" };
  if (!KERNEL_OPERATIONS.includes(v.op as KernelOperation) || typeof v.node !== "string" ||
    parseInstanceId(v.node) === undefined || Object.keys(v).some((k) => k !== "op" && k !== "node")) return undefined;
  return { op: v.op as KernelOperation, node: v.node };
}
import { parseInstanceId } from "./ipc";
