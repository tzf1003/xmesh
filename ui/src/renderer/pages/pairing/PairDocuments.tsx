import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Banner, Confirm, ErrorBanner, Field } from "../../components";
import { useT } from "../../i18n";
import { PAIR_TAG_CHOICES, fingerprintMatches, groupFingerprint, parsePairText } from "../../lib/pair";
import type { ActionError } from "../../../shared/ipc";
import type { PairDocument } from "../../../shared/control";

/**
 * The pairing-document flow, kept for the cases a password cannot cover and for the command line. The
 * password flow above is the normal path.
 */
export function PairDocuments({ ctx, expanded }: { ctx: PageContext; expanded?: boolean }): JSX.Element {
  const t = useT();
  const [mine, setMine] = useState<PairDocument | undefined>();
  const [mineErr, setMineErr] = useState<ActionError | undefined>();
  const exportMine = async (): Promise<void> => {
    const r = await getApi().exportPair();
    if (r.ok) {
      setMine(r.data.pair);
      setMineErr(undefined);
    } else setMineErr(r.error);
  };

  const [text, setText] = useState("");
  const [typed, setTyped] = useState("");
  const [tags, setTags] = useState<string[]>(["tag:peer", "tag:device"]);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const [done, setDone] = useState<{ name: string } | undefined>();
  const parsed = text.trim() === "" ? undefined : parsePairText(text);
  const matches = parsed?.ok === true && fingerprintMatches(typed, parsed.pair.fingerprint);

  const doImport = async (): Promise<void> => {
    if (parsed?.ok !== true) return;
    setBusy(true);
    const r = await getApi().importPair({ pair: parsed.pair, fingerprint: typed.trim(), tags });
    setBusy(false);
    setConfirm(false);
    if (r.ok) {
      setErr(undefined);
      setDone({ name: r.data.name });
      ctx.applied({ restart: r.data.restart_required, paths: [], applyErrors: [] });
      setText("");
      setTyped("");
      ctx.toast(t("pair.imported", { name: r.data.name }));
    } else setErr(r.error);
  };

  return (
    <details class="card" open={expanded} data-testid="pair-advanced">
      <summary>
        <strong>{t("pair.advancedTitle")}</strong>
      </summary>
      <p class="hint">{t("pair.advancedHint")}</p>
      <p class="hint">{t("pair.intro")}</p>
      <div class="row">
        <button type="button" class="btn" onClick={() => void exportMine()} data-testid="pair-export">
          {t("pair.export")}
        </button>
      </div>
      {mineErr && <ErrorBanner error={mineErr} />}
      {mine && (
        <div style={{ marginTop: "8px" }}>
          <p>
            {t("pair.myFingerprint")}: <code data-testid="pair-my-fingerprint">{groupFingerprint(mine.fingerprint)}</code>
          </p>
          <textarea readOnly rows={6} value={JSON.stringify({ pair: mine }, null, 2)} onFocus={(e) => (e.currentTarget as HTMLTextAreaElement).select()} aria-label={t("pair.myDocument")} data-testid="pair-my-doc" />
          <p class="hint">{t("pair.copyHint")}</p>
        </div>
      )}
      <hr />
      <h3>{t("pair.importTitle")}</h3>
      <Field label={t("pair.paste")}>
        <textarea rows={6} value={text} onInput={(e) => setText((e.currentTarget as HTMLTextAreaElement).value)} aria-invalid={parsed?.ok === false} data-testid="pair-paste" />
      </Field>
      {parsed?.ok === false && <Banner kind="danger">{t("pair.badDocument")}</Banner>}
      {parsed?.ok === true && (
        <>
          <p>
            {t("pair.peerName")}: <strong data-testid="pair-peer-name">{parsed.pair.name}</strong>
          </p>
          <fieldset>
            <legend>{t("pair.tags")}</legend>
            {PAIR_TAG_CHOICES.map((tag) => (
              <label class="check" key={tag}>
                <input
                  type="checkbox"
                  checked={tags.includes(tag)}
                  onChange={(e) => setTags((cur) => ((e.currentTarget as HTMLInputElement).checked ? [...cur, tag] : cur.filter((x) => x !== tag)))}
                />
                <span>{tag}</span>
              </label>
            ))}
          </fieldset>
          {!tags.includes("tag:device") && <p class="hint" data-testid="pair-no-device">{t("pair.noDeviceTag")}</p>}
          <Field label={t("pair.typeFingerprint")} hint={t("pair.typeFingerprintHint")}>
            <input type="text" value={typed} onInput={(e) => setTyped((e.currentTarget as HTMLInputElement).value)} autocomplete="off" spellcheck={false} aria-invalid={typed !== "" && !matches} data-testid="pair-typed" />
          </Field>
          {typed !== "" && !matches && <p class="hint" data-testid="pair-mismatch">{t("pair.mismatch")}</p>}
          <button type="button" class="btn primary" disabled={!matches || tags.length === 0 || busy} onClick={() => setConfirm(true)} data-testid="pair-import">
            {t("pair.import")}
          </button>
        </>
      )}
      {err && <ErrorBanner error={err} />}
      {done && (
        <Banner kind="ok" testid="pair-done">
          {t("pair.done", { name: done.name })}
        </Banner>
      )}
      {confirm && parsed?.ok === true && (
        <Confirm
          title={t("pair.confirmTitle", { name: parsed.pair.name })}
          body={
            <>
              <p>{t("pair.confirmBody")}</p>
              <p>
                <code>{groupFingerprint(parsed.pair.fingerprint)}</code>
              </p>
              <p>{tags.join(", ")}</p>
            </>
          }
          confirmLabel={t("pair.import")}
          busy={busy}
          onCancel={() => setConfirm(false)}
          onConfirm={() => void doImport()}
        />
      )}
    </details>
  );
}
