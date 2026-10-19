export interface TransferState {
  id: string;
  direction: "upload" | "download";
  phase: "preparing" | "sending" | "checking" | "complete" | "failed" | "cancelled";
  name: string;
  remote: string;
  device: string;
  bytes: number;
  total?: number;
  sha256?: string;
  error?: string;
  code?: string;
}
export type TransferRequest =
  | { op: "pick"; instance: string }
  | { op: "status"; instance: string }
  | { op: "cancel"; instance: string; id: string }
  | { op: "retry"; instance: string; id: string }
  | {
      op: "start";
      instance: string;
      direction: "upload" | "download";
      device: string;
      path: string;
      cwd?: string;
      ticket?: string;
      force?: boolean;
    };
export type TransferReply = {
  ticket?: string;
  name?: string;
  bytes?: number;
  states?: TransferState[];
  id?: string;
  cancelled?: boolean;
};
export function parseTransfer(v: unknown): TransferRequest | undefined {
  if (!v || typeof v !== "object" || Array.isArray(v)) return undefined;
  const r = v as Record<string, unknown>;
  if (typeof r.instance !== "string" || !/^[A-Za-z0-9._-]{1,128}$/.test(r.instance)) return undefined;
  const only = (keys: string[]) => Object.keys(r).every((k) => ["op", "instance", ...keys].includes(k));
  if ((r.op === "pick" || r.op === "status") && only([])) return r as TransferRequest;
  if ((r.op === "cancel" || r.op === "retry") && only(["id"]) && typeof r.id === "string" && /^[a-z0-9-]{1,64}$/.test(r.id))
    return r as TransferRequest;
  if (r.op !== "start" || !only(["direction", "device", "path", "cwd", "ticket", "force"])) return undefined;
  if (r.direction !== "upload" && r.direction !== "download") return undefined;
  if (typeof r.device !== "string" || !/^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/.test(r.device)) return undefined;
  if (typeof r.path !== "string" || r.path.length < 1 || r.path.length > 4096 || r.path.includes("\0"))
    return undefined;
  if (r.cwd !== undefined && (typeof r.cwd !== "string" || r.cwd.length > 4096 || r.cwd.includes("\0")))
    return undefined;
  if (r.ticket !== undefined && (typeof r.ticket !== "string" || !/^[a-z0-9-]{1,64}$/.test(r.ticket))) return undefined;
  if (r.force !== undefined && typeof r.force !== "boolean") return undefined;
  if (r.direction === "upload" && !r.ticket) return undefined;
  return r as TransferRequest;
}
