import type { JSX } from "preact";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Banner, PageHead } from "../components";
import { errorText, useT } from "../i18n";
import { useRecentCalls } from "../hooks";
import { formatTime } from "../lib/format";
import { isDegrading } from "../../shared/control";
import { Topology } from "./Topology";
import { Icon } from "../icons";

/** The restart and the settings the kernel could not apply are the bar's (see ApplyBar); the rest is listed here. */
const listed = (w: { code: string }): boolean => w.code !== "restart_required" && w.code !== "policy_reload_failed";

export function Overview({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  const c = ctx.snapshot.connection;
  const calls = useRecentCalls();
  if (c.kind !== "connected") return null;
  const s = c.status;
  const draining = s.drain.draining;

  return (
    <div class="page" data-testid="page-overview">
      <PageHead title={t("topology.title")} sub={t("topology.sub")}>
        <div class="row">
          <button type="button" class="btn" data-testid="topology-copy-client" onClick={() => { if (s.gateway) void getApi().copyGateway({ kind: "codexConfig", url: s.gateway.url }).then((r) => ctx.toast(r.ok ? t("overview.copied") : errorText(t, r.error), !r.ok)); }} disabled={!s.gateway}><Icon name="copy"/>{t("overview.copyCodex")}</button>
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
    </div>
  );
}
