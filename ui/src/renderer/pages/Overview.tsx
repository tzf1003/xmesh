import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Empty, Meter, PageHead } from "../components";
import { errorText, useLocale, useT } from "../i18n";
import { useRecentCalls } from "../hooks";
import { formatDuration, formatTime } from "../lib/format";
import { isDegrading } from "../../shared/control";
import { CODEX_PAT_ENV, type ActionResult, type CopyKind, type PathKind } from "../../shared/ipc";

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
  const copy = (kind: CopyKind): Promise<void> => run(getApi().copy(kind), t("overview.copied"));
  const open = (kind: PathKind): Promise<void> => run(getApi().openPath(kind));

  return (
    <div class="page" data-testid="page-overview">
      <PageHead title={t("nav.overview")} sub={`${s.hostname} · ${s.os}/${s.arch} · xmesh ${s.control.kernel_version}`}>
        <div class="row">
          {s.roles.map((r) => (
            <Badge key={r} kind="info">
              {r}
            </Badge>
          ))}
          <Badge>{t(`mode.${s.network.mode}`)}</Badge>
        </div>
      </PageHead>

      {s.restart_required && <Banner kind="warn" testid="restart-required">{t("overview.restartRequired")}</Banner>}
      {draining && (
        <Banner kind="warn" testid="draining">
          {t(s.drain.mode === "kill" ? "overview.drainingKill" : "overview.drainingKeep", { since: formatTime(s.drain.since) })}
        </Banner>
      )}
      {s.warnings.filter((w) => w.code !== "restart_required").length > 0 && (
        <Banner kind={s.warnings.some((w) => w.severity === "error") ? "danger" : "warn"} testid="warnings">
          <strong>{t("overview.warnings")}</strong>
          <ul>
            {s.warnings
              .filter((w) => w.code !== "restart_required")
              .map((w, i) => (
                <li key={`${w.code}${i}`}>
                  {isDegrading(w) ? "" : "ℹ "}
                  {w.message}
                </li>
              ))}
          </ul>
        </Banner>
      )}

      <section class="card-grid" aria-label={t("overview.quota")}>
        <div class="card">
          <Meter used={s.limits.pty_in_use} max={s.limits.pty_max_sessions} label={t("overview.pty")} />
        </div>
        <div class="card">
          <Meter used={s.limits.exec_shared_in_use} max={s.limits.exec_shared} label={t("overview.execShared")} />
        </div>
        <div class="card">
          <Meter used={s.limits.exec_exclusive_in_use} max={s.limits.exec_exclusive} label={t("overview.execExclusive")} />
        </div>
        <div class="card stat">
          <h3>{t("overview.peers")}</h3>
          <div class="value" data-testid="peers">
            {s.peers.online} / {s.peers.total}
          </div>
          <span class="hint">{t("overview.peersHint")}</span>
        </div>
      </section>

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

      {s.gateway && (
        <section class="card">
          <h2>{t("overview.gateway")}</h2>
          <p>
            <code data-testid="gateway-url">{s.gateway.url}</code> <Badge>{s.gateway.auth === "pat" ? t("overview.authPat") : t("overview.authNone")}</Badge>
          </p>
          <div class="row">
            <button type="button" class="btn" onClick={() => void copy("gatewayUrl")}>
              {t("overview.copyUrl")}
            </button>
            <button type="button" class="btn" onClick={() => void copy("claudeCodeCommand")}>
              {t("overview.copyClaude")}
            </button>
            <button type="button" class="btn" onClick={() => void copy("codexConfig")}>
              {t("overview.copyCodex")}
            </button>
          </div>
          {s.gateway.auth === "pat" && (
            <p class="hint" style={{ marginTop: "8px" }} data-testid="copy-pat-hint">
              {t("overview.copyPatHint", { env: CODEX_PAT_ENV })}
            </p>
          )}
        </section>
      )}

      <section class="card">
        <h2>{t("overview.files")}</h2>
        <div class="row">
          <button type="button" class="btn" onClick={() => void open("config")}>{t("overview.openConfig")}</button>
          <button type="button" class="btn" onClick={() => void open("logs")} disabled={s.paths.log_dir === null}>{t("overview.openLogs")}</button>
          <button type="button" class="btn" onClick={() => void open("audit")} disabled={s.paths.audit_dir === null}>{t("overview.openAudit")}</button>
        </div>
      </section>

      <section class="card">
        <h2>{t("overview.recent")}</h2>
        <p class="hint">{t("overview.recentHint")}</p>
        <RecentCalls calls={calls} />
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

function RecentCalls({ calls }: { calls: ReturnType<typeof useRecentCalls> }): JSX.Element {
  const t = useT();
  const locale = useLocale();
  if (calls.length === 0) return <Empty>{t("overview.noCalls")}</Empty>;
  const rows = calls.slice(-10).reverse();
  return (
    <div class="table-wrap">
      <table data-testid="recent-calls" lang={locale}>
        <thead>
          <tr>
            <th scope="col">{t("col.tool")}</th>
            <th scope="col">{t("col.device")}</th>
            <th scope="col">{t("col.caller")}</th>
            <th scope="col">{t("col.status")}</th>
            <th scope="col" class="num">{t("col.duration")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((c, i) => (
            <tr key={i}>
              <td class="mono">{c.tool}</td>
              <td>{c.device}</td>
              <td>{c.oauth_client ?? c.caller}</td>
              <td>{c.status === "ok" ? <Badge kind="ok">ok</Badge> : <Badge kind="danger">{c.error ?? "error"}</Badge>}</td>
              <td class="num">{formatDuration(c.duration_ms)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
