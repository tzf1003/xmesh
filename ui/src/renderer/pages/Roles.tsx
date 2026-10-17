import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, ErrorBanner, Field, NotSupported, PageHead } from "../components";
import { errorText, useT } from "../i18n";
import { diffRows, needsRestart, weakening } from "../lib/config";
import {
  LISTEN_MODES,
  MODES,
  ROLES,
  TLS_MODES,
  applyRolesForm,
  checkRolesForm,
  formFromConfig,
  isChanged,
  toggleRole,
  type ListenMode,
  type Mode,
  type Role,
  type RolesForm,
  type RolesProblem,
  type TLSMode,
} from "../lib/roles";
import type { ActionError } from "../../shared/ipc";
import type { ConfigChange, ConfigResponse } from "../../shared/control";
import type { Key } from "../messages";

const ROLE_HINT: Record<Role, Key> = { agent: "wizard.agentHint", gateway: "wizard.gatewayHint", ingress: "roles.ingressHint" };
const MODE_LABEL: Record<Mode, Key> = { lan: "mode.lan", tailnet: "mode.tailnet" };
const LISTEN_LABEL: Record<ListenMode, Key> = { tunnel: "roles.listen.tunnel", "public:443": "roles.listen.public443", funnel: "roles.listen.funnel" };
const TLS_LABEL: Record<TLSMode, Key> = { autocert: "roles.tls.autocert", file: "roles.tls.file" };
const PROBLEM: Record<RolesProblem, Key> = {
  roles: "roles.problem.roles",
  tailnet: "roles.problem.tailnet",
  control_url: "roles.problem.control_url",
  authkey_file: "roles.problem.authkey_file",
  issuer: "roles.problem.issuer",
  domain: "roles.problem.domain",
  cert_files: "roles.problem.cert_files",
};

/** A saved dry run: the kernel's diff for `content`, which is what Save writes. */
interface Review {
  changes: ConfigChange[];
  content: string;
}

export function Roles({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const supported = ctx.can("config.read");
  const writable = ctx.can("config.write");
  const [loaded, setLoaded] = useState<ConfigResponse | undefined>();
  const [form, setForm] = useState<RolesForm | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();
  const [review, setReview] = useState<Review | undefined>();
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<{ restart: boolean } | undefined>();
  const [askRestart, setAskRestart] = useState(false);
  const ingressSection = useRef<HTMLElement>(null);

  const load = async (): Promise<void> => {
    const r = await getApi().getConfig();
    if (!r.ok) return setErr(r.error);
    setErr(undefined);
    setLoaded(r.data);
    const current = formFromConfig(r.data.parsed);
    // A link from the ingress page opens with the ingress role on in the form; nothing is saved until the user does.
    setForm(ctx.focus === "ingress" ? { ...current, roles: toggleRole(current.roles, "ingress", true) } : current);
    setReview(undefined);
  };
  useEffect(() => {
    if (supported) void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ctx.snapshot.selectedInstance, supported]);
  useEffect(() => {
    if (loaded && ctx.focus === "ingress") ingressSection.current?.scrollIntoView?.({ block: "start" });
  }, [loaded, ctx.focus]);

  if (!supported) {
    return (
      <div class="page">
        <PageHead title={t("nav.roles")} />
        <NotSupported what={t("nav.roles")} />
      </div>
    );
  }

  const update = (patch: Partial<RolesForm>): void => {
    setForm((f) => (f ? { ...f, ...patch } : f));
    setReview(undefined);
    setSaved(undefined);
  };
  const problem = form ? checkRolesForm(form) : undefined;
  const changed = loaded !== undefined && form !== undefined && isChanged(formFromConfig(loaded.parsed), form);
  const content = loaded && form && problem === undefined ? applyRolesForm(loaded.content, form) : undefined;
  const ingressOn = form?.roles.includes("ingress") ?? false;

  const prepare = async (): Promise<void> => {
    if (!loaded || content === undefined) return;
    setBusy(true);
    const r = await getApi().putConfig({ content, etag: loaded.etag, dryRun: true });
    setBusy(false);
    if (!r.ok) return setErr(r.error);
    setErr(undefined);
    setReview({ changes: r.data.diff, content });
  };

  const save = async (): Promise<void> => {
    if (!loaded || !review) return;
    setBusy(true);
    const r = await getApi().putConfig({ content: review.content, etag: loaded.etag, dryRun: false });
    setBusy(false);
    setReview(undefined);
    if (!r.ok) return setErr(r.error);
    setSaved({ restart: r.data.restart_required });
    ctx.toast(t("config.saved"));
    await load();
  };

  const restart = async (): Promise<void> => {
    setBusy(true);
    const r = await getApi().restartKernel();
    setBusy(false);
    setAskRestart(false);
    if (r.ok) ctx.toast(t("roles.restarting"));
    else ctx.toast(errorText(t, r.error), true);
  };

  const pickAuthkey = async (): Promise<void> => {
    const r = await getApi().pickPath("authkeyFile");
    if (r.ok && r.data.path) update({ authkeyFile: r.data.path });
  };

  const weak = review ? weakening(review.changes) : [];
  const rows = review ? diffRows(review.changes) : [];

  return (
    <div class="page" data-testid="page-roles">
      <PageHead title={t("nav.roles")} sub={t("roles.sub")} />
      {loaded && !loaded.valid && (
        <Banner kind="danger" testid="roles-invalid-disk">
          {t("roles.invalidDisk")}
        </Banner>
      )}
      {saved && (
        <Banner kind={saved.restart ? "warn" : "ok"} testid="roles-saved">
          <div class="row">
            <span>{saved.restart ? t("roles.savedRestart") : t("roles.savedLive")}</span>
            {saved.restart && (
              <button type="button" class="btn small" onClick={() => setAskRestart(true)} disabled={!writable} data-testid="roles-restart">
                {t("roles.restart")}
              </button>
            )}
          </div>
        </Banner>
      )}
      {err && <ErrorBanner error={err} />}
      {err?.diagnostics && err.diagnostics.length > 0 && (
        <Banner kind="danger" testid="roles-diagnostics">
          <ul>
            {err.diagnostics.map((d, i) => (
              <li key={i}>
                {d.pointer && <code>{d.pointer}</code>} {d.message}
              </li>
            ))}
          </ul>
        </Banner>
      )}
      {err?.code === "precondition_failed" && (
        <div class="row">
          <button type="button" class="btn" onClick={() => void load()} data-testid="roles-reload-latest">
            {t("config.loadLatest")}
          </button>
        </div>
      )}

      {form && loaded?.valid && (
        <section class="card" data-testid="roles-form">
          {!writable && <Banner kind="warn" testid="roles-readonly">{t("config.readOnly")}</Banner>}
          <fieldset disabled={!writable} class="roles-fields">
            <fieldset>
              <legend>{t("roles.mode")}</legend>
              {MODES.map((m) => (
                <label class="check" key={m}>
                  <input type="radio" name="roles-mode" checked={form.mode === m} onChange={() => update({ mode: m })} data-testid={`roles-mode-${m}`} />
                  <span>
                    <strong>{t(MODE_LABEL[m])}</strong> <span class="hint">{t(m === "lan" ? "wizard.lanHint" : "wizard.tailnetHint")}</span>
                  </span>
                </label>
              ))}
            </fieldset>
            {form.mode === "tailnet" && (
              <>
                <Field label={t("wizard.controlUrl")} hint={t("wizard.controlUrlHint")}>
                  <input
                    type="text"
                    value={form.controlUrl}
                    onInput={(e) => update({ controlUrl: (e.currentTarget as HTMLInputElement).value })}
                    aria-invalid={problem === "control_url"}
                    data-testid="roles-control-url"
                  />
                </Field>
                <Field label={t("wizard.authkey")} hint={t("wizard.authkeyHint")}>
                  <div class="row">
                    <input
                      type="text"
                      value={form.authkeyFile}
                      onInput={(e) => update({ authkeyFile: (e.currentTarget as HTMLInputElement).value })}
                      aria-invalid={problem === "authkey_file"}
                      data-testid="roles-authkey"
                    />
                    <button type="button" class="btn" onClick={() => void pickAuthkey()}>
                      {t("wizard.choose")}
                    </button>
                  </div>
                </Field>
              </>
            )}

            <fieldset>
              <legend>{t("wizard.roles")}</legend>
              {ROLES.map((role) => (
                <label class="check" key={role}>
                  <input
                    type="checkbox"
                    checked={form.roles.includes(role)}
                    onChange={(e) => update({ roles: toggleRole(form.roles, role, (e.currentTarget as HTMLInputElement).checked) })}
                    data-testid={`roles-${role}`}
                  />
                  <span>
                    <strong>{role}</strong> <span class="hint">{t(ROLE_HINT[role])}</span>
                  </span>
                </label>
              ))}
            </fieldset>

            {ingressOn && (
              <section class="roles-ingress" ref={ingressSection} data-testid="roles-ingress">
                <h2>{t("roles.ingressTitle")}</h2>
                <p class="hint">{t("roles.ingressSub")}</p>
                {form.mode === "lan" && (
                  <Banner kind="warn" testid="roles-needs-tailnet">
                    {t("roles.needsTailnet")}
                  </Banner>
                )}
                <Field label={t("roles.listen")}>
                  <select value={form.listen} onChange={(e) => update({ listen: (e.currentTarget as HTMLSelectElement).value as ListenMode })} data-testid="roles-listen">
                    {LISTEN_MODES.map((m) => (
                      <option key={m} value={m}>
                        {t(LISTEN_LABEL[m])}
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label={t("roles.issuer")} hint={t("roles.issuerHint")}>
                  <input
                    type="text"
                    value={form.issuer}
                    placeholder="https://"
                    onInput={(e) => update({ issuer: (e.currentTarget as HTMLInputElement).value })}
                    aria-invalid={problem === "issuer"}
                    data-testid="roles-issuer"
                  />
                </Field>
                {form.listen === "public:443" && (
                  <>
                    <Field label={t("roles.domain")} hint={t("roles.domainHint")}>
                      <input
                        type="text"
                        value={form.domain}
                        onInput={(e) => update({ domain: (e.currentTarget as HTMLInputElement).value })}
                        aria-invalid={problem === "domain"}
                        data-testid="roles-domain"
                      />
                    </Field>
                    <fieldset>
                      <legend>{t("roles.tls")}</legend>
                      {TLS_MODES.map((m) => (
                        <label class="check" key={m}>
                          <input type="radio" name="roles-tls" checked={form.tls === m} onChange={() => update({ tls: m })} data-testid={`roles-tls-${m}`} />
                          <span>{t(TLS_LABEL[m])}</span>
                        </label>
                      ))}
                    </fieldset>
                    {form.tls === "file" && (
                      <>
                        <Field label={t("roles.certFile")}>
                          <input
                            type="text"
                            value={form.certFile}
                            onInput={(e) => update({ certFile: (e.currentTarget as HTMLInputElement).value })}
                            aria-invalid={problem === "cert_files"}
                            data-testid="roles-cert"
                          />
                        </Field>
                        <Field label={t("roles.keyFile")}>
                          <input
                            type="text"
                            value={form.keyFile}
                            onInput={(e) => update({ keyFile: (e.currentTarget as HTMLInputElement).value })}
                            aria-invalid={problem === "cert_files"}
                            data-testid="roles-key"
                          />
                        </Field>
                      </>
                    )}
                  </>
                )}
              </section>
            )}
          </fieldset>

          {problem && changed && (
            <p class="hint" data-testid="roles-problem">
              {t(PROBLEM[problem])}
            </p>
          )}
          {!changed && <p class="hint" data-testid="roles-unchanged">{t("roles.noChanges")}</p>}
          <div class="row end" style={{ marginTop: "10px" }}>
            {changed && (
              <button type="button" class="btn" disabled={busy} onClick={() => setForm(formFromConfig(loaded.parsed))}>
                {t("config.revert")}
              </button>
            )}
            <button
              type="button"
              class="btn primary"
              disabled={!writable || !changed || content === undefined || busy}
              onClick={() => void prepare()}
              data-testid="roles-review"
            >
              {t("roles.review")}
            </button>
          </div>
        </section>
      )}

      {review && (
        <Confirm
          title={t("roles.reviewTitle")}
          confirmLabel={t("config.save")}
          danger={weak.length > 0}
          busy={busy}
          onCancel={() => setReview(undefined)}
          onConfirm={() => void save()}
          body={
            <div data-testid="roles-diff">
              {weak.length > 0 && (
                <Banner kind="danger" testid="roles-weakens">
                  <p>{t("config.weakens")}</p>
                  <p class="hint">{t("config.weakensNote")}</p>
                </Banner>
              )}
              {needsRestart(review.changes) && <Banner kind="warn" testid="roles-needs-restart">{t("config.needsRestart")}</Banner>}
              {rows.length === 0 && <Banner kind="ok">{t("config.commentsOnly")}</Banner>}
              {rows.length > 0 && (
                <div class="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th scope="col">{t("config.path")}</th>
                        <th scope="col">{t("config.before")}</th>
                        <th scope="col">{t("config.after")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((r, i) => (
                        <tr key={i}>
                          <td class="mono">
                            <span class={`diff-${r.op}`}>{r.op === "add" ? "+" : r.op === "remove" ? "−" : "~"}</span> {r.pointer}{" "}
                            {r.weakens && <Badge kind="danger">{t("config.weakensTag")}</Badge>} {r.restartRequired && <Badge kind="warn">{t("config.restartTag")}</Badge>}
                          </td>
                          <td class="mono">{r.before}</td>
                          <td class="mono">{r.after}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          }
        />
      )}

      {askRestart && (
        <Confirm
          title={t("roles.restartTitle")}
          body={<p>{t("roles.restartBody")}</p>}
          confirmLabel={t("roles.restart")}
          busy={busy}
          onCancel={() => setAskRestart(false)}
          onConfirm={() => void restart()}
        />
      )}
    </div>
  );
}
