import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { ComputerRequest } from "../../shared/computer";
import type { ComputerState } from "../../shared/control";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Empty, NotSupported, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { useT, type T } from "../i18n";
import { formatTime } from "../lib/format";
import { applySaved } from "../lib/reload";

function status(state: ComputerState, t: T): string {
  if (!state.enabled) return t("computer.disabled");
  if (state.paused) return t("computer.paused");
  if (state.info.available) return t("computer.ready");
  if (state.info.state === "permission_required") return t("computer.permissionRequired");
  if (state.info.state === "locked") return t("computer.locked");
  return t("computer.unavailable");
}

export function Computer({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  if (!ctx.can("computer")) return <div class="page"><PageHead title={t("nav.computer")} /><NotSupported what={t("nav.computer")} /></div>;
  return <ComputerPage ctx={ctx} />;
}

function ComputerPage({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const { resource, reload } = useResource(() => getApi().computer({ op: "get" }), [ctx.snapshot.selectedInstance], 2000);
  const [busy, setBusy] = useState(false);
  const run = async (request: ComputerRequest): Promise<void> => {
    setBusy(true);
    try {
      const result = await getApi().computer(request);
      if (!result.ok) ctx.toast(result.error.message, true);
      else ctx.toast(t("computer.actionDone"));
      reload();
    } finally { setBusy(false); }
  };
  const toggle = async (enabled: boolean): Promise<void> => {
    setBusy(true);
    try {
      const loaded = await getApi().getConfig();
      if (!loaded.ok) { ctx.toast(loaded.error.message, true); return; }
      const saved = await getApi().patchConfig({ edits: [{ path: "/agent/computer/enabled", value: enabled }], etag: loaded.data.etag, dryRun: false });
      if (!saved.ok) ctx.toast(saved.error.message, true);
      else { ctx.applied(await applySaved(saved.data.restart_required)); ctx.toast(t("computer.saved")); }
      reload();
    } finally { setBusy(false); }
  };
  return <div class="page" data-testid="page-computer">
    <PageHead title={t("nav.computer")} sub={t("computer.sub")}><button type="button" class="btn" onClick={reload}>{t("common.refresh")}</button></PageHead>
    <ResourceView resource={resource}>{(state) => <>
      <section class="card stack">
        <div><Badge kind={state.info.available && !state.paused ? "ok" : "warn"}><span data-testid="computer-state">{status(state, t)}</span></Badge></div>
        <div class="computer-actions">
          <button type="button" class="btn" disabled={busy || !ctx.can("config.write")} onClick={() => void toggle(!state.enabled)} data-testid="computer-toggle">{t(state.enabled ? "computer.disable" : "computer.enable")}</button>
          <button type="button" class="btn" disabled={busy || !state.enabled} onClick={() => void run({ op: "pause", paused: !state.paused })} data-testid="computer-pause">{t(state.paused ? "computer.resume" : "computer.pause")}</button>
          <button type="button" class="btn danger" disabled={busy || !state.enabled || state.paused} onClick={() => void run({ op: "pause", paused: true })} data-testid="computer-takeover">{t("computer.takeover")}</button>
        </div>
        <p class="hint">{t("computer.takeoverHint")}</p>
        <p class="hint">{t("computer.shortcut")}</p>
      </section>
      <section class="card stack">
        <h2>{t("computer.permissions")}</h2>
        <p>{t("computer.permissionsHint")}</p>
        <label>{t("computer.setup")}<pre class="mono" data-testid="computer-setup">xmesh computer host --authorize</pre></label>
        <p class="hint">{t("computer.scopeHint")}</p>
        <code class="mono">computer:observe:{ctx.snapshot.connection.kind === "connected" ? ctx.snapshot.connection.status.hostname : "device"}</code>
        <code class="mono">computer:control:{ctx.snapshot.connection.kind === "connected" ? ctx.snapshot.connection.status.hostname : "device"}</code>
        <Banner kind="warn">{t("computer.privilege")}</Banner>
        {state.info.displays.length > 0 && <div><h3>{t("computer.displays")}</h3><ul class="plain">{state.info.displays.map((d) => <li key={d.id}>{d.name} · {d.width}×{d.height} · {d.scale}×</li>)}</ul></div>}
      </section>
      <section class="card"><h2>{t("computer.sessions")}</h2>
        {state.sessions.length === 0 ? <Empty>{t("computer.noSessions")}</Empty> : <div class="table-wrap"><table data-testid="computer-sessions"><thead><tr><th scope="col">{t("col.owner")}</th><th scope="col">{t("col.status")}</th><th scope="col">{t("computer.expires")}</th><th scope="col">{t("computer.actions")}</th></tr></thead>
          <tbody>{state.sessions.map((session) => <tr key={session.session_id}><td class="mono">{session.owner}</td><td>{t(session.mode === "control" ? "computer.control" : "computer.observe")}</td><td>{formatTime(session.expires_at)}</td><td><button type="button" class="btn small" disabled={busy} onClick={() => void run({ op: "revoke", session_id: session.session_id })} data-testid={`computer-revoke-${session.session_id}`}>{t("computer.revoke")}</button></td></tr>)}</tbody>
        </table></div>}
      </section>
    </>}</ResourceView>
  </div>;
}
