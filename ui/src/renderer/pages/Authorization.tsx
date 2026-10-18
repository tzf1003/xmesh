import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Dialog, Empty, ErrorBanner, Field, NotSupported, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { type Key } from "../messages";
import { errorText, useLocale, useT, type T } from "../i18n";
import { MAX_CONFIG_EDITS, applyConfigEdits } from "../lib/apply-config";
import { type AuthzRow, authzRows, cwdPolicy } from "../lib/config";
import {
  BACKEND_POLICIES,
  MAX_EDITS,
  TAG_KEY,
  type AuthzForm,
  type BackendForm,
  type BackendPolicy,
  type BackendRow,
  type Problem,
  type Transport,
  authzDeleteEdits,
  authzSaveEdits,
  authzState,
  backendDeleteEdits,
  backendEdits,
  backendNames,
  backendRows,
  checkAuthzForm,
  checkBackendForm,
  disabledTools,
  exactGrants,
  grantedTags,
  toolEnabled,
  toolToggleEdits,
} from "../lib/mcp";
import { type ImportResult, parseMcpImport } from "../lib/mcp-import";
import { formatTime } from "../lib/format";
import { SCOPE_PRESETS, checkScopes, isBroadScope } from "../lib/scopes";
import { BUILTIN_TOOLS, isBuiltinTool } from "../../shared/builtin-tools";
import { parseNodeName } from "../../shared/ipc";
import type { ActionError, ConfigPatchEdit } from "../../shared/ipc";
import type { ConfigResponse, PatInfo, PatSecret } from "../../shared/control";

export function Authorization({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  return (
    <div class="page" data-testid="page-authz">
      <PageHead title={t("nav.authz")} sub={t("authz.sub")} />
      {ctx.can("pats") ? <Pats ctx={ctx} /> : <NotSupported what={t("authz.pats")} />}
      {ctx.can("config.read") ? <ToolAccess ctx={ctx} /> : <NotSupported what={t("authz.tools")} />}
    </div>
  );
}

function Pats({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const { resource, reload } = useResource(() => getApi().listPats(), [ctx.snapshot.selectedInstance]);
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<PatSecret | undefined>();
  const [deleting, setDeleting] = useState<PatInfo | undefined>();
  const [rotating, setRotating] = useState<PatInfo | undefined>();
  const [busy, setBusy] = useState(false);

  const rotate = async (p: PatInfo): Promise<void> => {
    setBusy(true);
    const r = await getApi().rotatePat(p.name);
    setBusy(false);
    setRotating(undefined);
    if (r.ok) {
      setSecret(r.data);
      reload();
    } else ctx.toast(errorText(t, r.error), true);
  };
  const remove = async (p: PatInfo): Promise<void> => {
    setBusy(true);
    const r = await getApi().deletePat(p.name);
    setBusy(false);
    setDeleting(undefined);
    if (r.ok) {
      ctx.toast(t("authz.deleted", { name: p.name }));
      reload();
    } else ctx.toast(errorText(t, r.error), true);
  };

  return (
    <section class="card" data-testid="pats">
      <div class="page-head">
        <div class="grow">
          <h2>{t("authz.pats")}</h2>
          <p class="hint">{t("authz.patsHint")}</p>
        </div>
        <button type="button" class="btn primary" onClick={() => setCreating(true)} data-testid="pat-create">
          {t("authz.create")}
        </button>
      </div>
      <ResourceView resource={resource}>
        {(list) => (
          <>
            <Banner testid="pats-policy">{t("authz.tokenPolicy")}</Banner>
            {list.pats.length === 0 ? (
              <Empty>{t("authz.noPats")}</Empty>
            ) : (
              <div class="table-wrap">
                <table data-testid="pats-table">
                  <thead>
                    <tr>
                      <th scope="col">{t("col.name")}</th>
                      <th scope="col">{t("col.scopes")}</th>
                      <th scope="col">{t("col.tokenFile")}</th>
                      <th scope="col">{t("col.modified")}</th>
                      <th scope="col" />
                    </tr>
                  </thead>
                  <tbody>
                    {list.pats.map((p) => (
                      <tr key={p.name} data-testid={`pat-${p.name}`}>
                        <td>
                          <strong>{p.name}</strong>
                        </td>
                        <td>
                          <div class="row">
                            {p.scopes.map((s) => (
                              <Badge key={s} kind={isBroadScope(s) ? "warn" : ""}>
                                {s}
                              </Badge>
                            ))}
                          </div>
                        </td>
                        <td class="mono">{p.token_file}</td>
                        <td>{formatTime(p.modified_at)}</td>
                        <td>
                          <div class="row end">
                            <button type="button" class="btn small" onClick={() => setRotating(p)}>
                              {t("authz.rotate")}
                            </button>
                            <button type="button" class="btn small danger" onClick={() => setDeleting(p)}>
                              {t("authz.delete")}
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </ResourceView>
      {creating && (
        <CreatePat
          onClose={() => setCreating(false)}
          onCreated={(s) => {
            setCreating(false);
            setSecret(s);
            reload();
          }}
        />
      )}
      {secret && <SecretDialog secret={secret} onClose={() => setSecret(undefined)} />}
      {deleting && (
        <Confirm
          title={t("authz.deleteTitle", { name: deleting.name })}
          body={<p>{t("authz.deleteBody")}</p>}
          confirmLabel={t("authz.delete")}
          danger
          busy={busy}
          onCancel={() => setDeleting(undefined)}
          onConfirm={() => void remove(deleting)}
        />
      )}
      {rotating && (
        <Confirm
          title={t("authz.rotateTitle", { name: rotating.name })}
          body={<p>{t("authz.rotateBody")}</p>}
          confirmLabel={t("authz.rotate")}
          busy={busy}
          onCancel={() => setRotating(undefined)}
          onConfirm={() => void rotate(rotating)}
        />
      )}
    </section>
  );
}

function CreatePat({ onClose, onCreated }: { onClose: () => void; onCreated: (s: PatSecret) => void }): JSX.Element {
  const t = useT();
  const [name, setName] = useState("");
  const [scopeText, setScopeText] = useState(SCOPE_PRESETS[0]!.scopes.join("\n"));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const check = checkScopes(scopeText);
  const nameOk = parseNodeName(name) !== undefined;
  const valid = nameOk && check.scopes.length > 0 && check.invalid.length === 0;
  const broad = check.scopes.filter(isBroadScope);

  const submit = async (e: Event): Promise<void> => {
    e.preventDefault();
    if (!valid) return;
    setBusy(true);
    const r = await getApi().createPat({ name, scopes: check.scopes });
    setBusy(false);
    if (r.ok) onCreated(r.data);
    else setErr(r.error);
  };

  return (
    <Dialog title={t("authz.create")} onClose={onClose} testid="pat-dialog">
      <form onSubmit={(e) => void submit(e)}>
        <Field label={t("col.name")} hint={t("authz.nameHint")}>
          <input type="text" value={name} onInput={(e) => setName((e.currentTarget as HTMLInputElement).value)} aria-invalid={name !== "" && !nameOk} autocomplete="off" data-testid="pat-name" />
        </Field>
        <Field label={t("authz.preset")}>
          <select
            onChange={(e) => {
              const p = SCOPE_PRESETS.find((x) => x.id === (e.currentTarget as HTMLSelectElement).value);
              if (p) setScopeText(p.scopes.join("\n"));
            }}
            data-testid="pat-preset"
          >
            {SCOPE_PRESETS.map((p) => (
              <option key={p.id} value={p.id}>
                {t(`preset.${p.id}`)}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t("col.scopes")} hint={t("authz.scopesHint")}>
          <textarea rows={5} value={scopeText} onInput={(e) => setScopeText((e.currentTarget as HTMLTextAreaElement).value)} aria-invalid={check.invalid.length > 0} data-testid="pat-scopes" />
        </Field>
        {check.invalid.length > 0 && <Banner kind="danger" testid="pat-invalid">{t("authz.invalidScopes", { list: check.invalid.join(", ") })}</Banner>}
        {broad.length > 0 && <Banner kind="warn">{t("authz.broad", { list: broad.join(", ") })}</Banner>}
        {err && <ErrorBanner error={err} />}
        <div class="row end" style={{ marginTop: "12px" }}>
          <button type="button" class="btn" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button type="submit" class="btn primary" disabled={!valid || busy} data-testid="pat-submit">
            {t("authz.create")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

/** The token is shown once. Closing the dialog drops it from memory; nothing is stored. */
function SecretDialog({ secret, onClose }: { secret: PatSecret; onClose: () => void }): JSX.Element {
  const t = useT();
  return (
    <Dialog title={t("authz.secretTitle", { name: secret.pat.name })} onClose={onClose} testid="pat-secret-dialog">
      <Banner kind="warn">{t("authz.secretWarn")}</Banner>
      <p>
        <span class="secret" data-testid="pat-secret">
          {secret.token}
        </span>
      </p>
      <p class="hint">{t("authz.secretFile", { file: secret.pat.token_file })}</p>
      <div class="row end">
        <button type="button" class="btn primary" onClick={onClose} data-testid="pat-secret-close">
          {t("authz.secretDone")}
        </button>
      </div>
    </Dialog>
  );
}

/** Shown once the config is read: the MCP servers, the built-in tool switches and the tag grants. They share one read and one write path. */
function ToolAccess({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const { resource, reload } = useResource(() => getApi().getConfig(), [ctx.snapshot.selectedInstance]);
  const [restart, setRestart] = useState(false);
  const writable = ctx.can("config.write");

  /** One PATCH per action. Success reloads the config; a stale etag also reloads, so the next try starts from the file on disk. */
  const write = async (cfg: ConfigResponse, edits: ConfigPatchEdit[]): Promise<ActionError | undefined> => {
    if (edits.length > MAX_CONFIG_EDITS) return { code: "too_many_edits", message: t("config.tooManyEdits", { count: edits.length, max: MAX_CONFIG_EDITS }) };
    const r = await applyConfigEdits(cfg.etag, edits);
    if (!r.ok) {
      if (r.error.code === "precondition_failed") reload();
      return r.error;
    }
    setRestart(r.data.restart_required);
    ctx.toast(t(r.data.restart_required ? "config.savedRestart" : "config.savedLive"));
    reload();
    return undefined;
  };

  return (
    <>
      {!writable && <Banner kind="warn" testid="config-readonly">{t("config.readOnly")}</Banner>}
      {restart && <Banner kind="warn" testid="authz-restart">{t("config.savedRestart")}</Banner>}
      <ResourceView resource={resource}>
        {(cfg) => {
          const editable = writable && cfg.valid;
          const apply = (edits: ConfigPatchEdit[]): Promise<ActionError | undefined> => write(cfg, edits);
          return (
            <>
              {!cfg.valid && <Banner kind="danger" testid="authz-invalid">{t("authz.invalidConfig")}</Banner>}
              <McpCard ctx={ctx} cfg={cfg} editable={editable} apply={apply} />
              <BuiltinCard ctx={ctx} cfg={cfg} editable={editable} apply={apply} />
              <AuthzCard ctx={ctx} cfg={cfg} editable={editable} apply={apply} />
            </>
          );
        }}
      </ResourceView>
    </>
  );
}

type Apply = (edits: ConfigPatchEdit[]) => Promise<ActionError | undefined>;

interface CardProps {
  ctx: PageContext;
  cfg: ConfigResponse;
  editable: boolean;
  apply: Apply;
}

/** Form-level problem codes (`url.host`, `name.duplicate`, ...) as the panel's sentences. */
function problemText(t: T, p: { code: string }): string {
  return t(`mcp.err.${p.code}` as Key);
}

function ProblemList({ problems, testid }: { problems: Problem[]; testid: string }): JSX.Element {
  const t = useT();
  return (
    <Banner kind="danger" testid={testid}>
      <ul>
        {problems.map((p, i) => (
          <li key={i}>{problemText(t, p)}</li>
        ))}
      </ul>
    </Banner>
  );
}

const hasProblem = (problems: Problem[], field: string): boolean => problems.some((p) => p.field === field);

const POLICY_KEY: Record<BackendPolicy, Key> = {
  shared: "mcp.policy.shared",
  exclusive: "mcp.policy.exclusive",
  "per-caller": "mcp.policy.per-caller",
};

function McpCard({ ctx, cfg, editable, apply }: CardProps): JSX.Element {
  const t = useT();
  const rows = authzRows(cfg.parsed);
  const backends = backendRows(cfg.parsed);
  const names = backendNames(cfg.parsed);
  const [editing, setEditing] = useState<{ name?: string } | undefined>();
  const [importing, setImporting] = useState(false);
  const [deleting, setDeleting] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const existing = editing?.name === undefined ? undefined : backends.find((b) => b.name === editing.name);

  const remove = async (name: string): Promise<void> => {
    setBusy(true);
    const err = await apply(backendDeleteEdits(rows, name));
    setBusy(false);
    setDeleting(undefined);
    if (err) ctx.toast(errorText(t, err), true);
    else ctx.toast(t("authz.deleted", { name }));
  };

  return (
    <section class="card" data-testid="mcp-servers">
      <div class="page-head">
        <div class="grow">
          <h2>{t("mcp.title")}</h2>
          <p class="hint">{t("mcp.hint")}</p>
        </div>
        {editable && (
          <div class="row">
            <button type="button" class="btn" onClick={() => setImporting(true)} data-testid="mcp-import">
              {t("mcp.import")}
            </button>
            <button type="button" class="btn primary" onClick={() => setEditing({})} data-testid="mcp-add">
              {t("mcp.add")}
            </button>
          </div>
        )}
      </div>
      {backends.length === 0 ? (
        <Empty>{t("mcp.empty")}</Empty>
      ) : (
        <div class="table-wrap">
          <table data-testid="mcp-table">
            <thead>
              <tr>
                <th scope="col">{t("col.name")}</th>
                <th scope="col">{t("col.transport")}</th>
                <th scope="col">{t("col.command")}</th>
                <th scope="col">{t("col.policy")}</th>
                <th scope="col">{t("mcp.grants")}</th>
                <th scope="col" />
              </tr>
            </thead>
            <tbody>
              {backends.map((b) => {
                const granted = grantedTags(rows, b.name);
                return (
                  <tr key={b.name} data-testid={`backend-${b.name}`}>
                    <td>
                      <strong>{b.name}</strong>
                    </td>
                    <td>
                      <Badge kind="info">{b.transport}</Badge>
                    </td>
                    <td class="mono">{b.transport === "http" ? b.url : b.command.join(" ")}</td>
                    <td>{b.policy}</td>
                    <td>
                      <div class="row">
                        {granted.length === 0 ? (
                          <span class="hint">{t("mcp.noGrants")}</span>
                        ) : (
                          granted.map((tag) => (
                            <Badge key={tag} kind="info">
                              {tag}
                            </Badge>
                          ))
                        )}
                      </div>
                    </td>
                    <td>
                      {editable && (
                        <div class="row end">
                          <button type="button" class="btn small" onClick={() => setEditing({ name: b.name })} data-testid={`backend-edit-${b.name}`}>
                            {t("common.edit")}
                          </button>
                          <button type="button" class="btn small danger" onClick={() => setDeleting(b.name)} data-testid={`backend-delete-${b.name}`}>
                            {t("authz.delete")}
                          </button>
                        </div>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {editing && (
        <BackendDialog
          existing={existing}
          rows={rows}
          names={names}
          onSave={async (edits) => {
            const err = await apply(edits);
            if (!err) setEditing(undefined);
            return err;
          }}
          onClose={() => setEditing(undefined)}
        />
      )}
      {importing && (
        <ImportDialog
          rows={rows}
          names={names}
          onSave={async (edits) => {
            const err = await apply(edits);
            if (!err) setImporting(false);
            return err;
          }}
          onClose={() => setImporting(false)}
        />
      )}
      {deleting && (
        <Confirm
          title={t("mcp.deleteTitle", { name: deleting })}
          body={
            <>
              <p>{t("mcp.deleteBody")}</p>
              {exactGrants(rows, deleting).length > 0 && <p class="hint">{t("mcp.deleteGrants", { tags: exactGrants(rows, deleting).join(", ") })}</p>}
            </>
          }
          confirmLabel={t("authz.delete")}
          danger
          busy={busy}
          onCancel={() => setDeleting(undefined)}
          onConfirm={() => void remove(deleting)}
        />
      )}
    </section>
  );
}

function formFromRow(row: BackendRow, rows: readonly AuthzRow[]): BackendForm {
  return {
    name: row.name,
    transport: row.transport === "http" ? "http" : "stdio",
    command: row.command.join("\n"),
    url: row.url,
    cwd: row.cwd,
    policy: row.policy || "shared",
    idleTimeout: row.idleTimeout,
    grants: exactGrants(rows, row.name),
    newTag: "",
    favorite: row.favorite,
  };
}

const EMPTY_BACKEND_FORM: BackendForm = {
  name: "",
  transport: "stdio",
  command: "",
  url: "",
  cwd: "",
  policy: "shared",
  idleTimeout: "",
  grants: [],
  newTag: "",
  favorite: false,
};

/** The tags a form grants: the checked ones plus the new tag, when it is valid. */
function tagsOf(grants: readonly string[], newTag: string): string[] {
  const tags = [...grants];
  const tag = newTag.trim();
  if (tag !== "" && TAG_KEY.test(tag) && !tags.includes(tag)) tags.push(tag);
  return tags;
}

/** Checkboxes for the existing tags, and a text box for a new one. Shared by the backend form and the import. */
function GrantFields({ rows, grants, newTag, onGrants, onNewTag, invalid }: {
  rows: readonly AuthzRow[];
  grants: string[];
  newTag: string;
  onGrants: (next: string[]) => void;
  onNewTag: (next: string) => void;
  invalid: boolean;
}): JSX.Element {
  const t = useT();
  const tags = rows.map((r) => r.tag);
  return (
    <fieldset>
      <legend>{t("mcp.grant")}</legend>
      <p class="hint">{t("mcp.grantHint")}</p>
      {tags.length === 0 && <p class="hint">{t("mcp.noTags")}</p>}
      {tags.map((tag) => (
        <label class="check" key={tag}>
          <input
            type="checkbox"
            checked={grants.includes(tag)}
            onChange={(e) => {
              const on = (e.currentTarget as HTMLInputElement).checked;
              onGrants(on ? [...grants.filter((g) => g !== tag), tag] : grants.filter((g) => g !== tag));
            }}
            data-testid={`grant-${tag}`}
          />
          <span class="mono">{tag}</span>
        </label>
      ))}
      <Field label={t("mcp.newTag")} hint={t("mcp.newTagHint")}>
        <input
          type="text"
          value={newTag}
          aria-invalid={invalid}
          autocomplete="off"
          onInput={(e) => onNewTag((e.currentTarget as HTMLInputElement).value)}
          data-testid="mcp-newtag"
        />
      </Field>
    </fieldset>
  );
}

function BackendDialog({ existing, rows, names, onSave, onClose }: {
  existing?: BackendRow;
  rows: readonly AuthzRow[];
  names: readonly string[];
  onSave: (edits: ConfigPatchEdit[]) => Promise<ActionError | undefined>;
  onClose: () => void;
}): JSX.Element {
  const t = useT();
  const [form, setForm] = useState<BackendForm>(() => (existing ? formFromRow(existing, rows) : EMPTY_BACKEND_FORM));
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const patch = (p: Partial<BackendForm>): void => {
    setTouched(true);
    setForm((f) => ({ ...f, ...p }));
  };
  const check = checkBackendForm(form, names, existing?.name);
  const problems = check.ok ? [] : check.problems;

  const save = async (e: Event): Promise<void> => {
    e.preventDefault();
    const c = checkBackendForm(form, names, existing?.name);
    if (!c.ok) return;
    setBusy(true);
    setErr(undefined);
    const edits = backendEdits(authzState(rows), c.name, c.value, c.tags, true);
    const failed = await onSave(edits);
    setBusy(false);
    if (failed) setErr(failed);
  };

  return (
    <Dialog title={existing ? t("mcp.editTitle", { name: existing.name }) : t("mcp.addTitle")} onClose={onClose} testid="mcp-dialog">
      <form onSubmit={(e) => void save(e)}>
        <Field label={t("mcp.name")} hint={existing ? undefined : t("mcp.nameHint")}>
          {existing ? (
            <input type="text" value={existing.name} disabled data-testid="mcp-name" />
          ) : (
            <input
              type="text"
              value={form.name}
              autocomplete="off"
              aria-invalid={touched && hasProblem(problems, "name")}
              onInput={(e) => patch({ name: (e.currentTarget as HTMLInputElement).value })}
              data-testid="mcp-name"
            />
          )}
        </Field>
        <Field label={t("mcp.transport")}>
          <select value={form.transport} onChange={(e) => patch({ transport: (e.currentTarget as HTMLSelectElement).value as Transport })} data-testid="mcp-transport">
            <option value="stdio">{t("mcp.transport.stdio")}</option>
            <option value="http">{t("mcp.transport.http")}</option>
          </select>
        </Field>
        {form.transport === "stdio" ? (
          <Field label={t("mcp.command")} hint={t("mcp.commandHint")}>
            <textarea
              rows={3}
              value={form.command}
              spellcheck={false}
              aria-invalid={touched && hasProblem(problems, "command")}
              onInput={(e) => patch({ command: (e.currentTarget as HTMLTextAreaElement).value })}
              data-testid="mcp-command"
            />
          </Field>
        ) : (
          <Field label={t("mcp.url")} hint={t("mcp.urlHint")}>
            <input
              type="text"
              value={form.url}
              spellcheck={false}
              autocomplete="off"
              aria-invalid={touched && hasProblem(problems, "url")}
              onInput={(e) => patch({ url: (e.currentTarget as HTMLInputElement).value })}
              data-testid="mcp-url"
            />
          </Field>
        )}
        <Field label={t("mcp.cwd")}>
          <input type="text" value={form.cwd} spellcheck={false} onInput={(e) => patch({ cwd: (e.currentTarget as HTMLInputElement).value })} data-testid="mcp-cwd" />
        </Field>
        <Field label={t("mcp.policy")} hint={t("mcp.policyHint")}>
          <select value={form.policy} onChange={(e) => patch({ policy: (e.currentTarget as HTMLSelectElement).value })} data-testid="mcp-policy">
            {BACKEND_POLICIES.map((p) => (
              <option key={p} value={p}>
                {t(POLICY_KEY[p])}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t("mcp.idle")} hint={t("mcp.idleHint")}>
          <input
            type="text"
            value={form.idleTimeout}
            autocomplete="off"
            aria-invalid={touched && hasProblem(problems, "idle")}
            onInput={(e) => patch({ idleTimeout: (e.currentTarget as HTMLInputElement).value })}
            data-testid="mcp-idle"
          />
        </Field>
        <GrantFields
          rows={rows}
          grants={form.grants}
          newTag={form.newTag}
          onGrants={(grants) => patch({ grants })}
          onNewTag={(newTag) => patch({ newTag })}
          invalid={touched && hasProblem(problems, "tag")}
        />
        {touched && problems.length > 0 && <ProblemList problems={problems} testid="mcp-problems" />}
        {err && <ErrorBanner error={err} />}
        <div class="row end" style={{ marginTop: "12px" }}>
          <button type="button" class="btn" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button type="submit" class="btn primary" disabled={!check.ok || busy} data-testid="mcp-save">
            {t("config.save")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function ImportDialog({ rows, names, onSave, onClose }: {
  rows: readonly AuthzRow[];
  names: readonly string[];
  onSave: (edits: ConfigPatchEdit[]) => Promise<ActionError | undefined>;
  onClose: () => void;
}): JSX.Element {
  const t = useT();
  const [text, setText] = useState("");
  const [result, setResult] = useState<ImportResult | undefined>();
  const [grants, setGrants] = useState<string[]>([]);
  const [newTag, setNewTag] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const entries = result?.entries ?? [];
  const tagText = newTag.trim();
  const tagOk = tagText === "" || TAG_KEY.test(tagText);
  const tags = tagsOf(grants, newTag);
  // Edits are built from one working copy of the grants, so several servers granted the same tag compose.
  const state = authzState(rows);
  const edits = entries.flatMap((e) => backendEdits(state, e.name, e.value, tags, false));
  const tooMany = edits.length > MAX_EDITS;
  const canImport = entries.length > 0 && tagOk && !tooMany;

  const save = async (): Promise<void> => {
    if (!canImport) return;
    setBusy(true);
    setErr(undefined);
    const failed = await onSave(edits);
    setBusy(false);
    if (failed) setErr(failed);
  };

  return (
    <Dialog title={t("mcp.import.title")} onClose={onClose} testid="mcp-import-dialog">
      <p class="hint">{t("mcp.import.intro")}</p>
      <Field label={t("mcp.import.paste")}>
        <textarea
          rows={8}
          value={text}
          spellcheck={false}
          onInput={(e) => {
            setText((e.currentTarget as HTMLTextAreaElement).value);
            setResult(undefined);
          }}
          data-testid="mcp-import-text"
        />
      </Field>
      <div class="row end">
        <button type="button" class="btn" onClick={() => setResult(parseMcpImport(text))} disabled={text.trim() === ""} data-testid="mcp-import-parse">
          {t("mcp.import.preview")}
        </button>
      </div>
      {result && (
        <div data-testid="mcp-import-preview">
          {result.problems.length > 0 && (
            <Banner kind="danger" testid="mcp-import-problems">
              <ul>
                {result.problems.map((p, i) => (
                  <li key={i}>
                    {p.name !== "" && <code>{p.name}</code>} {p.name !== "" && "："}
                    {t(`mcp.err.${p.code}` as Key, p.detail === undefined ? undefined : { detail: p.detail })}
                  </li>
                ))}
              </ul>
            </Banner>
          )}
          {entries.length === 0 ? (
            <Empty>{t("mcp.import.nothing")}</Empty>
          ) : (
            <>
              {result.problems.some((p) => p.name !== "") && <p class="hint">{t("mcp.import.skipped", { n: result.problems.filter((p) => p.name !== "").length })}</p>}
              <div class="table-wrap">
                <table data-testid="mcp-import-table">
                  <thead>
                    <tr>
                      <th scope="col">{t("col.name")}</th>
                      <th scope="col">{t("col.transport")}</th>
                      <th scope="col">{t("col.command")}</th>
                      <th scope="col">{t("col.policy")}</th>
                      <th scope="col" />
                    </tr>
                  </thead>
                  <tbody>
                    {entries.map((e) => (
                      <tr key={e.name} data-testid={`import-${e.name}`}>
                        <td>
                          <strong>{e.name}</strong>
                        </td>
                        <td>
                          <Badge kind="info">{e.value.transport}</Badge>
                        </td>
                        <td class="mono">{e.value.transport === "http" ? e.value.url : (e.value.command ?? []).join(" ")}</td>
                        <td>{e.value.policy}</td>
                        <td>
                          <Badge kind={names.includes(e.name) ? "warn" : "ok"}>{names.includes(e.name) ? t("mcp.import.replace") : t("mcp.import.new")}</Badge>
                          {e.warnings.map((w, i) => (
                            <p class="hint" key={i}>
                              {t(`mcp.warn.${w.code}` as Key, { names: w.detail ?? "" })}
                            </p>
                          ))}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <GrantFields rows={rows} grants={grants} newTag={newTag} onGrants={setGrants} onNewTag={setNewTag} invalid={!tagOk} />
              {tooMany && <Banner kind="danger" testid="mcp-import-too-many">{t("mcp.err.tooMany")}</Banner>}
            </>
          )}
        </div>
      )}
      {err && <ErrorBanner error={err} />}
      <div class="row end" style={{ marginTop: "12px" }}>
        <button type="button" class="btn" onClick={onClose}>
          {t("common.cancel")}
        </button>
        <button type="button" class="btn primary" disabled={!canImport || busy} onClick={() => void save()} data-testid="mcp-import-confirm">
          {t("mcp.import.count", { n: entries.length })}
        </button>
      </div>
    </Dialog>
  );
}

function BuiltinCard({ ctx, cfg, editable, apply }: CardProps): JSX.Element {
  const t = useT();
  const locale = useLocale();
  const disabled = disabledTools(cfg.parsed);
  const [busy, setBusy] = useState(false);

  const toggle = async (tool: string, on: boolean, el: HTMLInputElement): Promise<void> => {
    // The box shows the file's state until the write lands; the reload then moves it.
    el.checked = toolEnabled(cfg.parsed, tool);
    const edits = toolToggleEdits(disabled, tool, on);
    if (edits.length === 0) return;
    setBusy(true);
    const err = await apply(edits);
    setBusy(false);
    if (err) ctx.toast(errorText(t, err), true);
  };

  return (
    <section class="card" data-testid="builtin-tools">
      <div class="page-head">
        <div class="grow">
          <h2>{t("tools.title")}</h2>
          <p class="hint">{t("tools.hint")}</p>
        </div>
      </div>
      <div class="table-wrap">
        <table data-testid="builtin-table">
          <thead>
            <tr>
              <th scope="col">{t("col.name")}</th>
              <th scope="col">{t("tools.desc")}</th>
              <th scope="col" />
            </tr>
          </thead>
          <tbody>
            {BUILTIN_TOOLS.map((tool) => {
              const on = toolEnabled(cfg.parsed, tool.name);
              return (
                <tr key={tool.name} data-testid={`tool-row-${tool.name}`}>
                  <td class="mono">{tool.name}</td>
                  <td>{locale === "en" ? tool.en : tool.zh}</td>
                  <td>
                    <label class="row">
                      <input
                        type="checkbox"
                        role="switch"
                        checked={on}
                        disabled={!editable || busy}
                        aria-label={t("tools.label", { name: tool.name })}
                        onChange={(e) => void toggle(tool.name, (e.currentTarget as HTMLInputElement).checked, e.currentTarget as HTMLInputElement)}
                        data-testid={`tool-${tool.name}`}
                      />
                      <span>{on ? t("tools.on") : t("tools.off")}</span>
                    </label>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function AuthzCard({ ctx, cfg, editable, apply }: CardProps): JSX.Element {
  const t = useT();
  const rows = authzRows(cfg.parsed);
  const cwd = cwdPolicy(cfg.parsed);
  const names = backendNames(cfg.parsed);
  const [editing, setEditing] = useState<{ tag?: string } | undefined>();
  const [deleting, setDeleting] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);

  const remove = async (tag: string): Promise<void> => {
    setBusy(true);
    const err = await apply(authzDeleteEdits(tag));
    setBusy(false);
    setDeleting(undefined);
    if (err) ctx.toast(errorText(t, err), true);
    else ctx.toast(t("authz.deleted", { name: tag }));
  };

  return (
    <section class="card" data-testid="authz-view">
      <div class="page-head">
        <div class="grow">
          <h2>{t("authz.tools")}</h2>
          <p class="hint">{t("authz.toolsHint")}</p>
        </div>
        <div class="row">
          <button type="button" class="btn" onClick={() => ctx.go("config")}>
            {t("authz.editInConfig")}
          </button>
          {editable && (
            <button type="button" class="btn primary" onClick={() => setEditing({})} data-testid="authz-add">
              {t("authz.add")}
            </button>
          )}
        </div>
      </div>
      {rows.length === 0 ? (
        <Empty>{t("authz.noAuthz")}</Empty>
      ) : (
        <div class="table-wrap">
          <table data-testid="authz-table">
            <thead>
              <tr>
                <th scope="col">{t("col.tag")}</th>
                <th scope="col">{t("col.tools")}</th>
                <th scope="col">{t("col.backends")}</th>
                <th scope="col" />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.tag} data-testid={`authz-${r.tag}`}>
                  <td>
                    <Badge kind="info">{r.tag}</Badge>
                  </td>
                  <td class="mono">{r.tools.join(", ") || "—"}</td>
                  <td class="mono">{r.backends.join(", ") || "—"}</td>
                  <td>
                    {editable && (
                      <div class="row end">
                        <button type="button" class="btn small" onClick={() => setEditing({ tag: r.tag })} data-testid={`authz-edit-${r.tag}`}>
                          {t("common.edit")}
                        </button>
                        <button type="button" class="btn small danger" onClick={() => setDeleting(r.tag)} data-testid={`authz-delete-${r.tag}`}>
                          {t("authz.delete")}
                        </button>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <h3 style={{ marginTop: "12px" }}>{t("authz.cwd")}</h3>
      <p>
        {t("authz.cwdAllow")}: <span class="mono">{cwd.allow.join(", ") || t("authz.cwdAllowAll")}</span>
      </p>
      <p>
        {t("authz.cwdDeny")}: <span class="mono">{cwd.deny.join(", ") || "—"}</span>
      </p>
      <button type="button" class="btn" onClick={() => ctx.go("settings", { section: "workdirs" })} data-testid="authz-to-workdirs">
        {t("authz.editWorkdirs")}
      </button>
      {editing && (
        <AuthzDialog
          original={editing.tag}
          rows={rows}
          names={names}
          onSave={async (edits) => {
            const err = await apply(edits);
            if (!err) setEditing(undefined);
            return err;
          }}
          onClose={() => setEditing(undefined)}
        />
      )}
      {deleting && (
        <Confirm
          title={t("authz.tagDeleteTitle", { tag: deleting })}
          body={<p>{t("authz.tagDeleteBody")}</p>}
          confirmLabel={t("authz.delete")}
          danger
          busy={busy}
          onCancel={() => setDeleting(undefined)}
          onConfirm={() => void remove(deleting)}
        />
      )}
    </section>
  );
}

function AuthzDialog({ original, rows, names, onSave, onClose }: {
  original?: string;
  rows: readonly AuthzRow[];
  names: readonly string[];
  onSave: (edits: ConfigPatchEdit[]) => Promise<ActionError | undefined>;
  onClose: () => void;
}): JSX.Element {
  const t = useT();
  const locale = useLocale();
  const current = original === undefined ? undefined : rows.find((r) => r.tag === original);
  const [tag, setTag] = useState(original ?? "");
  const [builtin, setBuiltin] = useState<string[]>(() => (current?.tools ?? []).filter(isBuiltinTool));
  const [extraTools, setExtraTools] = useState(() => (current?.tools ?? []).filter((x) => !isBuiltinTool(x)).join("\n"));
  const [backends, setBackends] = useState<string[]>(() => (current?.backends ?? []).filter((b) => names.includes(b)));
  const [extraBackends, setExtraBackends] = useState(() => (current?.backends ?? []).filter((b) => !names.includes(b)).join("\n"));
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const form: AuthzForm = { original, tag, builtin, extraTools, backends, extraBackends };
  const check = checkAuthzForm(form, rows.map((r) => r.tag));
  const problems = check.ok ? [] : check.problems;

  const save = async (e: Event): Promise<void> => {
    e.preventDefault();
    const c = checkAuthzForm(form, rows.map((r) => r.tag));
    if (!c.ok) return;
    setBusy(true);
    setErr(undefined);
    const failed = await onSave(authzSaveEdits(original, c.tag, c.tools, c.backends));
    setBusy(false);
    if (failed) setErr(failed);
  };

  return (
    <Dialog title={original === undefined ? t("authz.addTitle") : t("authz.editTitle", { tag: original })} onClose={onClose} testid="authz-dialog">
      <form onSubmit={(e) => void save(e)}>
        <Field label={t("authz.tagKey")} hint={t("authz.tagHint")}>
          <input
            type="text"
            value={tag}
            autocomplete="off"
            spellcheck={false}
            aria-invalid={touched && hasProblem(problems, "tag")}
            onInput={(e) => {
              setTouched(true);
              setTag((e.currentTarget as HTMLInputElement).value);
            }}
            data-testid="authz-tag"
          />
        </Field>
        <fieldset>
          <legend>{t("authz.builtinTools")}</legend>
          <div class="check-grid">
            {BUILTIN_TOOLS.map((tool) => (
              <label class="check" key={tool.name}>
                <input
                  type="checkbox"
                  checked={builtin.includes(tool.name)}
                  onChange={(e) => {
                    setTouched(true);
                    const on = (e.currentTarget as HTMLInputElement).checked;
                    setBuiltin((b) => (on ? [...b.filter((x) => x !== tool.name), tool.name] : b.filter((x) => x !== tool.name)));
                  }}
                  data-testid={`authz-tool-${tool.name}`}
                />
                <span>
                  <span class="mono">{tool.name}</span> <span class="hint">{locale === "en" ? tool.en : tool.zh}</span>
                </span>
              </label>
            ))}
          </div>
          <Field label={t("authz.extraTools")} hint={t("authz.extraToolsHint")}>
            <textarea
              rows={2}
              value={extraTools}
              spellcheck={false}
              aria-invalid={touched && hasProblem(problems, "extraTools")}
              onInput={(e) => {
                setTouched(true);
                setExtraTools((e.currentTarget as HTMLTextAreaElement).value);
              }}
              data-testid="authz-extra-tools"
            />
          </Field>
        </fieldset>
        <fieldset>
          <legend>{t("authz.backends")}</legend>
          {names.length === 0 ? (
            <p class="hint">{t("authz.noBackends")}</p>
          ) : (
            <div class="check-grid">
              {names.map((name) => (
                <label class="check" key={name}>
                  <input
                    type="checkbox"
                    checked={backends.includes(name)}
                    onChange={(e) => {
                      setTouched(true);
                      const on = (e.currentTarget as HTMLInputElement).checked;
                      setBackends((b) => (on ? [...b.filter((x) => x !== name), name] : b.filter((x) => x !== name)));
                    }}
                    data-testid={`authz-backend-${name}`}
                  />
                  <span class="mono">{name}</span>
                </label>
              ))}
            </div>
          )}
          <Field label={t("authz.extraBackends")} hint={t("authz.extraBackendsHint")}>
            <textarea
              rows={2}
              value={extraBackends}
              spellcheck={false}
              aria-invalid={touched && hasProblem(problems, "extraBackends")}
              onInput={(e) => {
                setTouched(true);
                setExtraBackends((e.currentTarget as HTMLTextAreaElement).value);
              }}
              data-testid="authz-extra-backends"
            />
          </Field>
        </fieldset>
        {touched && problems.length > 0 && <ProblemList problems={problems} testid="authz-problems" />}
        {err && <ErrorBanner error={err} />}
        <div class="row end" style={{ marginTop: "12px" }}>
          <button type="button" class="btn" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button type="submit" class="btn primary" disabled={!check.ok || busy} data-testid="authz-save">
            {t("config.save")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

