import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Banner, Confirm, Field } from "../../components";
import { useT } from "../../i18n";
import { formatTime } from "../../lib/format";
import { pairPasswordValid } from "../../lib/pair";
import { PAIR_PASSWORD_MAX, PAIR_PASSWORD_MIN, type ActionError } from "../../../shared/ipc";
import type { PairPasswordState } from "../../../shared/control";
import { PairErrorBanner } from "./common";

/** This node's connection password: set, change, or clear. The value is never read back. */
export function PairPassword({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const [state, setState] = useState<PairPasswordState | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();
  const [pw, setPw] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [clearing, setClearing] = useState(false);

  const load = async (): Promise<void> => {
    const r = await getApi().getPairPassword();
    if (r.ok) setState(r.data);
    else setErr(r.error);
  };
  useEffect(() => {
    void load();
  }, []);

  const valid = pairPasswordValid(pw);
  const same = pw === again;
  const locked = state?.locked_until != null && Date.parse(state.locked_until) > Date.now();

  const submit = async (e: Event): Promise<void> => {
    e.preventDefault();
    if (!valid || !same || busy) return;
    setBusy(true);
    const r = await getApi().setPairPassword(pw);
    setBusy(false);
    // The field values are dropped as soon as the kernel has them.
    setPw("");
    setAgain("");
    if (r.ok) {
      setErr(undefined);
      setState(r.data);
      ctx.toast(t("pair.passwordSaved"));
    } else setErr(r.error);
  };

  const clear = async (): Promise<void> => {
    setBusy(true);
    const r = await getApi().clearPairPassword();
    setBusy(false);
    setClearing(false);
    if (r.ok) {
      setErr(undefined);
      ctx.toast(t("pair.passwordCleared"));
      await load();
    } else setErr(r.error);
  };

  return (
    <section class="card" data-testid="pair-password">
      <h2>{t("pair.passwordTitle")}</h2>
      <p class="hint">{t("pair.passwordIntro")}</p>
      {state && (
        <div data-testid="pair-password-state">
          <p>{state.set ? t("pair.passwordSetAt", { time: formatTime(state.updated_at) }) : t("pair.passwordNotSet")}</p>
          {state.failed_attempts > 0 && <p class="hint" data-testid="pair-failed">{t("pair.failedAttempts", { n: state.failed_attempts })}</p>}
          {locked && <Banner kind="warn" testid="pair-locked">{t("pair.lockedUntil", { time: formatTime(state.locked_until) })}</Banner>}
        </div>
      )}
      <form onSubmit={(e) => void submit(e)}>
        <Field label={t("pair.passwordNew")} hint={t("pair.passwordRules", { min: PAIR_PASSWORD_MIN, max: PAIR_PASSWORD_MAX })}>
          <input
            type="password"
            value={pw}
            onInput={(e) => setPw((e.currentTarget as HTMLInputElement).value)}
            autocomplete="new-password"
            aria-invalid={pw !== "" && !valid}
            data-testid="pair-pw-new"
          />
        </Field>
        <Field label={t("pair.repeat")}>
          <input
            type="password"
            value={again}
            onInput={(e) => setAgain((e.currentTarget as HTMLInputElement).value)}
            autocomplete="new-password"
            aria-invalid={again !== "" && !same}
            data-testid="pair-pw-again"
          />
        </Field>
        {again !== "" && !same && <p class="hint" data-testid="pair-pw-mismatch">{t("pair.noMatch")}</p>}
        {err && <PairErrorBanner error={err} testid="pair-password-error" />}
        <div class="row">
          <button type="submit" class="btn primary" disabled={!valid || !same || busy} data-testid="pair-pw-submit">
            {state?.set ? t("pair.passwordChange") : t("pair.passwordSet")}
          </button>
          {state?.set && (
            <button type="button" class="btn danger" disabled={busy} onClick={() => setClearing(true)} data-testid="pair-pw-clear">
              {t("pair.passwordClear")}
            </button>
          )}
        </div>
      </form>
      {clearing && (
        <Confirm
          title={t("pair.passwordClearTitle")}
          body={<p>{t("pair.passwordClearBody")}</p>}
          confirmLabel={t("pair.passwordClear")}
          busy={busy}
          onCancel={() => setClearing(false)}
          onConfirm={() => void clear()}
        />
      )}
    </section>
  );
}
