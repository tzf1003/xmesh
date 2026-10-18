import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Confirm, Dialog, Field } from "../../components";
import { useT } from "../../i18n";
import { githubTokenValid } from "../../lib/ci";
import type { CiAccount, CiOverview } from "../../../shared/control";
import type { ActionError } from "../../../shared/ipc";
import { CiError } from "./common";

const DEFAULT_HOST = "github.com";

/** The GitHub accounts whose tokens the kernel keeps, with add, replace-token and delete. */
export function Accounts({ ctx, overview, reload }: { ctx: PageContext; overview: CiOverview; reload(): void }): JSX.Element {
  const t = useT();
  const [dialog, setDialog] = useState<{ replace?: CiAccount } | undefined>();
  const [removing, setRemoving] = useState<CiAccount | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();
  const [busy, setBusy] = useState(false);

  const remove = async (a: CiAccount): Promise<void> => {
    setBusy(true);
    setErr(undefined);
    const r = await getApi().ci({ op: "account.delete", id: a.id });
    setBusy(false);
    setRemoving(undefined);
    if (!r.ok) return setErr(r.error);
    ctx.toast(t("ci.accountRemoved", { login: a.login }));
    reload();
  };

  return (
    <section class="card" data-testid="ci-accounts">
      <div class="row">
        <h2 class="grow">{t("ci.accounts")}</h2>
        <button type="button" class="btn" onClick={() => setDialog({})} data-testid="ci-account-add">
          {t("ci.accountAdd")}
        </button>
      </div>
      <p class="hint">{t("ci.accountsIntro")}</p>
      {err && <CiError error={err} testid="ci-account-error" />}
      {overview.accounts.length === 0 ? (
        <p class="hint" data-testid="ci-accounts-none">
          {t("ci.accountsNone")}
        </p>
      ) : (
        <ul class="plain">
          {overview.accounts.map((a) => (
            <li key={a.id} class="row ci-account" data-testid={`ci-account-${a.id}`}>
              <strong>{a.login}</strong>
              {a.host !== DEFAULT_HOST && <span class="hint">{a.host}</span>}
              <span class="mono hint grow">{a.token_hint}</span>
              <button type="button" class="btn small" onClick={() => setDialog({ replace: a })} data-testid={`ci-account-replace-${a.id}`}>
                {t("ci.accountReplace")}
              </button>
              <button type="button" class="btn small danger" onClick={() => setRemoving(a)} data-testid={`ci-account-delete-${a.id}`}>
                {t("ci.accountDelete")}
              </button>
            </li>
          ))}
        </ul>
      )}
      {dialog && (
        <AccountDialog
          ctx={ctx}
          replace={dialog.replace}
          onClose={() => setDialog(undefined)}
          onSaved={() => {
            setDialog(undefined);
            reload();
          }}
        />
      )}
      {removing && (
        <Confirm
          title={t("ci.accountDeleteTitle", { login: removing.login })}
          body={<p>{t("ci.accountDeleteBody")}</p>}
          confirmLabel={t("ci.accountDelete")}
          danger
          busy={busy}
          onConfirm={() => void remove(removing)}
          onCancel={() => setRemoving(undefined)}
        />
      )}
    </section>
  );
}

/**
 * Adds an account or, with `replace`, gives an existing one a new token. The token is cleared from the page as soon as
 * the request is sent; the kernel never returns it.
 */
export function AccountDialog({
  ctx,
  replace,
  onClose,
  onSaved,
}: {
  ctx: PageContext;
  replace?: CiAccount;
  onClose(): void;
  onSaved(account: CiAccount): void;
}): JSX.Element {
  const t = useT();
  const [token, setToken] = useState("");
  const [host, setHost] = useState(replace?.host ?? DEFAULT_HOST);
  const [err, setErr] = useState<ActionError | undefined>();
  const [busy, setBusy] = useState(false);
  const valid = githubTokenValid(token);

  const save = async (): Promise<void> => {
    if (!valid || busy) return;
    setBusy(true);
    setErr(undefined);
    const hostArg = host.trim().toLowerCase();
    const r = await getApi().ci({ op: "account.create", token: token.trim(), ...(hostArg !== DEFAULT_HOST && hostArg !== "" && { host: hostArg }) });
    setToken("");
    setBusy(false);
    if (!r.ok) return setErr(r.error);
    ctx.toast(t("ci.accountAdded", { login: r.data.login }));
    onSaved(r.data);
  };

  return (
    <Dialog title={replace ? `${t("ci.accountReplace")} · ${replace.login}` : t("ci.accountAddTitle")} onClose={onClose} testid="ci-account-dialog">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <p class="hint">{t("ci.tokenHelp")}</p>
        <div class="row">
          <button type="button" class="btn" onClick={() => void getApi().openExternal("githubToken")} data-testid="ci-token-open">
            {t("ci.tokenOpen")}
          </button>
        </div>
        <Field label={t("ci.token")}>
          <input
            type="password"
            value={token}
            disabled={busy}
            onInput={(e) => setToken((e.currentTarget as HTMLInputElement).value)}
            aria-invalid={token !== "" && !valid}
            autocomplete="off"
            spellcheck={false}
            data-testid="ci-token"
          />
        </Field>
        {token !== "" && !valid && <p class="hint" data-testid="ci-token-invalid">{t("ci.tokenInvalid")}</p>}
        {!replace && (
          <Field label={t("ci.host")} hint={t("ci.hostHint")}>
            <input type="text" value={host} disabled={busy} onInput={(e) => setHost((e.currentTarget as HTMLInputElement).value)} autocomplete="off" spellcheck={false} data-testid="ci-host" />
          </Field>
        )}
        {err && <CiError error={err} />}
        <div class="row end" style={{ marginTop: "12px" }}>
          <button type="button" class="btn" onClick={onClose} data-testid="ci-account-cancel">
            {t("common.cancel")}
          </button>
          <button type="submit" class="btn primary" disabled={!valid || busy} data-testid="ci-account-save">
            {t("ci.save")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
