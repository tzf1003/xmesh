import type { JSX } from "preact";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Empty, NotSupported, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { useT } from "../i18n";
import { ago, formatIdle, formatTime } from "../lib/format";

export function Sessions({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const supported = ctx.can("sessions");
  const { resource, reload } = useResource(() => getApi().getSessions(), [ctx.snapshot.selectedInstance, supported], supported ? 3000 : undefined);
  if (!supported) {
    return (
      <div class="page">
        <PageHead title={t("nav.sessions")} />
        <NotSupported what={t("nav.sessions")} />
      </div>
    );
  }
  return (
    <div class="page" data-testid="page-sessions">
      <PageHead title={t("nav.sessions")} sub={t("sessions.sub")}>
        <button type="button" class="btn" onClick={reload}>
          {t("common.refresh")}
        </button>
      </PageHead>
      <ResourceView resource={resource}>
        {(s) => (
          <>
            {s.pty.some((p) => p.state === "orphaned") && <Banner kind="warn" testid="orphans">{t("sessions.orphans")}</Banner>}
            <section class="card">
              <h2>{t("sessions.pty")}</h2>
              {s.pty.length === 0 ? (
                <Empty>{t("sessions.noPty")}</Empty>
              ) : (
                <div class="table-wrap">
                  <table data-testid="pty-table">
                    <thead>
                      <tr>
                        <th scope="col">{t("col.name")}</th>
                        <th scope="col">{t("col.owner")}</th>
                        <th scope="col">{t("col.status")}</th>
                        <th scope="col">{t("col.cwd")}</th>
                        <th scope="col" class="num">{t("col.idle")}</th>
                        <th scope="col" class="num">{t("col.size")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {s.pty.map((p) => (
                        <tr key={p.id}>
                          <td>
                            {p.name ?? <span class="sub">{t("sessions.unnamed")}</span>} <span class="hint mono">{p.id}</span>
                          </td>
                          <td>{p.oauth_client ?? (p.owner || "—")}</td>
                          <td>
                            <Badge kind={p.state === "active" ? "ok" : p.state === "orphaned" ? "warn" : ""}>{t(`pty.${p.state}`)}</Badge>
                          </td>
                          <td class="mono">{p.cwd}</td>
                          <td class="num">{formatIdle(p.idle_s)}</td>
                          <td class="num">
                            {p.cols}×{p.rows}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>
            <section class="card">
              <h2>{t("sessions.exec")}</h2>
              {s.exec.length === 0 ? (
                <Empty>{t("sessions.noExec")}</Empty>
              ) : (
                <div class="table-wrap">
                  <table data-testid="exec-table">
                    <thead>
                      <tr>
                        <th scope="col">{t("col.command")}</th>
                        <th scope="col">{t("col.caller")}</th>
                        <th scope="col">{t("col.mode")}</th>
                        <th scope="col">{t("col.status")}</th>
                        <th scope="col">{t("col.started")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {s.exec.map((e) => {
                        const when = ago(e.started_at);
                        return (
                          <tr key={e.id}>
                            <td class="mono">{e.cmd}</td>
                            <td>{e.oauth_client ?? e.caller}</td>
                            <td>{e.mode}</td>
                            <td>
                              <Badge kind={e.state === "running" ? "ok" : "warn"}>{t(`exec.${e.state}`)}</Badge>
                            </td>
                            <td title={formatTime(e.started_at)}>{when ? t(`ago.${when.unit}`, { n: when.n }) : "—"}</td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </section>
            <section class="card">
              <h2>{t("sessions.leases")}</h2>
              {s.leases.length === 0 ? (
                <Empty>{t("sessions.noLeases")}</Empty>
              ) : (
                <div class="table-wrap">
                  <table data-testid="lease-table">
                    <thead>
                      <tr>
                        <th scope="col">{t("col.resource")}</th>
                        <th scope="col">{t("col.owner")}</th>
                        <th scope="col" class="num">{t("col.token")}</th>
                        <th scope="col">{t("col.expires")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {s.leases.map((l) => (
                        <tr key={l.resource}>
                          <td class="mono">{l.resource}</td>
                          <td>{l.owner}</td>
                          <td class="num">{l.token}</td>
                          <td>{formatTime(l.expires_at)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>
          </>
        )}
      </ResourceView>
    </div>
  );
}
