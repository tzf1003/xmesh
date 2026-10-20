import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Badge, Dialog, Field } from "../../components";
import { useT } from "../../i18n";
import { Icon } from "../../icons";
import { formatTime } from "../../lib/format";
import { CI_MAX_CONCURRENCY } from "../../../shared/ci";
import { RESULT_KEY, STATE_KEY, STEP_KEY, parseLabelInput, registrationTokenValid, stateKind, stepOf } from "../../lib/ci";
import type { CiAccount, CiBinding, CiInstance, CiOverview } from "../../../shared/control";
import type { ActionError } from "../../../shared/ipc";
import { AccountDialog } from "./Accounts";
import { CiError } from "./common";
import { bindingNeedsAttention } from "../module-model";
import { useModuleT } from "../module-messages";

interface Props {
  ctx: PageContext;
  overview: CiOverview;
  binding: CiBinding;
  reload(): void;
}

/** One bound repository: its settings, its runner instances, and what to press when an instance needs the user. */
export function BindingCard({ ctx, overview, binding: b, reload }: Props): JSX.Element {
  const t = useT();
  const m = useModuleT();
  const [managing, setManaging] = useState(false);
  const [dialog, setDialog] = useState<"edit" | "unbind" | "repair" | "token" | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();
  const [busy, setBusy] = useState(false);
  const account = overview.accounts.find((a) => a.id === b.account);

  const call = async (run: () => Promise<{ ok: true } | { ok: false; error: ActionError }>, done?: string): Promise<void> => {
    setBusy(true);
    setErr(undefined);
    const r = await run();
    setBusy(false);
    if (!r.ok) return setErr(r.error);
    if (done) ctx.toast(done);
    reload();
  };
  const restart = (): Promise<void> => call(() => getApi().ci({ op: "binding.restart", id: b.id }), t("ci.restarted", { repo: b.target }));
  const togglePause = (): Promise<void> => call(() => getApi().ci({ op: "binding.patch", id: b.id, patch: { paused: !b.paused } }));
  const source = b.source === "account" ? t("ci.src.account", { account: account?.login ?? b.account ?? "?" }) : b.source === "token" ? t("ci.src.token") : t("ci.src.import");

  const act = (i: CiInstance): void => {
    const step = stepOf(i);
    if (step === "restart") void restart();
    else if (step === "repair") {
      if (b.source === "account") void call(() => getApi().ci({ op: "binding.repair", id: b.id }));
      else setDialog("repair");
    } else if (step === "account") setDialog(account ? "token" : "repair");
  };

  return (
    <section class={`card ci-binding ${bindingNeedsAttention(b) ? "issue" : ""}`} data-testid={`ci-binding-${b.id}`}>
      <div class="module-repo-heading">
        <span class="module-repo-icon"><Icon name="folder"/></span>
        <div class="module-repo-heading-main"><div class="module-repo-title"><h2>{b.target}</h2>
          {b.public === true ? <Badge kind="warn">{t("ci.public")}</Badge> : b.public === false && <Badge>{m("private")}</Badge>}
          {b.paused && <Badge>{t("ci.paused")}</Badge>}{b.removing && <Badge kind="warn">{t("ci.removing")}</Badge>}
        </div><p>{source} · {m("instanceCount", { n: b.instances.length })}</p></div>
        <div class="module-repo-actions">
        <button type="button" class="btn small" disabled={busy || b.removing} onClick={() => void togglePause()} data-testid={`ci-pause-${b.id}`}>
          {b.paused ? t("ci.resume") : t("ci.pause")}
        </button>
        <button type="button" class="module-icon-button" disabled={busy || b.removing} onClick={() => setManaging(!managing)} aria-label={`${m("manage")} ${b.target}`} aria-expanded={managing} data-testid={`ci-manage-${b.id}`}><Icon name="sliders"/></button>
        </div>
      </div>
      <div class="module-repo-tags"><span>{m("tagsTitle")}</span>{b.source !== "import" && <Badge kind="info">xmesh</Badge>}{b.labels.filter((label) => b.source === "import" || label !== "xmesh").map((label) => <Badge key={label}>{label}</Badge>)}{b.source === "import" && b.labels.length === 0 && <span class="hint">—</span>}</div>
      <div class="module-binding-management" hidden={!managing}>
        <span>{t("ci.concurrency")} {b.concurrency}</span>
        <button type="button" class="btn small" disabled={busy || b.removing} onClick={() => void restart()} data-testid={`ci-restart-${b.id}`}>
          {t("ci.restartAll")}
        </button>
        <button type="button" class="btn small" disabled={busy || b.removing} onClick={() => setDialog("edit")} data-testid={`ci-edit-${b.id}`}>
          {t("ci.edit")}
        </button>
        <button type="button" class="btn small danger" disabled={busy || b.removing} onClick={() => setDialog("unbind")} data-testid={`ci-unbind-${b.id}`}>
          {t("ci.unbind")}
        </button>
      </div>
      {err && <CiError error={err} />}
      <ul class="plain" aria-label={t("ci.instances")}>
        {b.instances.map((i) => {
          const step = stepOf(i);
          return (
            <li key={i.index} class="ci-instance" data-testid={`ci-instance-${b.id}-${i.index}`}>
              <div class="module-instance-row">
                <span class="mono">{i.name}</span>
                <span class={`module-state ${stateKind(i.state)}`}>
                  <span data-testid={`ci-state-${b.id}-${i.index}`} data-state={i.state}>
                    {t(STATE_KEY[i.state])}
                  </span>
                </span>
                <div class="module-instance-job"><strong>{i.job ? t("ci.instance.job", { name: i.job.name }) : m("waitingJob")}</strong><small>{i.job ? formatTime(i.job.started_at) : i.last_result ? `${t("ci.instance.last", { name: i.last_result.name, result: t(RESULT_KEY[i.last_result.result]) })} · ${formatTime(i.last_result.finished_at)}` : m("stateSince", { time: formatTime(i.since) })}</small></div>
                {step && (
                  <button type="button" class="btn small primary" disabled={busy} onClick={() => act(i)} data-testid={`ci-step-${b.id}-${i.index}`}>
                    {t(STEP_KEY[step])}
                  </button>
                )}
              </div>
              {i.problem && <p class="hint ci-problem">{i.problem.message}</p>}
              {i.restarts > 0 && <p class="hint">{t("ci.instance.restarts", { n: i.restarts })}</p>}
            </li>
          );
        })}
      </ul>
      {dialog === "edit" && (
        <EditDialog
          ctx={ctx}
          binding={b}
          accounts={overview.accounts}
          onClose={() => setDialog(undefined)}
          onSaved={() => {
            setDialog(undefined);
            reload();
          }}
        />
      )}
      {dialog === "unbind" && (
        <UnbindDialog
          ctx={ctx}
          binding={b}
          onClose={() => setDialog(undefined)}
          onDone={() => {
            setDialog(undefined);
            reload();
          }}
        />
      )}
      {dialog === "repair" && (
        <RepairDialog
          ctx={ctx}
          binding={b}
          onClose={() => setDialog(undefined)}
          onDone={() => {
            setDialog(undefined);
            reload();
          }}
        />
      )}
      {dialog === "token" && account && (
        <AccountDialog
          ctx={ctx}
          replace={account as CiAccount}
          onClose={() => setDialog(undefined)}
          onSaved={() => {
            setDialog(undefined);
            void call(() => getApi().ci({ op: "binding.repair", id: b.id }));
          }}
        />
      )}
    </section>
  );
}

function EditDialog({
  ctx,
  binding: b,
  accounts,
  onClose,
  onSaved,
}: {
  ctx: PageContext;
  binding: CiBinding;
  accounts: CiAccount[];
  onClose(): void;
  onSaved(): void;
}): JSX.Element {
  const t = useT();
  const [concurrency, setConcurrency] = useState(String(b.concurrency));
  const [labelsText, setLabelsText] = useState(b.labels.join(", "));
  const [account, setAccount] = useState(b.account ?? "");
  const [err, setErr] = useState<ActionError | undefined>();
  const [busy, setBusy] = useState(false);
  const labels = parseLabelInput(labelsText);
  const n = Number(concurrency);
  const concurrencyOk = Number.isInteger(n) && n >= 1 && n <= CI_MAX_CONCURRENCY;
  const single = b.source === "import";
  const patch: { concurrency?: number; labels?: string[]; account?: string } = {};
  if (concurrencyOk && n !== b.concurrency) patch.concurrency = n;
  if (labels !== undefined && labels.join(",") !== b.labels.join(",")) patch.labels = labels;
  if (b.source === "account" && account !== "" && account !== b.account) patch.account = account;
  const valid = concurrencyOk && labels !== undefined && Object.keys(patch).length > 0;

  const save = async (): Promise<void> => {
    if (!valid || busy) return;
    setBusy(true);
    setErr(undefined);
    const r = await getApi().ci({ op: "binding.patch", id: b.id, patch });
    setBusy(false);
    if (!r.ok) return setErr(r.error);
    ctx.toast(t("ci.saved"));
    onSaved();
  };

  return (
    <Dialog title={t("ci.editTitle", { repo: b.target })} onClose={onClose} testid="ci-edit-dialog" drawer>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <Field label={t("ci.concurrency")} hint={single ? t("ci.concurrencyImport") : t("ci.concurrencyHint")}>
          <input
            type="number"
            min={1}
            max={CI_MAX_CONCURRENCY}
            value={concurrency}
            disabled={busy || single}
            onInput={(e) => setConcurrency((e.currentTarget as HTMLInputElement).value)}
            aria-invalid={!concurrencyOk}
            data-testid="ci-edit-concurrency"
          />
        </Field>
        <Field label={t("ci.labels")} hint={t("ci.labelsHint")}>
          <input
            type="text"
            value={labelsText}
            disabled={busy}
            onInput={(e) => setLabelsText((e.currentTarget as HTMLInputElement).value)}
            aria-invalid={labels === undefined}
            autocomplete="off"
            spellcheck={false}
            data-testid="ci-edit-labels"
          />
        </Field>
        {labels === undefined && <p class="hint" data-testid="ci-edit-labels-invalid">{t("ci.labelsInvalid")}</p>}
        {b.source === "account" && accounts.length > 1 && (
          <Field label={t("ci.account")}>
            <select value={account} disabled={busy} onChange={(e) => setAccount((e.currentTarget as HTMLSelectElement).value)} data-testid="ci-edit-account">
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.login}
                </option>
              ))}
            </select>
          </Field>
        )}
        {err && <CiError error={err} />}
        <div class="row end" style={{ marginTop: "12px" }}>
          <button type="button" class="btn" onClick={onClose} data-testid="ci-edit-cancel">
            {t("common.cancel")}
          </button>
          <button type="submit" class="btn primary" disabled={!valid || busy} data-testid="ci-edit-save">
            {t("ci.save")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function UnbindDialog({ ctx, binding: b, onClose, onDone }: { ctx: PageContext; binding: CiBinding; onClose(): void; onDone(): void }): JSX.Element {
  const t = useT();
  const running = b.instances.filter((i) => i.state === "busy").length;
  const [force, setForce] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const [busy, setBusy] = useState(false);

  const unbind = async (): Promise<void> => {
    setBusy(true);
    setErr(undefined);
    const r = await getApi().ci({ op: "binding.delete", id: b.id, ...(force && { force: true }) });
    setBusy(false);
    if (!r.ok) return setErr(r.error);
    ctx.toast(t("ci.unbound", { repo: b.target }));
    onDone();
  };

  return (
    <Dialog title={t("ci.unbindTitle", { repo: b.target })} onClose={onClose} testid="ci-unbind-dialog">
      <p>{b.source === "import" ? t("ci.unbindBodyImport") : b.source === "token" ? t("ci.unbindBodyToken") : t("ci.unbindBody")}</p>
      {running > 0 && (
        <>
          <p class="hint ci-problem" data-testid="ci-unbind-busy">{t("ci.unbindBusy", { n: running })}</p>
          <label class="check">
            <input type="checkbox" checked={force} onChange={(e) => setForce(e.currentTarget.checked)} data-testid="ci-unbind-force" /> {t("ci.unbindForce")}
          </label>
        </>
      )}
      {err && <CiError error={err} />}
      <div class="row end" style={{ marginTop: "12px" }}>
        <button type="button" class="btn" onClick={onClose} data-testid="ci-unbind-cancel">
          {t("common.cancel")}
        </button>
        <button type="button" class="btn danger" disabled={busy} onClick={() => void unbind()} data-testid="ci-unbind-confirm">
          {t("ci.unbind")}
        </button>
      </div>
    </Dialog>
  );
}

/** Re-registers a binding that has no account: GitHub needs a fresh one-time registration token from the user. */
function RepairDialog({ ctx, binding: b, onClose, onDone }: { ctx: PageContext; binding: CiBinding; onClose(): void; onDone(): void }): JSX.Element {
  const t = useT();
  const [token, setToken] = useState("");
  const [err, setErr] = useState<ActionError | undefined>();
  const [busy, setBusy] = useState(false);
  const valid = registrationTokenValid(token);

  const repair = async (): Promise<void> => {
    if (!valid || busy) return;
    setBusy(true);
    setErr(undefined);
    const r = await getApi().ci({ op: "binding.repair", id: b.id, registration_token: token.trim() });
    setToken("");
    setBusy(false);
    if (!r.ok) return setErr(r.error);
    ctx.toast(t("ci.restarted", { repo: b.target }));
    onDone();
  };

  return (
    <Dialog title={`${t("ci.step.repair")} · ${b.target}`} onClose={onClose} testid="ci-repair-dialog" drawer>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void repair();
        }}
      >
        <p class="hint">{t("ci.regTokenHelp")}</p>
        <Field label={t("ci.regToken")}>
          <input
            type="password"
            value={token}
            disabled={busy}
            onInput={(e) => setToken((e.currentTarget as HTMLInputElement).value)}
            aria-invalid={token !== "" && !valid}
            autocomplete="off"
            spellcheck={false}
            data-testid="ci-repair-token"
          />
        </Field>
        {token !== "" && !valid && <p class="hint">{t("ci.regTokenInvalid")}</p>}
        {err && <CiError error={err} />}
        <div class="row end" style={{ marginTop: "12px" }}>
          <button type="button" class="btn" onClick={onClose} data-testid="ci-repair-cancel">
            {t("common.cancel")}
          </button>
          <button type="submit" class="btn primary" disabled={!valid || busy} data-testid="ci-repair-submit">
            {t("ci.step.repair")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
