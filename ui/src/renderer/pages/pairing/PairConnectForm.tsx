import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Field } from "../../components";
import { useT } from "../../i18n";
import { pairPasswordValid } from "../../lib/pair";
import { PAIR_PASSWORD_MAX, PAIR_PASSWORD_MIN, type ActionError, type PairConnectArgs } from "../../../shared/ipc";
import { PairErrorBanner } from "./common";

/**
 * Pairs with another device by its connection password. A discovered device brings its address and
 * fingerprint; a device added by hand takes the address typed here.
 */
export function PairConnectForm({
  ctx,
  address,
  fingerprint,
  onDone,
  testid,
}: {
  ctx: PageContext;
  /** The address of a discovered device. Undefined when the user types the address. */
  address?: string;
  /** The certificate fingerprint shown in discovery; the kernel refuses any other certificate. */
  fingerprint?: string;
  onDone?: () => void;
  testid: string;
}): JSX.Element {
  const t = useT();
  const [typed, setTyped] = useState("");
  const [pw, setPw] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const target = (address ?? typed).trim();
  const addressOk = target !== "" && target.length <= 260;
  const valid = pairPasswordValid(pw);

  const submit = async (e: Event): Promise<void> => {
    e.preventDefault();
    if (!addressOk || !valid || busy) return;
    setBusy(true);
    const args: PairConnectArgs = { address: target, password: pw };
    if (fingerprint !== undefined) args.fingerprint = fingerprint;
    const r = await getApi().connectPair(args);
    setBusy(false);
    // The password is dropped as soon as the kernel has it, whatever the answer.
    setPw("");
    if (r.ok) {
      setErr(undefined);
      ctx.toast(t("pair.connected", { name: r.data.name }));
      onDone?.();
    } else setErr(r.error);
  };

  return (
    <form onSubmit={(e) => void submit(e)} data-testid={testid}>
      {address === undefined ? (
        <Field label={t("pair.address")} hint={t("pair.addressHint")}>
          <input type="text" value={typed} onInput={(e) => setTyped((e.currentTarget as HTMLInputElement).value)} autocomplete="off" spellcheck={false} data-testid={`${testid}-address`} />
        </Field>
      ) : (
        <p class="hint mono" data-testid={`${testid}-address`}>
          {address}
        </p>
      )}
      <Field label={t("pair.peerPassword")}>
        <input
          type="password"
          value={pw}
          onInput={(e) => setPw((e.currentTarget as HTMLInputElement).value)}
          autocomplete="off"
          aria-invalid={pw !== "" && !valid}
          data-testid={`${testid}-password`}
        />
      </Field>
      {pw !== "" && !valid && <p class="hint">{t("pair.passwordRules", { min: PAIR_PASSWORD_MIN, max: PAIR_PASSWORD_MAX })}</p>}
      {err && <PairErrorBanner error={err} testid={`${testid}-error`} />}
      <button type="submit" class="btn primary" disabled={!addressOk || !valid || busy} data-testid={`${testid}-submit`}>
        {t("pair.connect")}
      </button>
    </form>
  );
}

/** Manual add: the address and the other device's connection password. */
export function PairManual({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  return (
    <section class="card" data-testid="pair-manual">
      <h2>{t("pair.manualTitle")}</h2>
      <p class="hint">{t("pair.manualIntro")}</p>
      <PairConnectForm ctx={ctx} testid="pair-manual-form" />
    </section>
  );
}
