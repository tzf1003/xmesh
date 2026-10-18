import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { getApi } from "./api";
import { Banner, Confirm } from "./components";
import { errorText, useT } from "./i18n";
import { applyView, hasNews } from "./lib/apply-state";
import type { SaveOutcome } from "./lib/reload";
import type { StatusResponse } from "../shared/control";

/**
 * Whether the kernel has taken the configuration, on every page. A save puts "applied" here; a setting that only takes
 * effect after a restart, or that the running kernel could not take, stays here for as long as the kernel says so, with the
 * restart button beside it. The pages only report what their save returned (`PageContext.applied`).
 */
export function ApplyBar({
  fresh,
  status,
  writable,
  onClear,
  toast,
}: {
  fresh: SaveOutcome | undefined;
  status: StatusResponse | undefined;
  writable: boolean;
  onClear: () => void;
  toast: (text: string, error?: boolean) => void;
}): JSX.Element | null {
  const t = useT();
  const [ask, setAsk] = useState(false);
  const [busy, setBusy] = useState(false);
  const view = applyView(fresh, status);
  if (!hasNews(view)) return null;

  const restart = async (): Promise<void> => {
    setBusy(true);
    const r = await getApi().restartKernel();
    setBusy(false);
    setAsk(false);
    if (r.ok) {
      onClear();
      toast(t("settings.restarting"));
    } else toast(errorText(t, r.error), true);
  };

  return (
    <div class="apply-bar" data-testid="apply-status" data-state={view.restart ? "restart" : view.problems.length > 0 || view.loadError !== undefined ? "failed" : "applied"}>
      {view.loadError !== undefined && (
        <Banner kind="danger" testid="apply-load-error">
          <strong>{t("apply.loadError")}</strong> <span class="hint">{view.loadError}</span>
        </Banner>
      )}
      {view.problems.length > 0 && (
        <Banner kind="warn" testid="apply-failed">
          <strong>{t("apply.failed")}</strong>
          {view.problems.map((message) => (
            <p class="hint" data-testid="apply-error" key={message}>
              {message}
            </p>
          ))}
        </Banner>
      )}
      {view.restart && (
        <Banner kind="warn" testid="apply-restart-needed">
          <div class="row">
            <strong>{t("apply.restart")}</strong>
            <span class="grow" />
            <button type="button" class="btn small primary" onClick={() => setAsk(true)} disabled={!writable} data-testid="apply-restart">
              {t("settings.restart")}
            </button>
          </div>
          {view.waiting.length > 0 && (
            <p class="hint" data-testid="apply-waiting">
              {t("apply.waiting", { names: view.waiting.join("、") })}
            </p>
          )}
        </Banner>
      )}
      {view.applied && (
        <Banner kind="ok" testid="apply-applied">
          <div class="row">
            <strong>{t("apply.applied")}</strong>
            <span class="hint">{t("apply.appliedNote")}</span>
            <span class="grow" />
            <button type="button" class="btn small" onClick={onClear} data-testid="apply-dismiss">
              {t("apply.dismiss")}
            </button>
          </div>
        </Banner>
      )}
      {ask && (
        <Confirm
          title={t("settings.restartTitle")}
          body={<p>{t("settings.restartBody")}</p>}
          confirmLabel={t("settings.restart")}
          busy={busy}
          onCancel={() => setAsk(false)}
          onConfirm={() => void restart()}
        />
      )}
    </div>
  );
}
