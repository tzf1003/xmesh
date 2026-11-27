import type {
  DelegateRepliesResponse,
  DelegateReplyDetail,
  DelegateReplyState,
  DelegateReplySummary,
} from "./control";

export type DelegateReplyRequest =
  | { op: "list"; state?: DelegateReplyState }
  | { op: "preview"; reply_id: string }
  | { op: "approve"; reply_id: string; body_sha256?: string }
  | { op: "reject"; reply_id: string };

export type DelegateReplyResultFor<R extends DelegateReplyRequest> =
  R extends { op: "list" } ? DelegateRepliesResponse
  : R extends { op: "preview" } ? DelegateReplyDetail
  : DelegateReplySummary;

const REPLY_STATES = new Set<string>(["pending", "approved", "rejected", "expired", "cancelled"]);
const REPLY_ID = /^[A-Za-z0-9._-]{1,128}$/;
const SHA256_HEX = /^[0-9a-f]{64}$/;

export function parseDelegateReplyRequest(value: unknown): DelegateReplyRequest | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const v = value as Record<string, unknown>;
  const keys = Object.keys(v);
  switch (v.op) {
    case "list":
      if (keys.length === 1) return { op: "list" };
      if (keys.length === 2 && typeof v.state === "string" && REPLY_STATES.has(v.state)) {
        return { op: "list", state: v.state as DelegateReplyState };
      }
      return undefined;
    case "preview":
      if (keys.length === 2 && typeof v.reply_id === "string" && REPLY_ID.test(v.reply_id)) {
        return { op: "preview", reply_id: v.reply_id };
      }
      return undefined;
    case "approve":
      if (typeof v.reply_id !== "string" || !REPLY_ID.test(v.reply_id)) return undefined;
      if (keys.length === 2) return { op: "approve", reply_id: v.reply_id };
      if (keys.length === 3 && typeof v.body_sha256 === "string" && SHA256_HEX.test(v.body_sha256)) {
        return { op: "approve", reply_id: v.reply_id, body_sha256: v.body_sha256 };
      }
      return undefined;
    case "reject":
      if (keys.length === 2 && typeof v.reply_id === "string" && REPLY_ID.test(v.reply_id)) {
        return { op: "reject", reply_id: v.reply_id };
      }
      return undefined;
    default:
      return undefined;
  }
}
