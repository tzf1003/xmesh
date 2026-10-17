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
  applySettingsForm,
  checkDenyList,
  checkSettingsForm,
  denyListText,
  formFromConfig,
  isChanged,
  parseDenyList,
  toggleRole,
  type ListenMode,
  type Mode,
  type Role,
  type SettingsForm,
  type SettingsProblem,
  type TLSMode,
} from "../lib/settings";
import { DEFAULT_DENY, isAbsolutePath } from "../../shared/setup";
import type { ActionError, DenyFileResponse } from "../../shared/ipc";
import type { ConfigChange, ConfigResponse } from "../../shared/control";
import type { Key } from "../messages";

const ROLE_HINT: Record<Role, Key> = { agent: "wizard.agentHint", gateway: "wizard.gatewayHint", ingress: "settings.ingressHint" };
const MODE_LABEL: Record<Mode, Key> = { lan: "mode.lan", tailnet: "mode.tailnet" };
const LISTEN_LABEL: Record<ListenMode, Key> = { tunnel: "settings.listen.tunnel", "public:443": "settings.listen.public443", funnel: "settings.listen.funnel" };
const TLS_LABEL: Record<TLSMode, Key> = { autocert: "settings.tls.autocert", file: "settings.tls.file" };
const PROBLEM: Record<SettingsProblem, Key> = {
  node_name: "settings.problem.node_name",
  roles: "settings.problem.roles",
  tailnet: "settings.problem.tailnet",
  control_url: "settings.problem.control_url",
  authkey_file: "settings.problem.authkey_file",
  issuer: "settings.problem.issuer",
  domain: "settings.problem.domain",
  cert_files: "settings.problem.cert_files",
  default_cwd: "settings.problem.default_cwd",
  allow_dirs: "settings.problem.allow_dirs",
};

/** A saved dry run: the kernel's diff for `content`, which is what Save writes. */
interface Review {
  changes: ConfigChange[];
  content: string;
}

export function Settings({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const supported = ctx.can("config.read");
  const writable = ctx.can("config.write");
  const [loaded, setLoaded] = useState<ConfigResponse | undefined>();
  const [form, setForm] = useState<SettingsForm | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();
  const [review, setReview] = useState<Review | undefined>();
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<{ restart: boolean } | undefined>();
  const [askRestart, setAskRestart] = useState(false);

  const [denyFile, setDenyFile] = useState<DenyFileResponse | undefined>();
  const [denyEntries, setDenyEntries] = useState<string[]>([]);
  const [denyErr, setDenyErr] = useState<ActionError | undefined>();
  const [denyReview, setDenyReview] = useState(false);
  const [denySaved, setDenySaved] = useState(false);

  const load = async (): Promise<void> => {
    const r = await getApi().getConfig();
    if (!r.ok) return setErr(r.error);
    setErr(undefined);
    setLoaded(r.data);
    const current = formFromConfig(r.data.parsed);
    // A link to the ingress role opens with it on in the form; nothing is saved until the user does.
    setForm(ctx.focus?.role === "ingress" ? { ...current, roles: toggleRole(current.roles, "ingress", true) } : current);
    setReview(undefined);
  };

  const loadDeny = async (): Promise<void> => {
    const r = await getApi().readDenyFile();
    if (!r.ok) return setDenyErr(r.error);
    setDenyErr(undefined);
    setDenyFile(r.data);
    setDenyEntries(parseDenyList(r.data.text) ?? []);
  };

  useEffect(() => {
    if (!supported) return;
    void load();
    void loadDeny();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ctx.snapshot.selectedInstance, supported]);

  // The section a link named is scrolled into view once the page has its data.
  useEffect(() => {
    if (!loaded || !ctx.focus) return;
    document.getElementById(`settings-${ctx.focus.section}`)?.scrollIntoView?.({ block: "start" });
  }, [loaded, ctx.focus]);

  if (!supported) {
    return (
      <div class="page">
        <PageHead title={t("nav.settings")} />
        <NotSupported what={t("nav.settings")} />
      </div>
    );
  }

  const update = (patch: Partial<SettingsForm>): void => {
    setForm((f) => (f ? { ...f, ...patch } : f));
    setReview(undefined);
    setSaved(undefined);
  };
  const problem = form ? checkSettingsForm(form) : undefined;
  const changed = loaded !== undefined && form !== undefined && isChanged(formFromConfig(loaded.parsed), form);
  const content = loaded && form && problem === undefined ? applySettingsForm(loaded.content, form) : undefined;
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
    // Changes that need a restart offer one. Once the kernel can reload the file, this is where the reload is requested instead.
    setSaved({ restart: r.data.restart_required });
    ctx.toast(t("config.saved"));
    await load();
  };

  const saveDeny = async (): Promise<void> => {
    if (!denyFile) return;
    setBusy(true);
    const r = await getApi().writeDenyFile({ text: denyListText(denyEntries), etag: denyFile.etag });
    setBusy(false);
    setDenyReview(false);
    if (!r.ok) return setDenyErr(r.error);
    setDenySaved(true);
    setDenyErr(undefined);
    ctx.toast(t("settings.denySaved"));
    await loadDeny();
  };

  const restart = async (): Promise<void> => {
    setBusy(true);
    const r = await getApi().restartKernel();
    setBusy(false);
    setAskRestart(false);
    if (r.ok) ctx.toast(t("settings.restarting"));
    else ctx.toast(errorText(t, r.error), true);
  };

  const pick = async (kind: "workspace" | "authkeyFile"): Promise<string | undefined> => {
    const r = await getApi().pickPath(kind);
    return r.ok ? (r.data.path ?? undefined) : undefined;
  };

  const weak = review ? weakening(review.changes) : [];
  const rows = review ? diffRows(review.changes) : [];
  const denyProblem = denyEntries.length > 0 ? checkDenyList(denyEntries) : undefined;
  const denyChanged = denyFile !== undefined && denyListText(denyEntries) !== denyListText(parseDenyList(denyFile.text) ?? []);
  const denyRemoved = denyFile ? (parseDenyList(denyFile.text) ?? []).filter((e) => !denyEntries.includes(e)) : [];

  return (
    <div class="page" data-testid="page-settings">
      <PageHead title={t("nav.settings")} sub={t("settings.sub")} />
      {loaded && !loaded.valid && (
        <Banner kind="danger" testid="settings-invalid-disk">
          {t("settings.invalidDisk")}
        </Banner>
      )}
      {saved && (
        <Banner kind={saved.restart ? "warn" : "ok"} testid="settings-saved">
          <div class="row">
            <span>{saved.restart ? t("settings.savedRestart") : t("settings.savedLive")}</span>
            {saved.restart && (
              <button type="button" class="btn small" onClick={() => setAskRestart(true)} disabled={!writable} data-testid="settings-restart">
                {t("settings.restart")}
              </button>
            )}
          </div>
        </Banner>
      )}
      {err && <ErrorBanner error={err} />}
      {err?.diagnostics && err.diagnostics.length > 0 && (
        <Banner kind="danger" testid="settings-diagnostics">
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
          <button type="button" class="btn" onClick={() => void load()} data-testid="settings-reload-latest">
            {t("config.loadLatest")}
          </button>
        </div>
      )}

      {form && loaded?.valid && (
        <form
          class="roles-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (content !== undefined && changed && writable) void prepare();
          }}
        >
          {!writable && <Banner kind="warn" testid="settings-readonly">{t("config.readOnly")}</Banner>}
          <fieldset disabled={!writable} class="settings-fields">
            <section class="card" id="settings-node" data-testid="settings-node">
              <h2>{t("settings.nodeTitle")}</h2>
              <Field label={t("settings.nodeName")} hint={t("wizard.nodeNameHint")}>
                <input
                  type="text"
                  value={form.nodeName}
                  onInput={(e) => update({ nodeName: (e.currentTarget as HTMLInputElement).value })}
                  aria-invalid={problem === "node_name"}
                  data-testid="settings-name"
                />
              </Field>
              <fieldset>
                <legend>{t("settings.mode")}</legend>
                {MODES.map((m) => (
                  <label class="check" key={m}>
                    <input type="radio" name="settings-mode" checked={form.mode === m} onChange={() => update({ mode: m })} data-testid={`settings-mode-${m}`} />
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
                      data-testid="settings-control-url"
                    />
                  </Field>
                  <Field label={t("wizard.authkey")} hint={t("wizard.authkeyHint")}>
                    <div class="row">
                      <input
                        type="text"
                        value={form.authkeyFile}
                        onInput={(e) => update({ authkeyFile: (e.currentTarget as HTMLInputElement).value })}
                        aria-invalid={problem === "authkey_file"}
                        data-testid="settings-authkey"
                      />
                      <button type="button" class="btn" onClick={() => void pick("authkeyFile").then((p) => p && update({ authkeyFile: p }))}>
                        {t("wizard.choose")}
                      </button>
                    </div>
                  </Field>
                </>
              )}
            </section>

            <section class="card" id="settings-roles" data-testid="settings-roles">
              <h2>{t("settings.rolesTitle")}</h2>
              <fieldset>
                <legend>{t("wizard.roles")}</legend>
                {ROLES.map((role) => (
                  <label class="check" key={role}>
                    <input
                      type="checkbox"
                      checked={form.roles.includes(role)}
                      onChange={(e) => update({ roles: toggleRole(form.roles, role, (e.currentTarget as HTMLInputElement).checked) })}
                      data-testid={`settings-role-${role}`}
                    />
                    <span>
                      <strong>{role}</strong> <span class="hint">{t(ROLE_HINT[role])}</span>
                    </span>
                  </label>
                ))}
              </fieldset>
              {ingressOn && (
                <section class="roles-ingress" data-testid="settings-ingress">
                  <h3>{t("settings.ingressTitle")}</h3>
                  <p class="hint">{t("settings.ingressSub")}</p>
                  {form.mode === "lan" && (
                    <Banner kind="warn" testid="settings-needs-tailnet">
                      {t("settings.needsTailnet")}
                    </Banner>
                  )}
                  <Field label={t("settings.listen")}>
                    <select value={form.listen} onChange={(e) => update({ listen: (e.currentTarget as HTMLSelectElement).value as ListenMode })} data-testid="settings-listen">
                      {LISTEN_MODES.map((m) => (
                        <option key={m} value={m}>
                          {t(LISTEN_LABEL[m])}
                        </option>
                      ))}
                    </select>
                  </Field>
                  <Field label={t("settings.issuer")} hint={t("settings.issuerHint")}>
                    <input
                      type="text"
                      value={form.issuer}
                      placeholder="https://"
                      onInput={(e) => update({ issuer: (e.currentTarget as HTMLInputElement).value })}
                      aria-invalid={problem === "issuer"}
                      data-testid="settings-issuer"
                    />
                  </Field>
                  {form.listen === "public:443" && (
                    <>
                      <Field label={t("settings.domain")} hint={t("settings.domainHint")}>
                        <input
                          type="text"
                          value={form.domain}
                          onInput={(e) => update({ domain: (e.currentTarget as HTMLInputElement).value })}
                          aria-invalid={problem === "domain"}
                          data-testid="settings-domain"
                        />
                      </Field>
                      <fieldset>
                        <legend>{t("settings.tls")}</legend>
                        {TLS_MODES.map((m) => (
                          <label class="check" key={m}>
                            <input type="radio" name="settings-tls" checked={form.tls === m} onChange={() => update({ tls: m })} data-testid={`settings-tls-${m}`} />
                            <span>{t(TLS_LABEL[m])}</span>
                          </label>
                        ))}
                      </fieldset>
                      {form.tls === "file" && (
                        <>
                          <Field label={t("settings.certFile")}>
                            <input
                              type="text"
                              value={form.certFile}
                              onInput={(e) => update({ certFile: (e.currentTarget as HTMLInputElement).value })}
                              aria-invalid={problem === "cert_files"}
                              data-testid="settings-cert"
                            />
                          </Field>
                          <Field label={t("settings.keyFile")}>
                            <input
                              type="text"
                              value={form.keyFile}
                              onInput={(e) => update({ keyFile: (e.currentTarget as HTMLInputElement).value })}
                              aria-invalid={problem === "cert_files"}
                              data-testid="settings-key"
                            />
                          </Field>
                        </>
                      )}
                    </>
                  )}
                </section>
              )}
            </section>

            <section class="card" id="settings-workdirs" data-testid="settings-workdirs">
              <h2>{t("settings.workdirsTitle")}</h2>
              <p class="hint">{t("settings.workdirsSub")}</p>
              <Field label={t("settings.defaultCwd")} hint={t("settings.defaultCwdHint")}>
                <div class="row">
                  <input
                    type="text"
                    value={form.defaultCwd}
                    onInput={(e) => update({ defaultCwd: (e.currentTarget as HTMLInputElement).value })}
                    aria-invalid={problem === "default_cwd"}
                    data-testid="settings-default-cwd"
                  />
                  <button type="button" class="btn" onClick={() => void pick("workspace").then((p) => p && update({ defaultCwd: p }))}>
                    {t("wizard.choose")}
                  </button>
                </div>
              </Field>
              <h3>{t("settings.allowTitle")}</h3>
              {form.allowDirs.length === 0 && (
                <Banner kind="warn" testid="settings-allow-empty">
                  {t("settings.allowEmpty")}
                </Banner>
              )}
              {form.allowDirs.map((dir, i) => (
                <div class="row" key={i}>
                  <input
                    type="text"
                    value={dir}
                    onInput={(e) => update({ allowDirs: form.allowDirs.map((d, j) => (j === i ? (e.currentTarget as HTMLInputElement).value : d)) })}
                    aria-invalid={problem === "allow_dirs" && !isAbsolutePath(dir.trim())}
                    data-testid={`settings-allow-${i}`}
                  />
                  <button type="button" class="btn small" onClick={() => update({ allowDirs: form.allowDirs.filter((_, j) => j !== i) })} data-testid={`settings-allow-remove-${i}`}>
                    {t("settings.remove")}
                  </button>
                </div>
              ))}
              <div class="row">
                <button type="button" class="btn" onClick={() => void pick("workspace").then((p) => p && update({ allowDirs: [...form.allowDirs, p] }))} data-testid="settings-allow-add">
                  {t("settings.addDir")}
                </button>
              </div>
            </section>
          </fieldset>

          {problem && changed && (
            <p class="hint" data-testid="settings-problem">
              {t(PROBLEM[problem])}
            </p>
          )}
          {!changed && <p class="hint" data-testid="settings-unchanged">{t("roles.noChanges")}</p>}
          <div class="row end" style={{ marginTop: "10px" }}>
            {changed && (
              <button type="button" class="btn" disabled={busy} onClick={() => loaded && setForm(formFromConfig(loaded.parsed))}>
                {t("config.revert")}
              </button>
            )}
            <button type="submit" class="btn primary" disabled={!writable || !changed || content === undefined || busy} data-testid="settings-review">
              {t("roles.review")}
            </button>
          </div>
        </form>
      )}

      <section class="card" id="settings-denylist" data-testid="settings-denylist">
        <h2>{t("settings.denyTitle")}</h2>
        <p class="hint">{t("settings.denySub")}</p>
        {denyErr && <ErrorBanner error={denyErr} testid="settings-deny-error" />}
        {denyFile && denyFile.path === undefined && (
          <Banner testid="settings-deny-unset">{t("settings.denyUnset")}</Banner>
        )}
        {denyFile?.path !== undefined && !denyFile.exists && (
          <Banner kind="danger" testid="settings-deny-missing">
            {t("settings.denyMissing")}
          </Banner>
        )}
        {denyFile?.path !== undefined && parseDenyList(denyFile.text) === undefined && (
          <Banner kind="danger" testid="settings-deny-invalid">
            {t("settings.denyInvalid")}
          </Banner>
        )}
        {denyFile?.path !== undefined && parseDenyList(denyFile.text) !== undefined && (
          <fieldset disabled={!writable} class="deny-fields">
            {denyEntries.map((entry, i) => (
              <div class="row" key={i}>
                <input
                  type="text"
                  value={entry}
                  onInput={(e) => {
                    setDenyEntries(denyEntries.map((d, j) => (j === i ? (e.currentTarget as HTMLInputElement).value : d)));
                    setDenySaved(false);
                  }}
                  data-testid={`deny-entry-${i}`}
                />
                <button type="button" class="btn small" onClick={() => setDenyEntries(denyEntries.filter((_, j) => j !== i))} data-testid={`deny-remove-${i}`}>
                  {t("settings.remove")}
                </button>
              </div>
            ))}
            <div class="row">
              <button type="button" class="btn" onClick={() => setDenyEntries([...denyEntries, ""])} data-testid="deny-add">
                {t("settings.addDeny")}
              </button>
              <button type="button" class="btn" onClick={() => setDenyEntries([...DEFAULT_DENY])} data-testid="deny-restore">
                {t("settings.restoreDeny")}
              </button>
            </div>
            {denyProblem && <p class="hint" data-testid="deny-problem">{t(denyProblem === "blank" ? "settings.denyBlank" : "settings.denyControl")}</p>}
            {denySaved && !denyChanged && <p class="hint" data-testid="deny-saved">{t("settings.denySaved")}</p>}
            <div class="row end" style={{ marginTop: "10px" }}>
              <button type="button" class="btn primary" disabled={!writable || !denyChanged || denyProblem !== undefined || busy} onClick={() => setDenyReview(true)} data-testid="deny-review">
                {t("settings.denySave")}
              </button>
            </div>
          </fieldset>
        )}
      </section>

      {review && (
        <Confirm
          title={t("roles.reviewTitle")}
          confirmLabel={t("config.save")}
          danger={weak.length > 0}
          busy={busy}
          onCancel={() => setReview(undefined)}
          onConfirm={() => void save()}
          body={
            <div data-testid="settings-diff">
              {weak.length > 0 && (
                <Banner kind="danger" testid="settings-weakens">
                  <p>{t("config.weakens")}</p>
                  <p class="hint">{t("config.weakensNote")}</p>
                </Banner>
              )}
              {needsRestart(review.changes) && <Banner kind="warn" testid="settings-needs-restart">{t("config.needsRestart")}</Banner>}
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

      {denyReview && denyFile && (
        <Confirm
          title={t("settings.denyReviewTitle")}
          confirmLabel={t("config.save")}
          danger={denyRemoved.length > 0}
          busy={busy}
          onCancel={() => setDenyReview(false)}
          onConfirm={() => void saveDeny()}
          body={
            <div data-testid="deny-diff">
              {denyRemoved.length > 0 && (
                <Banner kind="danger" testid="deny-weakens">
                  <p>{t("settings.denyWeakens")}</p>
                  <ul>
                    {denyRemoved.map((e) => (
                      <li key={e}>
                        <code>{e}</code>
                      </li>
                    ))}
                  </ul>
                </Banner>
              )}
              <p class="hint">{t("settings.denyReviewNote")}</p>
            </div>
          }
        />
      )}

      {askRestart && (
        <Confirm
          title={t("settings.restartTitle")}
          body={<p>{t("settings.restartBody")}</p>}
          confirmLabel={t("settings.restart")}
          busy={busy}
          onCancel={() => setAskRestart(false)}
          onConfirm={() => void restart()}
        />
      )}
    </div>
  );
}
