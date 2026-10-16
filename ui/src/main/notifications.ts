import type { MessageKey, Translate } from "../shared/i18n";
import { isDegrading } from "../shared/control";
import type { Snapshot } from "../shared/state";

export interface Notice {
  /** Stable key so repeats can be de-duplicated. */
  key: string;
  message: MessageKey;
  params?: Record<string, string | number>;
}

/**
 * Notices for the change between two snapshots. Only the device name and a
 * status word are ever included: never a warning text, command or path, since
 * notifications show on lock screens and in notification history.
 * Nothing fires on the first connection or when the kernel was unreachable
 * in `prev`, because everything would look new.
 */
export function diffNotices(prev: Snapshot, next: Snapshot): Notice[] {
  const a = prev.connection;
  const b = next.connection;
  if (a.kind !== "connected" || b.kind !== "connected") return [];
  const out: Notice[] = [];

  const before = new Map(a.peers.filter((p) => !p.self).map((p) => [p.name, p.online]));
  for (const p of b.peers) {
    if (p.self) continue;
    const was = before.get(p.name);
    if (was === undefined || was === p.online) continue;
    out.push({
      key: `peer.${p.name}.${p.online ? "online" : "offline"}`,
      message: p.online ? "notice.peerOnline" : "notice.peerOffline",
      params: { name: p.name },
    });
  }

  if (!a.status.warnings.some(isDegrading) && b.status.warnings.some(isDegrading)) {
    out.push({ key: "degraded", message: "notice.degraded" });
  }
  if (!a.status.restart_required && b.status.restart_required) {
    out.push({ key: "restart", message: "notice.restartRequired" });
  }
  const la = a.status.limits;
  const lb = b.status.limits;
  if (lb.pty_max_sessions > 0 && lb.pty_in_use >= lb.pty_max_sessions && la.pty_in_use < la.pty_max_sessions) {
    out.push({ key: "quota.pty", message: "notice.quotaExhausted", params: { what: "pty" } });
  }
  if (lb.exec_shared > 0 && lb.exec_shared_in_use >= lb.exec_shared && la.exec_shared_in_use < la.exec_shared) {
    out.push({ key: "quota.exec", message: "notice.quotaExhausted", params: { what: "exec" } });
  }
  return out;
}

export function noticeText(n: Notice, t: Translate): string {
  if (n.message === "notice.quotaExhausted" && n.params) {
    const what = n.params.what === "exec" ? t("notice.quota.exec") : t("notice.quota.pty");
    return t(n.message, { what });
  }
  return t(n.message, n.params);
}
