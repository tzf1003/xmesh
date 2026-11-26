import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { DelegateState } from "../../shared/control";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Empty, NotSupported, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { useT, type T } from "../i18n";
import { formatDuration, formatTime } from "../lib/format";
import { applySaved } from "../lib/reload";

type PolicyKey = "allow_write" | "allow_sessions";

interface PendingPolicy {
  key: PolicyKey;
  next: boolean;
}

function taskState(state: string, t: T): string {
  switch (state) {
    case "running": return t("delegate.state.running");
    case "done": return t("delegate.state.done");
    case "failed": return t("delegate.state.failed");
    case "timeout": return t("delegate.state.timeout");
    case "cancelled": return t("delegate.state.cancelled");
    default: return state;
  }
}

function sessionStatus(status: string, t: T): string {
  if (status === "busy") return t("delegate.busy");
  if (status === "idle") return t("delegate.idle");
  return status;
}

export function Delegate({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  if (!ctx.can("delegate")) return <div class="page"><PageHead title={t("nav.delegate")} /><NotSupported what={t("nav.delegate")} /></div>;
  return <DelegatePage ctx={ctx} />;
}

function DelegatePage({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const { resource, reload } = useResource(() => getApi().delegate(), [ctx.snapshot.selectedInstance], 2000);
  const [busy, setBusy] = useState(false);
  const [cancelling, setCancelling] = useState<string>();
  const [pendingPolicy, setPendingPolicy] = useState<PendingPolicy>();

  const patchDelegateField = async (key: "enabled" | PolicyKey, value: boolean): Promise<boolean> => {
    setBusy(true);
    try {
      const loaded = await getApi().getConfig();
      if (!loaded.ok) { ctx.toast(loaded.error.message, true); return false; }
      const saved = await getApi().patchConfig({ edits: [{ path: `/agent/delegate/${key}`, value }], etag: loaded.data.etag, dryRun: false });
      if (!saved.ok) { ctx.toast(saved.error.message, true); return false; }
      ctx.applied(await applySaved(saved.data.restart_required));
      ctx.toast(t("delegate.saved"));
      reload();
      return true;
    } finally { setBusy(false); }
  };

  const confirmPolicy = async (): Promise<void> => {
    if (!pendingPolicy) return;
    if (await patchDelegateField(pendingPolicy.key, pendingPolicy.next)) {
      setPendingPolicy(undefined);
    }
  };

  const cancelTask = async (taskId: string): Promise<void> => {
    setCancelling(taskId);
    try {
      const res = await getApi().cancelDelegateTask(taskId);
      if (!res.ok) ctx.toast(res.error.message, true);
      else ctx.toast(t(res.data.state === "cancelled" ? "delegate.taskCancelled" : "delegate.taskAlreadyEnded"));
      reload();
    } finally { setCancelling(undefined); }
  };

  const confirmTitle = pendingPolicy
    ? t(
        pendingPolicy.key === "allow_write"
          ? pendingPolicy.next ? "delegate.confirmWriteEnableTitle" : "delegate.confirmWriteDisableTitle"
          : pendingPolicy.next ? "delegate.confirmSessionsEnableTitle" : "delegate.confirmSessionsDisableTitle",
      )
    : "";
  const confirmBody = pendingPolicy
    ? t(
        pendingPolicy.key === "allow_write"
          ? pendingPolicy.next ? "delegate.confirmWriteEnableBody" : "delegate.confirmWriteDisableBody"
          : pendingPolicy.next ? "delegate.confirmSessionsEnableBody" : "delegate.confirmSessionsDisableBody",
      )
    : "";
  const confirmRisk = pendingPolicy?.next
    ? t(pendingPolicy.key === "allow_write" ? "delegate.confirmWriteEnableRisk" : "delegate.confirmSessionsEnableRisk")
    : undefined;

  return <div class="page" data-testid="page-delegate">
    <PageHead title={t("nav.delegate")} sub={t("delegate.sub")}><button type="button" class="btn" onClick={reload}>{t("common.refresh")}</button></PageHead>
    <ResourceView resource={resource}>{(state: DelegateState) => <>
      <section class="card stack">
        <div><Badge kind={state.enabled ? "ok" : "warn"}><span data-testid="delegate-state">{state.enabled ? t("delegate.enabled") : t("delegate.disabled")}</span></Badge></div>
        <div><button type="button" class="btn" disabled={busy || !ctx.can("config.write")} onClick={() => void patchDelegateField("enabled", !state.enabled)} data-testid="delegate-toggle">{t(state.enabled ? "delegate.disable" : "delegate.enable")}</button></div>
        {!state.enabled && <p class="hint">{t("delegate.hint")}</p>}
        <p data-testid="delegate-limits">{t("delegate.limits", { running: state.running, max: state.max_concurrent })}</p>
        <p class="hint" data-testid="delegate-policy">
          {t("delegate.policyWrite", { state: state.allow_write ? t("delegate.allowed") : t("delegate.denied") })}
          {" · "}
          {t("delegate.policySessions", { state: state.allow_sessions ? t("delegate.allowed") : t("delegate.denied") })}
        </p>
        <div class="setting-row">
          <div>
            <strong>{t("delegate.allowWriteTitle")}</strong>
            <p class="hint">{t("delegate.allowWriteDesc")}</p>
          </div>
          <div class="setting-control">
            <input
              type="checkbox"
              role="switch"
              aria-label={t("delegate.allowWriteTitle")}
              checked={state.allow_write}
              disabled={busy || !ctx.can("config.write")}
              onChange={(e) => {
                const next = e.currentTarget.checked;
                e.currentTarget.checked = state.allow_write;
                setPendingPolicy({ key: "allow_write", next });
              }}
              data-testid="delegate-allow-write"
            />
          </div>
        </div>
        <div class="setting-row">
          <div>
            <strong>{t("delegate.allowSessionsTitle")}</strong>
            <p class="hint">{t("delegate.allowSessionsDesc")}</p>
          </div>
          <div class="setting-control">
            <input
              type="checkbox"
              role="switch"
              aria-label={t("delegate.allowSessionsTitle")}
              checked={state.allow_sessions}
              disabled={busy || !ctx.can("config.write")}
              onChange={(e) => {
                const next = e.currentTarget.checked;
                e.currentTarget.checked = state.allow_sessions;
                setPendingPolicy({ key: "allow_sessions", next });
              }}
              data-testid="delegate-allow-sessions"
            />
          </div>
        </div>
      </section>
      <section class="card"><h2>{t("delegate.runners")}</h2>
        <div class="table-wrap"><table data-testid="delegate-runners"><thead><tr><th scope="col">{t("delegate.colName")}</th><th scope="col">{t("delegate.colState")}</th><th scope="col">{t("delegate.colPath")}</th></tr></thead>
          <tbody>{state.runners.map((runner) => <tr key={runner.name}><td class="mono">{runner.name}</td><td>{runner.available ? t("delegate.runnerAvailable") : t("delegate.runnerMissing")}</td><td class="mono">{runner.path ?? ""}</td></tr>)}</tbody>
        </table></div>
      </section>
      <section class="card"><h2>{t("delegate.sessions")}</h2>
        {!state.allow_sessions && <p class="hint">{t("delegate.sessionsOff")}</p>}
        {state.sessions.length === 0 ? <Empty>{t("delegate.noSessions")}</Empty> : <div class="table-wrap"><table data-testid="delegate-sessions"><thead><tr><th scope="col">{t("delegate.colSession")}</th><th scope="col">{t("delegate.colName")}</th><th scope="col">{t("delegate.colCwd")}</th><th scope="col">{t("delegate.colState")}</th><th scope="col">{t("delegate.colPid")}</th></tr></thead>
          <tbody>{state.sessions.map((session) => <tr key={session.session_id}><td class="mono">{session.session_id}</td><td>{session.name}</td><td class="mono">{session.cwd}</td><td>{sessionStatus(session.status, t)}</td><td class="mono">{session.pid}</td></tr>)}</tbody>
        </table></div>}
      </section>
      <section class="card"><h2>{t("delegate.tasks")}</h2>
        <p class="hint">{t("delegate.taskHint")}</p>
        {state.tasks.length === 0 ? <Empty>{t("delegate.noTasks")}</Empty> : <div class="table-wrap"><table data-testid="delegate-tasks"><thead><tr><th scope="col">{t("delegate.colTask")}</th><th scope="col">{t("delegate.colCaller")}</th><th scope="col">{t("delegate.colRunner")}</th><th scope="col">{t("delegate.colState")}</th><th scope="col">{t("delegate.colCwd")}</th><th scope="col">{t("delegate.colStarted")}</th><th scope="col">{t("delegate.colDuration")}</th><th scope="col">{t("delegate.colActions")}</th></tr></thead>
          <tbody>{state.tasks.map((task) => <tr key={task.task_id}><td class="mono">{task.task_id}</td><td class="mono">{task.caller}</td><td>{task.runner}</td><td data-testid={`delegate-task-state-${task.task_id}`}>{taskState(task.state, t)}</td><td class="mono">{task.cwd}</td><td>{formatTime(task.started_at)}</td><td>{formatDuration(task.duration_ms)}</td><td>{task.state === "running" ? <button type="button" class="btn small danger" disabled={busy || cancelling === task.task_id} onClick={() => void cancelTask(task.task_id)} data-testid={`delegate-task-cancel-${task.task_id}`}>{cancelling === task.task_id ? t("delegate.cancellingTask") : t("delegate.cancelTask")}</button> : "—"}</td></tr>)}</tbody>
        </table></div>}
      </section>
    </>}</ResourceView>
    {pendingPolicy && (
      <Confirm
        title={confirmTitle}
        body={<div class="stack"><p>{confirmBody}</p>{confirmRisk && <Banner kind="warn" testid="delegate-confirm-risk">{confirmRisk}</Banner>}</div>}
        confirmLabel={t(pendingPolicy.next ? "delegate.confirmEnable" : "delegate.confirmDisable")}
        danger={pendingPolicy.next}
        busy={busy}
        onCancel={() => setPendingPolicy(undefined)}
        onConfirm={() => void confirmPolicy()}
      />
    )}
  </div>;
}
