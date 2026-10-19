import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Banner, Confirm, PageHead } from "../components";
import { errorText, useT } from "../i18n";
import { useRecentCalls } from "../hooks";
import { formatTime } from "../lib/format";
import { isDegrading } from "../../shared/control";
import type { ActionResult, PathKind } from "../../shared/ipc";
import { GatewayCard } from "./gateway/GatewayCard";
import { Topology } from "./Topology";
import { Icon } from "../icons";

/** The restart and the settings the kernel could not apply are the bar's (see ApplyBar); the rest is listed here. */
const listed = (w: { code: string }): boolean => w.code !== "restart_required" && w.code !== "policy_reload_failed";

export function Overview({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  const c = ctx.snapshot.connection;
  const calls = useRecentCalls();
  const [confirmKill, setConfirmKill] = useState(false);
  const [busy, setBusy] = useState(false);
  if (c.kind !== "connected") return null;
  const s = c.status;
  const draining = s.drain.draining;

  const run = async (p: Promise<ActionResult>, ok?: string): Promise<void> => {
    setBusy(true);
    const r = await p;
    setBusy(false);
    if (!r.ok) ctx.toast(errorText(t, r.error), true);
    else if (ok) ctx.toast(ok);
  };
  const open = (kind: PathKind): Promise<void> => run(getApi().openPath(kind));

  return (
    <div class="page" data-testid="page-overview">
      <PageHead title={t("topology.title")} sub={t("topology.sub")}>
        <div class="row">
          <button type="button" class="btn" data-testid="topology-copy-client" onClick={() => { if (s.gateway) void run(getApi().copyGateway({ kind: "codexConfig", url: s.gateway.url }), t("overview.copied")); }} disabled={busy || !s.gateway}><Icon name="copy"/>{t("overview.copyCodex")}</button>
          <button type="button" class="btn primary" onClick={() => s.network.mode === "tailnet" ? ctx.go("mesh", { meshTab: "onboard" }) : ctx.go("devices")}><Icon name="plus"/>{t("topology.addDevice")}</button>
        </div>
      </PageHead>

      {draining && (
        <Banner kind="warn" testid="draining">
          {t(s.drain.mode === "kill" ? "overview.drainingKill" : "overview.drainingKeep", { since: formatTime(s.drain.since) })}
        </Banner>
      )}
      {s.warnings.filter(listed).length > 0 && (
        <Banner kind={s.warnings.some((w) => w.severity === "error") ? "danger" : "warn"} testid="warnings">
          <strong>{t("overview.warnings")}</strong>
          <ul>
            {s.warnings
              .filter(listed)
              .map((w, i) => (
                <li key={`${w.code}${i}`}>
                  {isDegrading(w) ? "" : "ℹ "}
                  {w.message}
                </li>
              ))}
          </ul>
        </Banner>
      )}

      <Topology ctx={ctx} calls={calls}/>
      <details class="overview-advanced" id="overview-advanced" data-testid="overview-advanced">
      <summary><Icon name="sliders"/>{t("topology.operations")}<Icon name="chevron"/></summary>
      <div class="stack">
      <section class="card">
        <h2>{t("overview.control")}</h2>
        <div class="row">
          {!draining && (
            <button type="button" class="btn" disabled={busy || !ctx.can("drain")} onClick={() => void run(getApi().drain("keep"))}>
              {t("overview.drain")}
            </button>
          )}
          {!draining || s.drain.mode === "keep" ? (
            <button type="button" class="btn danger" disabled={busy || !ctx.can("drain")} onClick={() => setConfirmKill(true)} data-testid="drain-kill">
              {t("overview.drainKill")}
            </button>
          ) : null}
          {draining && (
            <button type="button" class="btn primary" disabled={busy || !ctx.can("drain")} onClick={() => void run(getApi().resume())} data-testid="resume">
              {t("overview.resume")}
            </button>
          )}
        </div>
        <p class="hint" style={{ marginTop: "8px" }}>
          {t("overview.drainHint")}
        </p>
      </section>

      {s.gateway && <div id="gateway-panel"><GatewayCard ctx={ctx} gateway={s.gateway} /></div>}

      <section class="card">
        <h2>{t("overview.files")}</h2>
        <div class="row">
          <button type="button" class="btn" onClick={() => void open("config")}>{t("overview.openConfig")}</button>
          <button type="button" class="btn" onClick={() => void open("logs")} disabled={s.paths.log_dir === null}>{t("overview.openLogs")}</button>
          <button type="button" class="btn" onClick={() => void open("audit")} disabled={s.paths.audit_dir === null}>{t("overview.openAudit")}</button>
        </div>
      </section>

      </div>
      </details>

      {confirmKill && (
        <Confirm
          title={t("overview.killTitle")}
          body={<p>{t("overview.killBody", { pty: s.limits.pty_in_use })}</p>}
          confirmLabel={t("overview.killConfirm")}
          danger
          busy={busy}
          onCancel={() => setConfirmKill(false)}
          onConfirm={() => {
            setConfirmKill(false);
            void run(getApi().drain("kill"));
          }}
        />
      )}
    </div>
  );
}
