import type { JSX } from "preact";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "./app";
import { getApi } from "./api";
import { Confirm } from "./components";
import { Icon } from "./icons";
import { useCopy } from "./design-copy";
import { useResource } from "./hooks";
import { errorText, useT } from "./i18n";
import { applyConfigEdits } from "./lib/apply-config";
import { diffRows, type DiffRow } from "./lib/config";
import { applySaved } from "./lib/reload";
import { checkIngressSettings, formFromConfig, ROLES, toggleRole, type Role } from "./lib/settings";
import type { ConfigPatchEdit } from "../shared/ipc";
import type { Key } from "./messages";

const HINT: Record<Role, Key> = { agent: "wizard.agentHint", gateway: "wizard.gatewayHint", ingress: "settings.ingressHint" };
interface Review { role: Role; disabling: boolean; etag: string; edits: ConfigPatchEdit[]; rows: DiffRow[]; restart: boolean }

export function RoleSwitches({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  const copy = useCopy();
  const [menu, setMenu] = useState<Role>();
  const root = useRef<HTMLDivElement>(null);
  const pending = useRef<AbortController>();
  const [waiting, setWaiting] = useState(0);
  const [unknownCount, setUnknownCount] = useState(0);
  useEffect(() => {
    const close = (e: PointerEvent): void => { if (!root.current?.contains(e.target as Node)) setMenu(undefined); };
    document.addEventListener("pointerdown", close);
    return () => { document.removeEventListener("pointerdown", close); pending.current?.abort(); };
  }, []);
  useLayoutEffect(() => { if (menu) root.current?.querySelector<HTMLElement>('[role="menuitem"]:not(:disabled)')?.focus(); }, [menu]);
  const connection = ctx.snapshot.connection;
  const supported = ctx.can("config.read");
  const liveRoles: readonly Role[] = connection.kind === "connected" ? connection.status.roles : [];
  const restartPending = connection.kind === "connected" && connection.status.restart_required;
  const { resource, reload } = useResource(() => getApi().getConfig(), [ctx.snapshot.selectedInstance, supported, ctx.configRevision, ctx.configDirty, restartPending, liveRoles.join(",")], supported ? 2000 : undefined);
  const [busy, setBusy] = useState(false);
  const [review, setReview] = useState<Review>();
  if (connection.kind !== "connected") return null;
  const configured: readonly Role[] = resource.state === "ready" ? formFromConfig(resource.data.parsed).roles : liveRoles;

  const prepare = async (role: Role): Promise<void> => {
    if (busy || ctx.configDirty) return;
    setBusy(true);
    try {
      const current = await getApi().getConfig();
      if (!current.ok) { ctx.toast(errorText(t, current.error), true); return; }
      if (!current.data.valid) { ctx.go("settings", { settingsTab: "configuration" }); return; }
      const form = formFromConfig(current.data.parsed);
      const roles = toggleRole(form.roles, role, !form.roles.includes(role));
      if (roles.length === 0) { ctx.toast(t("settings.problem.roles"), true); return; }
      if (role === "ingress" && roles.includes("ingress") && checkIngressSettings(form) !== undefined) {
        setMenu(undefined); ctx.openSettings("ingress");
        return;
      }
      const edits: ConfigPatchEdit[] = [{ path: "/node/roles", value: roles }];
      const parsed = current.data.parsed as { gateway?: { listen?: string } } | null;
      if (roles.includes("gateway") && !parsed?.gateway?.listen) edits.push({ path: "/gateway/listen", value: "127.0.0.1:8080" });
      const result = await applyConfigEdits(current.data.etag, edits, true);
      if (!result.ok) { ctx.toast(errorText(t, result.error), true); reload(); return; }
      setMenu(undefined); setUnknownCount(0);
      setReview({ role, disabling: form.roles.includes(role), etag: current.data.etag, edits, rows: diffRows(result.data.diff), restart: result.data.restart_required });
    } finally { setBusy(false); }
  };
  const save = async (): Promise<void> => {
    if (!review || busy || ctx.configDirty) return;
    setBusy(true);
    const abort = new AbortController(); pending.current = abort;
    try {
      if (review.disabling && review.role !== "ingress" && ctx.can("sessions")) {
        while (!abort.signal.aborted) {
          const sessions = await getApi().getSessions();
          if (!sessions.ok) { ctx.toast(sessions.error.message, true); return; }
          const calls = (sessions.data as { executions?: {state:string;tool:string;layer:string}[] }).executions;
          const roleCalls = calls?.filter(call => call.layer === (review.role === "agent" ? "agent" : "gateway") && !call.tool.endsWith("pty.read"));
          const unknown = roleCalls?.filter(call => call.state === "unknown").length ?? 0;
          if (unknown > 0 && unknownCount === 0) { setUnknownCount(unknown); return; }
          const count = roleCalls ? roleCalls.filter(call => call.state !== "unknown").length : sessions.data.exec.length;
          setWaiting(count);
          if (!count) break;
          await new Promise<void>(resolve => {
            const done = (): void => { clearTimeout(timer); abort.signal.removeEventListener("abort", done); resolve(); };
            const timer = setTimeout(done, 1000);
            abort.signal.addEventListener("abort", done, { once: true });
          });
        }
      }
      if (abort.signal.aborted) return;
      const result = await applyConfigEdits(review.etag, review.edits);
      setReview(undefined);
      if (!result.ok) { ctx.toast(errorText(t, result.error), true); reload(); return; }
      ctx.applied(await applySaved(result.data.restart_required));
      ctx.toast(t("config.saved"));
      ctx.refreshConfig();
      reload();
    } finally { pending.current = undefined; setWaiting(0); setBusy(false); }
  };
  return <>
    <div class="role-capsules" ref={root} role="group" aria-label={t("wizard.roles")} aria-busy={busy} data-testid="role-switches" onKeyDown={e => {
      if (!menu) return;
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); root.current?.querySelector<HTMLElement>(`[data-testid="role-${menu}"]`)?.focus(); setMenu(undefined); }
      if (["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) {
        e.preventDefault();
        const items = [...(root.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not(:disabled)') ?? [])];
        const at = items.indexOf(document.activeElement as HTMLElement);
        const next = e.key === "Home" ? 0 : e.key === "End" ? items.length - 1 : (at + (e.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
        items[next]?.focus();
      }
    }}>
      {ROLES.map(role => {
        const enabled = configured.includes(role), running = liveRoles.includes(role);
        return <button key={role} type="button" class={`role-capsule ${enabled ? "enabled" : ""}`} aria-haspopup="menu" aria-expanded={menu === role} data-enabled={enabled} data-pending={enabled !== running} disabled={busy || review !== undefined} title={t(HINT[role])} onClick={() => setMenu(menu === role ? undefined : role)} onKeyDown={e => { if (!menu && ["ArrowDown", "ArrowUp"].includes(e.key)) { e.preventDefault(); e.stopPropagation(); setMenu(role); } }} data-testid={`role-${role}`}><span class="capsule-dot"/>{role[0]!.toUpperCase() + role.slice(1)}<Icon name="chevron"/></button>;
      })}
      {menu && <div class="role-menu" role="menu" data-testid="role-menu"><div class="role-menu-heading"><strong>{menu[0]!.toUpperCase() + menu.slice(1)}</strong><span>{configured.includes(menu) ? copy("已开启", "Enabled") : copy("已关闭", "Disabled")}</span><p>{t(HINT[menu])}</p></div>
        <button type="button" role="menuitem" data-testid="role-menu-toggle" disabled={busy || ctx.configDirty || !ctx.can("config.write") || !supported || resource.state !== "ready" || (configured.includes(menu) && configured.length === 1)} onClick={() => void prepare(menu)}><Icon name="runner"/>{configured.includes(menu) ? copy("关闭", "Disable") : copy("开启", "Enable")} {menu[0]!.toUpperCase() + menu.slice(1)}</button>
        {configured.includes(menu) && configured.length === 1 && <p class="hint">{copy("至少保留一个角色开启。", "At least one role must stay enabled.")}</p>}
        <button type="button" role="menuitem" data-testid="role-menu-settings" disabled={!supported || ctx.configDirty} onClick={() => { ctx.openSettings(menu); setMenu(undefined); }}><Icon name="sliders"/>{copy("相关设置", "Role settings")}<Icon name="chevron"/></button>
      </div>}
    </div>
    {review && <Confirm title={t("roles.reviewTitle")} body={<>
      {review.disabling && <p class="hint">{copy("停止接收新调用前等待当前执行完成。相关终端和客户端连接可能关闭。", "Wait for current calls before stopping. Related terminal and client connections may close.")}</p>}
      {unknownCount > 0 && <p role="alert" class="hint">{copy("部分任务状态无法确认，继续关闭可能中断这些任务。请再次确认。", "Some task states are unknown. Continuing may interrupt them; confirm again.")}</p>}
      {waiting > 0 && <p role="status">{copy("正在等待执行完成：", "Waiting for calls: ")}{waiting}</p>}
      <div class="table-wrap" data-testid="role-config-diff"><table>
        <thead><tr><th>{t("config.path")}</th><th>{t("config.before")}</th><th>{t("config.after")}</th></tr></thead>
        <tbody>{review.rows.map((row) => <tr key={row.pointer}><td class="mono">{row.pointer}</td><td class="mono">{row.before}</td><td class="mono">{row.after}</td></tr>)}</tbody>
      </table></div>
      {review.restart && <p class="hint">{t("apply.restart")}</p>}
    </>} confirmLabel={unknownCount > 0 ? copy("确认继续关闭", "Disable with unknown tasks") : t("roles.review")} busy={busy || ctx.configDirty} onCancel={() => { pending.current?.abort(); setReview(undefined); }} onConfirm={() => void save()} />}
  </>;
}
