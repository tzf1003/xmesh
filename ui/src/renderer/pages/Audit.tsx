import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Empty, ErrorBanner, Field, NotSupported, PageHead } from "../components";
import { useT } from "../i18n";
import { EMPTY_AUDIT_FORM, auditTarget, buildAuditQuery, type AuditForm } from "../lib/audit";
import { formatDuration, formatTime } from "../lib/format";
import { AUDIT_EVENTS, type ActionError } from "../../shared/ipc";
import type { AuditEvent } from "../../shared/control";

export function Audit({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const [form, setForm] = useState<AuditForm>(EMPTY_AUDIT_FORM);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [enabled, setEnabled] = useState(true);
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const supported = ctx.can("audit");

  const load = async (f: AuditForm, next?: string): Promise<void> => {
    setLoading(true);
    const r = await getApi().queryAudit(buildAuditQuery(f, 100, next));
    setLoading(false);
    setLoaded(true);
    if (!r.ok) return setErr(r.error);
    setErr(undefined);
    setEnabled(r.data.enabled);
    setEvents((cur) => (next ? [...cur, ...r.data.events] : r.data.events));
    setCursor(r.data.next_cursor);
  };

  useEffect(() => {
    if (supported) void load(EMPTY_AUDIT_FORM);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ctx.snapshot.selectedInstance, supported]);

  if (!supported) {
    return (
      <div class="page">
        <PageHead title={t("nav.audit")} />
        <NotSupported what={t("nav.audit")} />
      </div>
    );
  }
  const set = <K extends keyof AuditForm>(k: K, v: AuditForm[K]): void => setForm((f) => ({ ...f, [k]: v }));

  return (
    <div class="page" data-testid="page-audit">
      <PageHead title={t("nav.audit")} sub={t("audit.sub")} />
      <section class="card">
        <form
          class="row"
          onSubmit={(e) => {
            e.preventDefault();
            void load(form);
          }}
        >
          <Field label={t("audit.since")}>
            <input type="datetime-local" value={form.since} onInput={(e) => set("since", (e.currentTarget as HTMLInputElement).value)} />
          </Field>
          <Field label={t("audit.until")}>
            <input type="datetime-local" value={form.until} onInput={(e) => set("until", (e.currentTarget as HTMLInputElement).value)} />
          </Field>
          <Field label={t("audit.event")}>
            <select value={form.event} onChange={(e) => set("event", (e.currentTarget as HTMLSelectElement).value)} data-testid="audit-event">
              <option value="">{t("audit.any")}</option>
              {AUDIT_EVENTS.map((ev) => (
                <option key={ev} value={ev}>
                  {ev}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t("col.tool")}>
            <input type="text" value={form.tool} onInput={(e) => set("tool", (e.currentTarget as HTMLInputElement).value)} data-testid="audit-tool" />
          </Field>
          <Field label={t("col.device")}>
            <input type="text" value={form.device} onInput={(e) => set("device", (e.currentTarget as HTMLInputElement).value)} />
          </Field>
          <Field label={t("col.caller")}>
            <input type="text" value={form.caller} onInput={(e) => set("caller", (e.currentTarget as HTMLInputElement).value)} />
          </Field>
          <Field label={t("col.status")}>
            <select value={form.status} onChange={(e) => set("status", (e.currentTarget as HTMLSelectElement).value as AuditForm["status"])} data-testid="audit-status">
              <option value="">{t("audit.any")}</option>
              <option value="ok">ok</option>
              <option value="error">error</option>
            </select>
          </Field>
          <div class="row">
            <button type="submit" class="btn primary" disabled={loading} data-testid="audit-search">
              {t("audit.search")}
            </button>
            <button
              type="button"
              class="btn"
              onClick={() => {
                setForm(EMPTY_AUDIT_FORM);
                void load(EMPTY_AUDIT_FORM);
              }}
            >
              {t("audit.reset")}
            </button>
          </div>
        </form>
      </section>
      {err && <ErrorBanner error={err} />}
      {loaded && !enabled && <Banner testid="audit-disabled">{t("audit.disabled")}</Banner>}
      {enabled && (
        <section class="card">
          {loaded && events.length === 0 ? (
            <Empty>{t("audit.none")}</Empty>
          ) : (
            <div class="table-wrap">
              <table data-testid="audit-table">
                <thead>
                  <tr>
                    <th scope="col">{t("col.time")}</th>
                    <th scope="col">{t("audit.event")}</th>
                    <th scope="col">{t("col.tool")}</th>
                    <th scope="col">{t("col.device")}</th>
                    <th scope="col">{t("col.caller")}</th>
                    <th scope="col">{t("audit.target")}</th>
                    <th scope="col">{t("col.status")}</th>
                    <th scope="col" class="num">{t("col.duration")}</th>
                  </tr>
                </thead>
                <tbody>
                  {events.map((e, i) => (
                    <tr key={`${e.time}-${i}`}>
                      <td>{formatTime(e.time)}</td>
                      <td>
                        {e.event} {e.origin === "panel" && <Badge kind="info">{t("pg.origin")}</Badge>} {e.actor === "control" && <Badge kind="info">control</Badge>}
                      </td>
                      <td class="mono">{e.tool}</td>
                      <td>{e.device}</td>
                      <td>{e.oauth_client ?? e.caller}</td>
                      <td class="mono">{auditTarget(e)}</td>
                      <td>{e.status === "ok" ? <Badge kind="ok">ok</Badge> : <Badge kind="danger">{e.error ?? "error"}</Badge>}</td>
                      <td class="num">{formatDuration(e.duration_ms)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {cursor && (
            <div class="row end" style={{ marginTop: "8px" }}>
              <button type="button" class="btn" disabled={loading} onClick={() => void load(form, cursor)} data-testid="audit-more">
                {t("audit.more")}
              </button>
            </div>
          )}
          <p class="hint">{t("audit.metaOnly")}</p>
        </section>
      )}
    </div>
  );
}
