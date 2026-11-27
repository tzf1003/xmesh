import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { DelegateRepliesResponse, DelegateReplyDetail, DelegateState } from "../../shared/control";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Empty, NotSupported, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { useT, type T } from "../i18n";
import { formatDuration, formatTime } from "../lib/format";
import { applySaved } from "../lib/reload";

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

function replyStateLabel(state: string, t: T): string {
  switch (state) {
    case "pending": return t("delegate.replyState.pending");
    case "approved": return t("delegate.replyState.approved");
    case "rejected": return t("delegate.replyState.rejected");
    case "expired": return t("delegate.replyState.expired");
    case "cancelled": return t("delegate.replyState.cancelled");
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
  const repliesRes = useResource(() => getApi().delegateReply({ op: "list" }), [ctx.snapshot.selectedInstance], 2000);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<DelegateReplyDetail | null>(null);
  const reloadAll = (): void => {
    reload();
    repliesRes.reload();
  };
  const toggle = async (enabled: boolean): Promise<void> => {
    setBusy(true);
    try {
      const loaded = await getApi().getConfig();
      if (!loaded.ok) { ctx.toast(loaded.error.message, true); return; }
      const saved = await getApi().patchConfig({ edits: [{ path: "/agent/delegate/enabled", value: enabled }], etag: loaded.data.etag, dryRun: false });
      if (!saved.ok) ctx.toast(saved.error.message, true);
      else { ctx.applied(await applySaved(saved.data.restart_required)); ctx.toast(t("delegate.saved")); }
      reloadAll();
    } finally { setBusy(false); }
  };
  const openPreview = async (replyId: string): Promise<void> => {
    setBusy(true);
    try {
      const res = await getApi().delegateReply({ op: "preview", reply_id: replyId });
      if (!res.ok) { ctx.toast(res.error.message, true); return; }
      setPreview(res.data);
      repliesRes.reload();
    } finally { setBusy(false); }
  };
  const decideReply = async (approve: boolean): Promise<void> => {
    if (!preview) return;
    setBusy(true);
    try {
      const res = approve
        ? await getApi().delegateReply({ op: "approve", reply_id: preview.reply_id, body_sha256: preview.body_sha256 })
        : await getApi().delegateReply({ op: "reject", reply_id: preview.reply_id });
      if (!res.ok) { ctx.toast(res.error.message, true); return; }
      ctx.toast(t(approve ? "delegate.replyApproved" : "delegate.replyRejected"));
      setPreview(null);
      reloadAll();
    } finally { setBusy(false); }
  };
  return <div class="page" data-testid="page-delegate">
    <PageHead title={t("nav.delegate")} sub={t("delegate.sub")}><button type="button" class="btn" onClick={reloadAll}>{t("common.refresh")}</button></PageHead>
    <ResourceView resource={resource}>{(state: DelegateState) => <>
      <section class="card stack">
        <div><Badge kind={state.enabled ? "ok" : "warn"}><span data-testid="delegate-state">{state.enabled ? t("delegate.enabled") : t("delegate.disabled")}</span></Badge></div>
        <div><button type="button" class="btn" disabled={busy || !ctx.can("config.write")} onClick={() => void toggle(!state.enabled)} data-testid="delegate-toggle">{t(state.enabled ? "delegate.disable" : "delegate.enable")}</button></div>
        {!state.enabled && <p class="hint">{t("delegate.hint")}</p>}
        <p data-testid="delegate-limits">{t("delegate.limits", { running: state.running, max: state.max_concurrent })}</p>
        <p class="hint" data-testid="delegate-policy">
          {t("delegate.policyWrite", { state: state.allow_write ? t("delegate.allowed") : t("delegate.denied") })}
          {" · "}
          {t("delegate.policySessions", { state: state.allow_sessions ? t("delegate.allowed") : t("delegate.denied") })}
          {" · "}
          {t("delegate.policySessionReplies", { state: state.allow_session_replies ? t("delegate.allowed") : t("delegate.denied") })}
        </p>
        <p class="hint">{t("delegate.policyHint")}</p>
      </section>
      <section class="card stack"><h2>{t("delegate.replies")}</h2>
        <p class="hint">{t("delegate.repliesHint")}</p>
        <ResourceView resource={repliesRes.resource}>{(list: DelegateRepliesResponse) => <>
          {list.replies.length === 0 ? <Empty>{t("delegate.noReplies")}</Empty> : <div class="table-wrap"><table data-testid="delegate-replies"><thead><tr><th scope="col">{t("delegate.colReply")}</th><th scope="col">{t("delegate.colSession")}</th><th scope="col">{t("delegate.colCaller")}</th><th scope="col">{t("delegate.colState")}</th><th scope="col">{t("delegate.colBytes")}</th><th scope="col">{t("delegate.colExpires")}</th><th scope="col">{t("delegate.colAction")}</th></tr></thead>
            <tbody>{list.replies.map((reply) => <tr key={reply.reply_id}>
              <td class="mono">{reply.reply_id}</td>
              <td class="mono">{reply.session_id}</td>
              <td class="mono">{reply.caller}</td>
              <td data-testid={`delegate-reply-state-${reply.reply_id}`}>{replyStateLabel(reply.state, t)}</td>
              <td>{t("delegate.replyBytes", { bytes: reply.bytes })}</td>
              <td>{formatTime(reply.expires_at)}</td>
              <td>{reply.state === "pending" && <button type="button" class="btn" disabled={busy} onClick={() => void openPreview(reply.reply_id)} data-testid={`delegate-reply-preview-${reply.reply_id}`}>{t("delegate.replyPreview")}</button>}</td>
            </tr>)}</tbody>
          </table></div>}
          {preview && <div class="card stack" data-testid="delegate-reply-preview">
            <h3>{t("delegate.replyPreviewTitle")} · <span class="mono">{preview.reply_id}</span></h3>
            <p class="hint mono">{t("delegate.replyBytes", { bytes: preview.bytes })} · {t("delegate.replySha", { sha: preview.body_sha256 })}</p>
            <pre class="mono" data-testid="delegate-reply-preview-body" style={{ whiteSpace: "pre-wrap", wordBreak: "break-word" }}>{preview.body ?? ""}</pre>
            <div class="row">
              {preview.state === "pending" && <>
                <button type="button" class="btn primary" disabled={busy} onClick={() => void decideReply(true)} data-testid="delegate-reply-approve">{t("delegate.replyApprove")}</button>
                <button type="button" class="btn danger" disabled={busy} onClick={() => void decideReply(false)} data-testid="delegate-reply-reject">{t("delegate.replyReject")}</button>
              </>}
              <button type="button" class="btn" disabled={busy} onClick={() => setPreview(null)}>{t("delegate.replyClosePreview")}</button>
            </div>
          </div>}
        </>}</ResourceView>
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
        {state.tasks.length === 0 ? <Empty>{t("delegate.noTasks")}</Empty> : <div class="table-wrap"><table data-testid="delegate-tasks"><thead><tr><th scope="col">{t("delegate.colTask")}</th><th scope="col">{t("delegate.colCaller")}</th><th scope="col">{t("delegate.colRunner")}</th><th scope="col">{t("delegate.colState")}</th><th scope="col">{t("delegate.colCwd")}</th><th scope="col">{t("delegate.colStarted")}</th><th scope="col">{t("delegate.colDuration")}</th></tr></thead>
          <tbody>{state.tasks.map((task) => <tr key={task.task_id}><td class="mono">{task.task_id}</td><td class="mono">{task.caller}</td><td>{task.runner}</td><td data-testid={`delegate-task-state-${task.task_id}`}>{taskState(task.state, t)}</td><td class="mono">{task.cwd}</td><td>{formatTime(task.started_at)}</td><td>{formatDuration(task.duration_ms)}</td></tr>)}</tbody>
        </table></div>}
      </section>
    </>}</ResourceView>
  </div>;
}
