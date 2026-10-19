import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, ErrorBanner, Field, NotSupported, PageHead } from "../components";
import { useT } from "../i18n";
import { cwdPolicy, diffRows, needsRestart, weakening } from "../lib/config";
import {
  LISTEN_MODES,
  MODES,
  ROLES,
  TLS_MODES,
  applyDenyFile,
  applySettingsForm,
  checkDenyList,
  checkSettingsForm,
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
import { DEFAULT_DENY, denyListText, isAbsolutePath } from "../../shared/setup";
import { applySaved } from "../lib/reload";
import type { ActionError, DenyFileResponse } from "../../shared/ipc";
import type { ConfigChange, ConfigResponse } from "../../shared/control";
import type { Key } from "../messages";

const ROLE_HINT: Record<Role, Key> = { agent: "wizard.agentHint", gateway: "wizard.gatewayHint", ingress: "settings.ingressHint" };
const MODE_LABEL: Record<Mode, Key> = { lan: "mode.lan", tailnet: "mode.tailnet", coexist: "mode.coexist", offline: "mode.offline" };
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
  max_file_size: "settings.problem.max_file_size",
};

/** A saved dry run: the kernel's diff for `content`, which is what Save writes. */
interface Review {
  changes: ConfigChange[];
  content: string;
}

export function Settings({ ctx, onDirtyChange }: { ctx: PageContext; onDirtyChange?(dirty: boolean): void }): JSX.Element {
  const t = useT();
  const supported = ctx.can("config.read");
  const writable = ctx.can("config.write");
  const [loaded, setLoaded] = useState<ConfigResponse | undefined>();
  const [form, setForm] = useState<SettingsForm | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();
  const [review, setReview] = useState<Review | undefined>();
  const [busy, setBusy] = useState(false);

  const [denyFile, setDenyFile] = useState<DenyFileResponse | undefined>();
  const [denyEntries, setDenyEntries] = useState<string[]>([]);
  const [denyErr, setDenyErr] = useState<ActionError | undefined>();
  const [denyReview, setDenyReview] = useState(false);
  const [denySaved, setDenySaved] = useState(false);
  useEffect(() => {
    const mainDirty = !!loaded && !!form && isChanged(formFromConfig(loaded.parsed), form);
    const base = denyFile?.path === undefined ? cwdPolicy(loaded?.parsed).deny : (parseDenyList(denyFile?.text ?? "") ?? []);
    onDirtyChange?.(mainDirty || (!!denyFile && denyListText(denyEntries) !== denyListText(base)));
  }, [loaded, form, denyFile, denyEntries, onDirtyChange]);

  // Reads can overlap (a save re-reads what an earlier read is still fetching); only the latest may write the page.
  const loadSeq = useRef(0);
  const denySeq = useRef(0);

  const load = async (): Promise<void> => {
    const seq = ++loadSeq.current;
    const r = await getApi().getConfig();
    if (seq !== loadSeq.current) return;
    if (!r.ok) return setErr(r.error);
    setErr(undefined);
    setLoaded(r.data);
    const current = formFromConfig(r.data.parsed);
    // A link to the ingress role opens with it on in the form; nothing is saved until the user does.
    setForm(ctx.focus?.role === "ingress" ? { ...current, roles: toggleRole(current.roles, "ingress", true) } : current);
    setReview(undefined);
  };

  // Without a deny list file the list shown is the config's inline one, which saving moves into a file.
  const loadDeny = async (): Promise<void> => {
    const seq = ++denySeq.current;
    const r = await getApi().readDenyFile();
    if (seq !== denySeq.current) return;
    if (!r.ok) return setDenyErr(r.error);
    setDenyErr(undefined);
    setDenyFile(r.data);
    if (r.data.path !== undefined) return setDenyEntries(parseDenyList(r.data.text) ?? []);
    const cfg = await getApi().getConfig();
    if (seq !== denySeq.current) return;
    setDenyEntries(cfg.ok ? cwdPolicy(cfg.data.parsed).deny : []);
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
    ctx.applied(undefined);
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
    ctx.applied(await applySaved(r.data.restart_required));
    ctx.toast(t("config.saved"));
    await load();
  };

  // Writes the deny list file the config already names, then has the kernel read it again.
  const writeDenyList = async (file: DenyFileResponse): Promise<ActionError | undefined> => {
    const r = await getApi().writeDenyFile({ text: denyListText(denyEntries), etag: file.etag });
    if (!r.ok) return r.error;
    ctx.applied(await applySaved(false));
    return undefined;
  };

  // A config whose deny list is still inline gets a file beside it, and the config is pointed at that file. The
  // file is written first, since the kernel refuses a config that names a missing file; if saving the config then
  // fails, the next save writes the same file again.
  const migrateDeny = async (): Promise<ActionError | undefined> => {
    if (!loaded) return undefined;
    const created = await getApi().createDenyFile({ text: denyListText(denyEntries) });
    if (!created.ok) return created.error;
    const next = applyDenyFile(loaded.content, created.data.path);
    if (next === undefined) return { code: "config_unparsed", message: "配置无法解析，禁止目录没有迁移。" };
    const r = await getApi().putConfig({ content: next, etag: loaded.etag, dryRun: false });
    if (!r.ok) return r.error;
    ctx.applied(await applySaved(r.data.restart_required));
    return undefined;
  };

  const saveDeny = async (): Promise<void> => {
    if (!denyFile) return;
    setBusy(true);
    const failure = denyFile.path === undefined ? await migrateDeny() : await writeDenyList(denyFile);
    setBusy(false);
    setDenyReview(false);
    if (failure) return setDenyErr(failure);
    setDenySaved(true);
    setDenyErr(undefined);
    ctx.toast(t("settings.denySaved"));
    // A migration changed the config, so its etag and the form are read again; the form has no unsaved edits by then.
    if (denyFile.path === undefined) await load();
    await loadDeny();
  };

  const pick = async (kind: "workspace" | "authkeyFile"): Promise<string | undefined> => {
    const r = await getApi().pickPath(kind);
    return r.ok ? (r.data.path ?? undefined) : undefined;
  };

  const weak = review ? weakening(review.changes) : [];
  const rows = review ? diffRows(review.changes) : [];
  const denyProblem = denyEntries.length > 0 ? checkDenyList(denyEntries) : undefined;
  const denyUnset = denyFile !== undefined && denyFile.path === undefined;
  const denyEditable = denyFile !== undefined && (denyUnset || parseDenyList(denyFile.text) !== undefined);
  // Unset, the base is the config's inline list, and saving is always a move, so it is never "unchanged".
  const denyBase = denyUnset ? cwdPolicy(loaded?.parsed).deny : (parseDenyList(denyFile?.text ?? "") ?? []);
  const denyChanged = denyFile !== undefined && (denyUnset || denyListText(denyEntries) !== denyListText(denyBase));
  const denyRemoved = denyFile ? denyBase.filter((e) => !denyEntries.includes(e)) : [];

  return (
    <div class="page" data-testid="page-settings">
      <PageHead title={t("nav.settings")} sub={t("settings.sub")} />
      {loaded && !loaded.valid && (
        <Banner kind="danger" testid="settings-invalid-disk">
          {t("settings.invalidDisk")}
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
                      <strong>{t(MODE_LABEL[m])}</strong> <span class="hint">{t(m === "coexist" ? "device.preferLan" : m === "lan" ? "wizard.lanHint" : m === "tailnet" ? "wizard.tailnetHint" : "mode.offline")}</span>
                    </span>
                  </label>
                ))}
              </fieldset>
              {(form.mode === "tailnet" || form.mode === "coexist") && (
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
                  {form.browserLogin ? <Banner>{t("settings.browserLogin")}</Banner> : <Field label={t("settings.authkey")} hint={t("settings.authkeyHint")}>
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
                  </Field>}
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
                  {form.listen === "funnel" && form.mode !== "tailnet" && form.mode !== "coexist" && (
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

            <section class="card" id="settings-transfers" data-testid="settings-transfers">
              <h2>{t("settings.transfersTitle")}</h2>
              <Field label={t("settings.maxFileSize")} hint={t("settings.maxFileSizeHint")}>
                <label class="row">
                  <input type="checkbox" checked={form.maxFileSize === "unlimited"}
                    onChange={(e) => update({ maxFileSize: e.currentTarget.checked ? "unlimited" : "1GiB" })}
                    data-testid="settings-file-unlimited" />
                  {t("settings.fileUnlimited")}
                </label>
                {form.maxFileSize !== "unlimited" && <input type="text" value={form.maxFileSize}
                  onInput={(e) => update({ maxFileSize: e.currentTarget.value })}
                  aria-invalid={problem === "max_file_size"} data-testid="settings-max-file-size" />}
              </Field>
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
        {denyUnset && <Banner testid="settings-deny-unset">{t("settings.denyUnset")}</Banner>}
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
        {denyEditable && (
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
            {denyUnset && changed && <p class="hint" data-testid="deny-finish-first">{t("settings.denyFinishFirst")}</p>}
            <div class="row end" style={{ marginTop: "10px" }}>
              <button type="button" class="btn primary" disabled={!writable || !denyChanged || denyProblem !== undefined || busy || (denyUnset && changed)} onClick={() => setDenyReview(true)} data-testid="deny-review">
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
    </div>
  );
}
