import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Confirm, PageHead } from "../components";
import { errorText, useT } from "../i18n";
import type { ActionResult, PathKind } from "../../shared/ipc";
import { GatewayCard } from "./gateway/GatewayCard";

export function Runtime({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  const c = ctx.snapshot.connection;
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
    <div class="page" data-testid="page-runtime">
      <PageHead title={t("topology.operations")} sub={t("topology.operationsHint")} />
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
