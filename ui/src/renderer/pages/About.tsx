import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, PageHead } from "../components";
import { errorText, useLocale, useT, type T } from "../i18n";
import { formatBytes, formatTime } from "../lib/format";
import type { UpdateError, UpdateState } from "../../shared/update";
import { sameVersion } from "../../shared/version";
import { KernelManager } from "./KernelManager";

/** The sentence for an update failure: the panel's own wording per code, with the detail the main process gave. */
export function updateErrorText(t: T, e: UpdateError): string {
  switch (e.code) {
    case "network":
    case "http":
    case "bad_checksum":
    case "install_failed":
      return t(`update.err.${e.code}`, { detail: e.message });
    case "not_writable":
    case "busy":
      return t(`update.err.${e.code}`);
    case "stop_failed":
      return e.message;
    default:
      return t("update.err.other", { detail: e.message });
  }
}

export function outcomeText(t: T, o: NonNullable<UpdateState["outcome"]>): string {
  return o.ok ? t("update.outcomeOk", { version: o.version }) : t("update.outcomeFailed", { version: o.version, reason: o.message ?? "?" });
}

export function About({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const locale = useLocale();
  const u = ctx.snapshot.update;
  const c = ctx.snapshot.connection;
  const [confirm, setConfirm] = useState(false);
  const [confirmRestart, setConfirmRestart] = useState(false);

  const kernel = c.kind === "connected" ? c.status.control.kernel_version : c.kind === "mismatch" ? c.kernelVersion : "";
  const stale = u.blocked !== "unpackaged" && u.kernelBundled && c.kind === "connected" && kernel !== "" && !sameVersion(kernel, u.current);
  const busy = u.phase === "checking" || u.phase === "downloading" || u.phase === "installing";
  const canCheck = u.blocked === "" || u.blocked === "translocated";

  const check = (): void => {
    void getApi().checkForUpdate();
  };
  const install = async (): Promise<void> => {
    setConfirm(false);
    const r = await getApi().installUpdate();
    if (!r.ok) ctx.toast(errorText(t, r.error), true);
  };
  const setPrefs = (patch: Partial<UpdateState["prefs"]>): void => {
    void getApi().setUpdatePrefs({ ...u.prefs, ...patch });
  };

  return (
    <div class="page" data-testid="page-about">
      <PageHead title={t("update.title")} sub={t("update.sub")} />

      <section class="card">
        <h2>{t("update.versionsTitle")}</h2>
        <dl class="kv" data-testid="versions">
          <dt>{t("update.panel")}</dt>
          <dd class="mono" data-testid="version-panel">{u.current || "—"}</dd>
          <dt>{t("update.kernel")}</dt>
          <dd class="mono" data-testid="version-kernel">{kernel || t("update.kernelOffline")}</dd>
        </dl>
        {stale && (
          <Banner kind="warn" testid="kernel-stale">
            <p>{t("update.kernelStale", { running: kernel })}</p>
            <div class="row">
              <button type="button" class="btn" onClick={() => setConfirmRestart(true)} data-testid="kernel-restart">
                {t("update.kernelRestart")}
              </button>
            </div>
          </Banner>
        )}
        {u.blocked !== "unpackaged" && !u.kernelBundled && (
          <p class="hint" data-testid="kernel-external">
            {t("update.kernelExternal")}
          </p>
        )}
      </section>

      <KernelManager ctx={ctx} />

      <section class="card" data-testid="update-card">
        <h2>{t("nav.about")}</h2>
        {u.blocked !== "" && (
          <Banner kind={u.blocked === "translocated" ? "warn" : ""} testid="update-blocked">
            {t(`update.blocked.${u.blocked}`)}
          </Banner>
        )}
        {u.outcome && (
          <Banner kind={u.outcome.ok ? "ok" : "danger"} testid="update-outcome">
            {outcomeText(t, u.outcome)}
          </Banner>
        )}
        {u.outcome?.unstarted && u.outcome.unstarted.length > 0 && (
          <Banner kind="warn" testid="update-unstarted">
            {t("update.outcomeUnstarted", { nodes: u.outcome.unstarted.join(", ") })}
          </Banner>
        )}
        {u.error && (
          <Banner kind="danger" testid="update-error">
            {updateErrorText(t, u.error)}
          </Banner>
        )}

        {u.phase === "checking" && <p data-testid="update-checking">{t("update.checking")}</p>}

        {(u.phase === "available" || u.phase === "downloading" || u.phase === "installing") && u.latest && (
          <div data-testid="update-available">
            <p>
              <strong>{t("update.available", { version: u.latest.version })}</strong> {u.latest.prerelease && <Badge kind="info">{t("update.prereleaseTag")}</Badge>}
            </p>
            <p class="hint">{t("update.published", { time: formatTime(u.latest.publishedAt), size: formatBytes(u.latest.size) })}</p>
            {u.latest.notes !== "" && (
              <details>
                <summary>{t("update.notes")}</summary>
                <pre class="notes" lang={locale}>{u.latest.notes}</pre>
              </details>
            )}
          </div>
        )}

        {u.phase === "downloading" && (
          <div data-testid="update-progress">
            <p>
              {u.progress && u.progress.total > 0
                ? t("update.downloading", { version: u.latest?.version ?? "", received: formatBytes(u.progress.received), total: formatBytes(u.progress.total) })
                : t("update.downloadingUnknown", { version: u.latest?.version ?? "", received: formatBytes(u.progress?.received ?? 0) })}
            </p>
            {u.progress && u.progress.total > 0 && <progress class="download" max={u.progress.total} value={u.progress.received} />}
          </div>
        )}
        {u.phase === "installing" && <p data-testid="update-installing">{t("update.installing", { version: u.latest?.version ?? "" })}</p>}

        {u.phase === "idle" && u.checkedAt && u.blocked !== "unpackaged" && (
          <p data-testid="update-uptodate">{t("update.upToDate", { version: u.current })}</p>
        )}
        {u.phase === "idle" && !u.checkedAt && canCheck && <p class="hint">{t("update.neverChecked")}</p>}
        {u.checkedAt && <p class="hint">{t("update.checkedAt", { time: formatTime(u.checkedAt) })}</p>}

        <div class="row">
          {u.phase === "available" && u.blocked === "" && (
            <button type="button" class="btn primary" onClick={() => setConfirm(true)} data-testid="update-install">
              {t("update.install")}
            </button>
          )}
          {u.phase === "downloading" && (
            <button type="button" class="btn" onClick={() => void getApi().cancelUpdate()} data-testid="update-cancel">
              {t("update.cancel")}
            </button>
          )}
          <button type="button" class="btn" disabled={busy || !canCheck} onClick={check} data-testid="update-check">
            {t("update.check")}
          </button>
          <button type="button" class="btn" onClick={() => void getApi().openExternal("releases")}>
            {t("update.releases")}
          </button>
        </div>
      </section>

      <section class="card">
        <h2>{t("update.prefsTitle")}</h2>
        <label class="check">
          <input type="checkbox" checked={u.prefs.auto} onChange={(e) => setPrefs({ auto: e.currentTarget.checked })} data-testid="pref-auto" /> {t("update.auto")}
        </label>
        <label class="check">
          <input type="checkbox" checked={u.prefs.prerelease} onChange={(e) => setPrefs({ prerelease: e.currentTarget.checked })} data-testid="pref-prerelease" /> {t("update.prerelease")}
        </label>
        <p class="hint">{t("update.prereleaseHint")}</p>
      </section>

      {confirm && u.latest && (
        <Confirm
          title={t("update.confirmTitle", { version: u.latest.version })}
          body={<p>{t("update.confirmBody")}</p>}
          confirmLabel={t("update.confirmOk")}
          onCancel={() => setConfirm(false)}
          onConfirm={() => void install()}
        />
      )}
      {confirmRestart && (
        <Confirm
          title={t("update.kernelRestartTitle")}
          body={<p>{t("update.kernelRestartBody")}</p>}
          confirmLabel={t("update.kernelRestart")}
          onCancel={() => setConfirmRestart(false)}
          onConfirm={() => {
            setConfirmRestart(false);
            void getApi().restartKernel().then((r) => {
              if (!r.ok) ctx.toast(errorText(t, r.error), true);
            });
          }}
        />
      )}
    </div>
  );
}
