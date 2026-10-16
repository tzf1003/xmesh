import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Dialog, Empty, ErrorBanner, Field, NotSupported, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { errorText, useT } from "../i18n";
import { authzRows, cwdPolicy } from "../lib/config";
import { formatTime } from "../lib/format";
import { SCOPE_PRESETS, checkScopes, isBroadScope } from "../lib/scopes";
import { parseNodeName } from "../../shared/ipc";
import type { ActionError } from "../../shared/ipc";
import type { PatInfo, PatSecret } from "../../shared/control";

export function Authorization({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  return (
    <div class="page" data-testid="page-authz">
      <PageHead title={t("nav.authz")} sub={t("authz.sub")} />
      {ctx.can("pats") ? <Pats ctx={ctx} /> : <NotSupported what={t("authz.pats")} />}
      {ctx.can("config.read") ? <AuthzView ctx={ctx} /> : <NotSupported what={t("authz.tools")} />}
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
            {!list.enforced && <Banner kind="warn" testid="pats-open">{t("authz.notEnforced")}</Banner>}
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

function AuthzView({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const { resource } = useResource(() => getApi().getConfig(), [ctx.snapshot.selectedInstance]);
  return (
    <section class="card" data-testid="authz-view">
      <div class="page-head">
        <div class="grow">
          <h2>{t("authz.tools")}</h2>
          <p class="hint">{t("authz.toolsHint")}</p>
        </div>
        <button type="button" class="btn" onClick={() => ctx.go("config")}>
          {t("authz.editInConfig")}
        </button>
      </div>
      <ResourceView resource={resource}>
        {(cfg) => {
          const rows = authzRows(cfg.parsed);
          const cwd = cwdPolicy(cfg.parsed);
          return (
            <>
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
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((r) => (
                        <tr key={r.tag}>
                          <td>
                            <Badge kind="info">{r.tag}</Badge>
                          </td>
                          <td class="mono">{r.tools.join(", ") || "—"}</td>
                          <td class="mono">{r.backends.join(", ") || "—"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              <h3 style={{ marginTop: "12px" }}>{t("authz.cwd")}</h3>
              <p>
                {t("authz.cwdAllow")}: <span class="mono">{cwd.allow.join(", ") || "—"}</span>
              </p>
              <p>
                {t("authz.cwdDeny")}: <span class="mono">{cwd.deny.join(", ") || "—"}</span>
              </p>
            </>
          );
        }}
      </ResourceView>
    </section>
  );
}
