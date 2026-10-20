import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Badge, Banner, Confirm, Dialog, Field } from "../../components";
import { useT } from "../../i18n";
import { CI_MAX_CONCURRENCY, type CiBindingCreateArgs, parseCiRequest } from "../../../shared/ci";
import { needsPublicConfirm, parseLabelInput, parseRepo, registrationTokenValid } from "../../lib/ci";
import type { CiImportInspectResponse, CiOverview, CiRepo } from "../../../shared/control";
import type { ActionError } from "../../../shared/ipc";
import { AccountDialog } from "./Accounts";
import { CiError, Command } from "./common";

type Way = "account" | "token" | "import";

/** Binds a repository in one of three ways: pick it from an account, paste a registration token, or take over a runner directory. */
export function AddBinding({ ctx, overview, onClose, onCreated }: { ctx: PageContext; overview: CiOverview; onClose(): void; onCreated(): void }): JSX.Element {
  const t = useT();
  const [way, setWay] = useState<Way>(overview.accounts.length > 0 ? "account" : "token");
  const [accountId, setAccountId] = useState(overview.accounts[0]?.id ?? "");
  const [picked, setPicked] = useState<string | undefined>();
  const [repoText, setRepoText] = useState("");
  const [regToken, setRegToken] = useState("");
  const [dir, setDir] = useState("");
  const [inspection, setInspection] = useState<CiImportInspectResponse | undefined>();
  const [concurrency, setConcurrency] = useState("1");
  const [labelsText, setLabelsText] = useState("");
  const [err, setErr] = useState<ActionError | undefined>();
  const [busy, setBusy] = useState(false);
  const [confirmPublic, setConfirmPublic] = useState(false);
  const [addingAccount, setAddingAccount] = useState(false);

  const labels = parseLabelInput(labelsText);
  const n = Number(concurrency);
  const concurrencyOk = way === "import" || (Number.isInteger(n) && n >= 1 && n <= CI_MAX_CONCURRENCY);
  const repo = parseRepo(repoText);

  const blocked = inspection !== undefined && (inspection.service_installed || inspection.running || inspection.ephemeral === true);
  const args = (confirm: boolean): CiBindingCreateArgs | undefined => {
    if (labels === undefined || !concurrencyOk) return undefined;
    const common = { ...(way !== "import" && { concurrency: n }), ...(labels.length > 0 && { labels }), ...(confirm && { confirm_public: true }) };
    if (way === "account") return picked !== undefined && accountId !== "" ? { target: picked, account: accountId, ...common } : undefined;
    if (way === "token") return repo !== undefined && registrationTokenValid(regToken) ? { target: repo, registration_token: regToken.trim(), ...common } : undefined;
    return inspection !== undefined && !blocked ? { import_dir: dir.trim(), ...common } : undefined;
  };
  const ready = args(false) !== undefined && parseCiRequest({ op: "binding.create", args: args(false) }) !== undefined;

  const create = async (confirm: boolean): Promise<void> => {
    const a = args(confirm);
    if (a === undefined || busy) return;
    setBusy(true);
    setErr(undefined);
    const r = await getApi().ci({ op: "binding.create", args: a });
    setBusy(false);
    setConfirmPublic(false);
    if (way === "token" && (r.ok || !needsPublicConfirm(r.error))) setRegToken("");
    if (r.ok) {
      ctx.toast(t("ci.created", { repo: r.data.target }));
      return onCreated();
    }
    if (needsPublicConfirm(r.error)) return setConfirmPublic(true);
    setErr(r.error);
  };

  const pickDir = async (): Promise<void> => {
    const r = await getApi().pickPath("runnerDir");
    if (r.ok && r.data.path) {
      setDir(r.data.path);
      setInspection(undefined);
    }
  };
  const inspect = async (): Promise<void> => {
    setBusy(true);
    setErr(undefined);
    const r = await getApi().ci({ op: "import.inspect", dir: dir.trim() });
    setBusy(false);
    if (!r.ok) {
      setInspection(undefined);
      return setErr(r.error);
    }
    setInspection(r.data);
  };

  const ways: { id: Way; label: "ci.way.account" | "ci.way.token" | "ci.way.import" }[] = [
    { id: "account", label: "ci.way.account" },
    { id: "token", label: "ci.way.token" },
    { id: "import", label: "ci.way.import" },
  ];
  return (
    <Dialog title={t("ci.add.title")} onClose={onClose} testid="ci-add-dialog">
      <div class="tabs" role="tablist" aria-label={t("ci.add.title")}>
        {ways.map((w) => (
          <button
            key={w.id}
            type="button"
            role="tab"
            class="tab"
            aria-selected={way === w.id}
            disabled={busy}
            data-testid={`ci-way-${w.id}`}
            onClick={() => {
              setWay(w.id);
              setErr(undefined);
            }}
          >
            {t(w.label)}
          </button>
        ))}
      </div>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void create(false);
        }}
      >
        {way === "account" && (
          <>
            {overview.accounts.length === 0 ? (
              <>
                <p class="hint" data-testid="ci-no-account">{t("ci.noAccount")}</p>
                <button type="button" class="btn" onClick={() => setAddingAccount(true)} data-testid="ci-add-account-inline">
                  {t("ci.accountAdd")}
                </button>
              </>
            ) : (
              <>
                <Field label={t("ci.account")}>
                  <select
                    value={accountId}
                    disabled={busy}
                    onChange={(e) => {
                      setAccountId((e.currentTarget as HTMLSelectElement).value);
                      setPicked(undefined);
                    }}
                    data-testid="ci-account-select"
                  >
                    {overview.accounts.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.login}
                      </option>
                    ))}
                  </select>
                </Field>
                <RepoPicker accountId={accountId} picked={picked} onPick={setPicked} disabled={busy} />
              </>
            )}
          </>
        )}
        {way === "token" && (
          <>
            <Field label={t("ci.repo")} hint={t("ci.repoHint")}>
              <input
                type="text"
                value={repoText}
                disabled={busy}
                onInput={(e) => setRepoText((e.currentTarget as HTMLInputElement).value)}
                aria-invalid={repoText !== "" && repo === undefined}
                autocomplete="off"
                spellcheck={false}
                data-testid="ci-repo"
              />
            </Field>
            {repoText !== "" && repo === undefined && <p class="hint" data-testid="ci-repo-invalid">{t("ci.repoInvalid")}</p>}
            <Field label={t("ci.regToken")} hint={t("ci.regTokenHelp")}>
              <input
                type="password"
                value={regToken}
                disabled={busy}
                onInput={(e) => setRegToken((e.currentTarget as HTMLInputElement).value)}
                aria-invalid={regToken !== "" && !registrationTokenValid(regToken)}
                autocomplete="off"
                spellcheck={false}
                data-testid="ci-reg-token"
              />
            </Field>
            {regToken !== "" && !registrationTokenValid(regToken) && <p class="hint">{t("ci.regTokenInvalid")}</p>}
          </>
        )}
        {way === "import" && (
          <>
            <Field label={t("ci.dir")} hint={t("ci.dirHint")}>
              <div class="row">
                <input
                  class="grow"
                  type="text"
                  value={dir}
                  disabled={busy}
                  onInput={(e) => {
                    setDir((e.currentTarget as HTMLInputElement).value);
                    setInspection(undefined);
                  }}
                  autocomplete="off"
                  spellcheck={false}
                  data-testid="ci-dir"
                />
                <button type="button" class="btn" disabled={busy} onClick={() => void pickDir()} data-testid="ci-dir-pick">
                  {t("ci.pickDir")}
                </button>
                <button type="button" class="btn" disabled={busy || dir.trim() === ""} onClick={() => void inspect()} data-testid="ci-dir-inspect">
                  {t("ci.inspect")}
                </button>
              </div>
            </Field>
            {inspection && (
              <div data-testid="ci-inspection">
                <p>{t("ci.inspected", { repo: inspection.target, name: inspection.runner_name })}</p>
                {inspection.service_installed && (
                  <Banner kind="warn" testid="ci-inspect-service">
                    {t("ci.inspectService")}
                    {inspection.service_hint && <Command text={inspection.service_hint} testid="ci-inspect-service-command" />}
                  </Banner>
                )}
                {inspection.running && <Banner kind="warn" testid="ci-inspect-running">{t("ci.inspectRunning")}</Banner>}
                {inspection.ephemeral === true && <Banner kind="danger" testid="ci-inspect-ephemeral">{t("ci.inspectEphemeral")}</Banner>}
              </div>
            )}
          </>
        )}
        {way !== "import" && (
          <Field label={t("ci.concurrency")} hint={t("ci.concurrencyHint")}>
            <input
              type="number"
              min={1}
              max={CI_MAX_CONCURRENCY}
              value={concurrency}
              disabled={busy}
              onInput={(e) => setConcurrency((e.currentTarget as HTMLInputElement).value)}
              aria-invalid={!concurrencyOk}
              data-testid="ci-concurrency"
            />
          </Field>
        )}
        <Field label={t("ci.labels")} hint={t("ci.labelsHint")}>
          <input
            type="text"
            value={labelsText}
            disabled={busy}
            onInput={(e) => setLabelsText((e.currentTarget as HTMLInputElement).value)}
            aria-invalid={labels === undefined}
            autocomplete="off"
            spellcheck={false}
            data-testid="ci-labels"
          />
        </Field>
        {labels === undefined && <p class="hint">{t("ci.labelsInvalid")}</p>}
        {err && <CiError error={err} />}
        <div class="row end" style={{ marginTop: "12px" }}>
          <button type="button" class="btn" onClick={onClose} data-testid="ci-add-cancel">
            {t("common.cancel")}
          </button>
          <button type="submit" class="btn primary" disabled={!ready || busy} data-testid="ci-create">
            {way === "import" ? t("ci.takeOver") : t("ci.create")}
          </button>
        </div>
      </form>
      {confirmPublic && (
        <Confirm
          title={t("ci.publicTitle")}
          body={<p>{t("ci.publicBody")}</p>}
          confirmLabel={t("ci.publicConfirm")}
          danger
          busy={busy}
          onConfirm={() => void create(true)}
          onCancel={() => setConfirmPublic(false)}
        />
      )}
      {addingAccount && (
        <AccountDialog
          ctx={ctx}
          onClose={() => setAddingAccount(false)}
          onSaved={(a) => {
            setAddingAccount(false);
            setAccountId(a.id);
          }}
        />
      )}
    </Dialog>
  );
}

/** The repositories an account may register runners for, searchable and paged. */
function RepoPicker({ accountId, picked, onPick, disabled }: { accountId: string; picked: string | undefined; onPick(repo: string): void; disabled: boolean }): JSX.Element {
  const t = useT();
  const [q, setQ] = useState("");
  const [repos, setRepos] = useState<CiRepo[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [state, setState] = useState<"loading" | "ready" | { error: ActionError }>("loading");
  const generation=useRef(0);

  const load = async (query: string, page?: string): Promise<void> => {
    const seq=++generation.current;
    setState("loading");
    const r = await getApi().ci({ op: "repos", id: accountId, ...(query !== "" && { q: query }), ...(page !== undefined && { page }) });
    if(seq!==generation.current) return;
    if (!r.ok) return setState({ error: r.error });
    setRepos((prev) => (page === undefined ? r.data.repos : [...prev, ...r.data.repos]));
    setNext(r.data.next_page);
    setState("ready");
  };
  useEffect(() => {
    generation.current++;
    setRepos([]);setNext(null);setState("loading");
    if (accountId === "") return;
    // The search runs shortly after typing stops, so each keystroke is not a request to GitHub.
    const timer = setTimeout(() => void load(q.trim()), q === "" ? 0 : 350);
    return () => {clearTimeout(timer);generation.current++;};
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accountId, q]);

  return (
    <div data-testid="ci-repo-picker">
      <Field label={t("ci.repoSearch")}>
        <input type="search" value={q} disabled={disabled} onInput={(e) => setQ((e.currentTarget as HTMLInputElement).value)} autocomplete="off" spellcheck={false} data-testid="ci-repo-search" />
      </Field>
      {typeof state === "object" && <CiError error={state.error} testid="ci-repos-error" />}
      {state === "ready" && repos.length === 0 && <p class="hint" data-testid="ci-repos-none">{t("ci.repoNone")}</p>}
      <ul class="plain ci-repos">
        {repos.map((r) => (
          <li key={r.full_name}>
            <label class="check">
              <input
                type="radio"
                name="ci-repo"
                checked={picked === r.full_name}
                disabled={disabled || r.binding !== undefined}
                onChange={() => onPick(r.full_name)}
                data-testid={`ci-repo-${r.full_name}`}
              />{" "}
              <span class="mono">{r.full_name}</span> <Badge kind={r.private ? "" : "warn"}>{r.private ? t("ci.repoPrivate") : t("ci.repoPublic")}</Badge>
              {r.binding !== undefined && <Badge kind="info">{t("ci.repoBound")}</Badge>}
            </label>
          </li>
        ))}
      </ul>
      {state === "loading" && <p class="hint">{t("common.loading")}</p>}
      {next !== null && state === "ready" && (
        <button type="button" class="btn small" disabled={disabled} onClick={() => void load(q.trim(), next)} data-testid="ci-repo-more">
          {t("ci.repoLoadMore")}
        </button>
      )}
    </div>
  );
}
