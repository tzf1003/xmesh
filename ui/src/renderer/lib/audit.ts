import type { AuditEvent, AuditQuery } from "../../shared/control";

export interface AuditForm {
  /** `datetime-local` values (`YYYY-MM-DDTHH:mm`), local time, or "". */
  since: string;
  until: string;
  event: string;
  tool: string;
  device: string;
  caller: string;
  status: "" | "ok" | "error";
}

export const EMPTY_AUDIT_FORM: AuditForm = { since: "", until: "", event: "", tool: "", device: "", caller: "", status: "" };

/** Local `datetime-local` text to the UTC timestamp the kernel expects; `undefined` when empty or invalid. */
export function toTimestamp(local: string): string | undefined {
  if (local === "") return undefined;
  const d = new Date(local);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

export function buildAuditQuery(form: AuditForm, limit = 100, cursor?: string): AuditQuery {
  const q: AuditQuery = { limit: String(limit) };
  const since = toTimestamp(form.since);
  const until = toTimestamp(form.until);
  if (since) q.since = since;
  if (until) q.until = until;
  if (form.event !== "") q.event = form.event;
  if (form.tool.trim() !== "") q.tool = form.tool.trim();
  if (form.device.trim() !== "") q.device = form.device.trim();
  if (form.caller.trim() !== "") q.caller = form.caller.trim();
  if (form.status !== "") q.status = form.status;
  if (cursor) q.cursor = cursor;
  return q;
}

/** The one-line summary of what an event touched, from the digest fields the audit schema allows. */
export function auditTarget(e: AuditEvent): string {
  const d = e.args_digest;
  if (!d) return "";
  const parts: string[] = [];
  if (d.cmd) parts.push(d.cmd);
  if (d.resolved_path) parts.push(d.resolved_path);
  if (d.backend) parts.push(d.backend);
  if (d.session_id) parts.push(d.session_id);
  if (d.signal) parts.push(d.signal);
  return parts.join("  ");
}
