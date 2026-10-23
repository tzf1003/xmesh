import { isListen } from "../../shared/gateway";
import { useCopy } from "../design-copy";
import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, ErrorBanner, Field, NotSupported, PageHead } from "../components";
import { useT } from "../i18n";
import { cwdPolicy, diffRows, needsRestart, weakening } from "../lib/config";
import {
  DEFAULT_REFRESH_DAYS,
  DEFAULT_TUNNEL_PORT,
  MAX_REFRESH_DAYS,
  LISTEN_MODES,
  MODES,
  SCOPE_CHOICES,
  TLS_MODES,
  applyDenyFile,
  applySettingsScope,
  checkDenyList,
  checkSettingsScope,
  formFromConfig,
  isChanged,
  otherScopes,
  parseDenyList,
  toggleScope,
  withOtherScopes,
  type ListenMode,
  type Mode,
  type SettingsForm,
  type SettingsProblem,
  type TLSMode,
} from "../lib/settings";
import { DEFAULT_DENY, denyListText, isAbsolutePath } from "../../shared/setup";
import { applySaved } from "../lib/reload";
import type { ActionError, DenyFileResponse } from "../../shared/ipc";
import type { ConfigChange, ConfigResponse } from "../../shared/control";
import type { Key } from "../messages";

const MODE_LABEL: Record<Mode, Key> = { lan: "mode.lan", tailnet: "mode.tailnet", coexist: "mode.coexist", offline: "mode.offline" };
const LISTEN_LABEL: Record<ListenMode, Key> = { tunnel: "settings.listen.tunnel", "public:443": "settings.listen.public443", funnel: "settings.listen.funnel" };
const SCOPE_LABEL: Record<(typeof SCOPE_CHOICES)[number], Key> = {
  "devices:read": "settings.scope.devicesRead",
  "sys:*": "settings.scope.sys",
  "fs:read:*": "settings.scope.fsRead",
  "fs:write:*": "settings.scope.fsWrite",
  "exec:*": "settings.scope.exec",
  "pty:*": "settings.scope.pty",
  "backend:*:*": "settings.scope.backend",
  "computer:observe:*": "settings.scope.computerObserve",
  "computer:control:*": "settings.scope.computerControl",
  "computer:clipboard-read:*": "settings.scope.clipboardRead",
  "computer:clipboard-write:*": "settings.scope.clipboardWrite",
};
const TLS_LABEL: Record<TLSMode, Key> = { autocert: "settings.tls.autocert", file: "settings.tls.file" };
const PROBLEM: Record<SettingsProblem, Key> = {
  node_name: "settings.problem.node_name",
  roles: "settings.problem.roles",
  tailnet: "settings.problem.tailnet",
  control_url: "settings.problem.control_url",
  authkey: "settings.problem.authkey",
  issuer: "settings.problem.issuer",
  tunnel_port: "settings.problem.tunnel_port",
  dynamic_scopes: "settings.problem.dynamic_scopes",
  refresh_days: "settings.problem.refresh_days",
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
  authkey?: string;
}

export type ModuleScope = "node" | "agent" | "gateway" | "ingress" | "transfers";

export function ModuleSettings({ ctx, scope = "node", onDirtyChange }: { ctx: PageContext; scope?: ModuleScope; onDirtyChange?(dirty: boolean): void }): JSX.Element {
  const t = useT();
  const copy = useCopy();
  const [listen, setListen] = useState("127.0.0.1:8080");
  const [execLimit, setExecLimit] = useState(4);
  const [ptyLimit, setPtyLimit] = useState(20);
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
    const extraBase = loaded?.parsed as { gateway?: { listen?: string }; agent?: { exec?: { max_concurrent?: number }; pty?: { max_sessions?: number } } } | undefined;
    const extras = scope === "gateway" ? listen !== (extraBase?.gateway?.listen ?? "127.0.0.1:8080") : scope === "agent" && (execLimit !== (extraBase?.agent?.exec?.max_concurrent ?? 4) || ptyLimit !== (extraBase?.agent?.pty?.max_sessions ?? 20));
    onDirtyChange?.(mainDirty || extras || (!!denyFile && denyListText(denyEntries) !== denyListText(base)));
  }, [loaded, form, denyFile, denyEntries, onDirtyChange, listen, execLimit, ptyLimit]);

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
    const parsed = r.data.parsed as { gateway?: { listen?: string }; agent?: { exec?: { max_concurrent?: number }; pty?: { max_sessions?: number } } };
    setListen(parsed.gateway?.listen ?? "127.0.0.1:8080");
    setExecLimit(parsed.agent?.exec?.max_concurrent ?? 4);
    setPtyLimit(parsed.agent?.pty?.max_sessions ?? 20);
    // Navigation may reveal ingress setup, but must never change the configured roles.
    setForm(formFromConfig(r.data.parsed));
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
    if (scope === "agent") void loadDeny();
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
  const problem = form ? checkSettingsScope(form, scope) : undefined;
  const invalidExtras = scope === "gateway" ? !isListen(listen) : scope === "agent" && ![execLimit, ptyLimit].every(n => Number.isInteger(n) && n > 0);
  const baseExtras = loaded?.parsed as { gateway?: { listen?: string }; agent?: { exec?: { max_concurrent?: number }; pty?: { max_sessions?: number } } } | undefined;
  const extrasChanged = scope === "gateway" ? listen !== (baseExtras?.gateway?.listen ?? "127.0.0.1:8080") : scope === "agent" && (execLimit !== (baseExtras?.agent?.exec?.max_concurrent ?? 4) || ptyLimit !== (baseExtras?.agent?.pty?.max_sessions ?? 20));
  const changed = loaded !== undefined && form !== undefined && (isChanged(formFromConfig(loaded.parsed), form) || extrasChanged);
  const content = loaded && form ? applySettingsScope(loaded.content, form, scope, { listen, execLimit, ptyLimit }) : undefined;
  const ingressOn = scope === "ingress";
  const authkey = form && (form.mode === "tailnet" || form.mode === "coexist") ? form.authkey.trim() : "";
  const savedRoles = loaded ? formFromConfig(loaded.parsed).roles : [];

  const prepare = async (): Promise<void> => {
    if (problem || invalidExtras) return;
    if (scope === "gateway" && !isListen(listen)) { ctx.toast(copy("监听地址格式不正确。", "Invalid listen address."), true); return; }
    if (scope === "agent" && (![execLimit, ptyLimit].every(n => Number.isInteger(n) && n > 0))) { ctx.toast(copy("执行限制需为正整数。", "Limits must be positive integers."), true); return; }
    if (!loaded || content === undefined) return;
    setBusy(true);
    const r = await getApi().putConfig({ content, etag: loaded.etag, dryRun: true, ...(authkey && { authkey }) });
    setBusy(false);
    if (!r.ok) return setErr(r.error);
    setErr(undefined);
    setReview({ changes: r.data.diff, content: r.data.content ?? content, ...(authkey && { authkey }) });
  };

  const save = async (): Promise<void> => {
    if (!loaded || !review) return;
    setBusy(true);
    const r = await getApi().putConfig({ content: review.content, etag: loaded.etag, dryRun: false, ...(review.authkey && { authkey: review.authkey }) });
    setBusy(false);
    setReview(undefined);
    if (!r.ok) return setErr(r.error);
    setForm((f) => f ? { ...f, authkey: "" } : f);
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

  const pick = async (kind: "workspace"): Promise<string | undefined> => {
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
    <div class={`page module-settings scope-${scope}`} data-testid="page-module-settings">
      <PageHead title={t(scope === "node" ? "settings.nodeTitle" : scope === "agent" ? "settings.workdirsTitle" : scope === "ingress" ? "settings.ingressTitle" : scope === "transfers" ? "settings.transfersTitle" : "nav.runtime")} />
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
          class="roles-form section-stack"
          onSubmit={(e) => {
            e.preventDefault();
            if (content !== undefined && changed && writable && !problem && !invalidExtras) void prepare();
          }}
        >
          {!writable && <Banner kind="warn" testid="settings-readonly">{t("config.readOnly")}</Banner>}
          <fieldset disabled={!writable || busy} class="settings-fields section-stack">
            {scope === "gateway" && <section class="card"><h2>{copy("客户端接入", "Client access")}</h2><Field label={copy("监听地址", "Listen address")} hint={copy("默认仅监听本机。", "Loopback is the default.")}><input type="text" value={listen} onInput={e => {setListen(e.currentTarget.value);setReview(undefined);ctx.applied(undefined);}} data-testid="settings-gateway-listen" aria-invalid={!isListen(listen)}/></Field><p class="hint">{copy("令牌与权限在访问与授权中管理。", "Manage tokens and permissions under Access & authorization.")}</p><button type="button" class="text-button" onClick={() => ctx.go("authz")}>{copy("访问与授权", "Access & authorization")}</button></section>}
            {scope === "agent" && <section class="card"><h2>{copy("执行限制", "Execution limits")}</h2><Field label={copy("命令并发上限", "Concurrent commands")}><input type="number" min={1} value={execLimit} onInput={e=>{setExecLimit(Number(e.currentTarget.value));setReview(undefined);ctx.applied(undefined);}} data-testid="settings-exec-limit"/></Field><Field label={copy("终端会话上限", "Terminal session limit")}><input type="number" min={1} value={ptyLimit} onInput={e=>{setPtyLimit(Number(e.currentTarget.value));setReview(undefined);ctx.applied(undefined);}} data-testid="settings-pty-limit"/></Field></section>}
            <section hidden={scope !== "node"} class="card" id="settings-node" data-testid="settings-node">
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
                  {form.browserLogin && !authkey && <Banner>{t("settings.browserLogin")}</Banner>}
                  <Field label={t("settings.authkey")} hint={t("settings.authkeyHint")}>
                    <input
                      type="password"
                      autoComplete="new-password"
                      spellcheck={false}
                      value={form.authkey}
                      placeholder={t(form.authkeyFile || form.browserLogin ? "settings.authkeySaved" : "wizard.authkeyPlaceholder")}
                      onInput={(e) => update({ authkey: (e.currentTarget as HTMLInputElement).value })}
                      aria-invalid={problem === "authkey"}
                      data-testid="settings-authkey"
                    />
                  </Field>
                </>
              )}
            </section>

            <section hidden={scope !== "ingress"} class="card" id="settings-roles" data-testid="settings-roles">
              <h2>{t("settings.rolesTitle")}</h2>
              <p class="hint">{t("roles.topbarHint")}</p>
              <div class="row" data-testid="settings-role-summary">
                {savedRoles.map((role) => <Badge key={role} kind="info">{role}</Badge>)}
              </div>
              {(ingressOn || ctx.focus?.role === "ingress") && (
                <section class="roles-ingress" data-testid="settings-ingress">
                  <h3>{t("settings.ingressTitle")}</h3>
                  <p class="hint">{t("settings.ingressSub")}</p>
                  {ingressOn && <>
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
                  {form.listen === "tunnel" && (
                    <Field label={t("settings.tunnelPort")} hint={t("settings.tunnelPortHint", { port: String(DEFAULT_TUNNEL_PORT) })}>
                      <input
                        type="text"
                        inputMode="numeric"
                        value={form.tunnelPort}
                        placeholder={String(DEFAULT_TUNNEL_PORT)}
                        onInput={(e) => update({ tunnelPort: (e.currentTarget as HTMLInputElement).value })}
                        aria-invalid={problem === "tunnel_port"}
                        data-testid="settings-tunnel-port"
                      />
                    </Field>
                  )}
                  <Field label={t("settings.refreshDays")} hint={t("settings.refreshDaysHint", { days: String(DEFAULT_REFRESH_DAYS), max: String(MAX_REFRESH_DAYS) })}>
                    <input
                      type="text"
                      inputMode="numeric"
                      value={form.refreshDays}
                      placeholder={String(DEFAULT_REFRESH_DAYS)}
                      onInput={(e) => update({ refreshDays: (e.currentTarget as HTMLInputElement).value })}
                      aria-invalid={problem === "refresh_days"}
                      data-testid="settings-refresh-days"
                    />
                  </Field>
                  <fieldset data-testid="settings-dynamic-scopes">
                    <legend>{t("settings.dynamicScopes")}</legend>
                    <p class="hint">{t("settings.dynamicScopesHint")}</p>
                    {SCOPE_CHOICES.map((scope) => (
                      <label class="check" key={scope}>
                        <input
                          type="checkbox"
                          checked={form.dynamicScopes.includes(scope)}
                          onChange={(e) => update({ dynamicScopes: toggleScope(form.dynamicScopes, scope, e.currentTarget.checked) })}
                          data-testid={`settings-scope-${scope}`}
                        />
                        <span><code>{scope}</code> {t(SCOPE_LABEL[scope])}</span>
                      </label>
                    ))}
                    <Field label={t("settings.dynamicScopesOther")} hint={t("settings.dynamicScopesOtherHint")}>
                      <input
                        type="text"
                        spellcheck={false}
                        value={otherScopes(form.dynamicScopes).join(", ")}
                        onChange={(e) => update({ dynamicScopes: withOtherScopes(form.dynamicScopes, e.currentTarget.value) })}
                        aria-invalid={problem === "dynamic_scopes"}
                        data-testid="settings-scope-other"
                      />
                    </Field>
                  </fieldset>
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
                  </>}
                </section>
              )}
            </section>

            <section hidden={scope !== "transfers"} class="card" id="settings-transfers" data-testid="settings-transfers">
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

            <section hidden={scope !== "agent"} class="card" id="settings-workdirs" data-testid="settings-workdirs">
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
              <button type="button" class="btn" disabled={busy} onClick={() => { if (!loaded) return; setForm(formFromConfig(loaded.parsed)); const base = loaded.parsed as { gateway?: { listen?: string }; agent?: { exec?: { max_concurrent?: number }; pty?: { max_sessions?: number } } }; setListen(base.gateway?.listen ?? "127.0.0.1:8080"); setExecLimit(base.agent?.exec?.max_concurrent ?? 4); setPtyLimit(base.agent?.pty?.max_sessions ?? 20); setReview(undefined); }}>
                {t("config.revert")}
              </button>
            )}
            <button type="submit" class="btn primary" disabled={!writable || !changed || problem !== undefined || invalidExtras || content === undefined || busy} data-testid="settings-review">
              {t("roles.review")}
            </button>
          </div>
        </form>
      )}

      <section hidden={scope !== "agent"} class="card" id="settings-denylist" data-testid="settings-denylist">
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
              {review.authkey && <Banner testid="settings-authkey-change">{t("settings.authkeyChanged")}</Banner>}
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
