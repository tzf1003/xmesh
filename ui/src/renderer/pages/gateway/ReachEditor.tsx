import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Banner, ErrorBanner } from "../../components";
import { useT } from "../../i18n";
import { outcomeOf } from "../../lib/reload";
import { checkAccessEntry, checkHostEntry, draftFrom, listenPort, listenScope, requestFrom, sameReach, withScope, type ReachDraft } from "../../lib/gateway";
import type { GatewayInfo } from "../../../shared/control";
import type { ActionError } from "../../../shared/ipc";
import { buildListen } from "../../../shared/gateway";
import type { Key } from "../../messages";

const PROBLEM_KEY: Record<"invalid" | "duplicate", Key> = {
  invalid: "reach.invalidAddress",
  duplicate: "reach.duplicate",
};
const HOST_PROBLEM_KEY: Record<"invalid" | "duplicate", Key> = {
  invalid: "reach.invalidHost",
  duplicate: "reach.duplicate",
};

/**
 * The gateway's reach as quick controls: which sources may connect, which domain names it answers to,
 * and whether it listens on every interface. All of it applies at once (control API 1.5); a kernel that cannot
 * move the running gateway to the new listen scope says why. The shell shows what the kernel answered.
 */
export function ReachEditor({ ctx, gateway }: { ctx: PageContext; gateway: GatewayInfo }): JSX.Element {
  const t = useT();
  const baseline = draftFrom(gateway);
  const baselineKey = JSON.stringify(baseline);
  const [draft, setDraft] = useState<ReachDraft>(baseline);
  const [customInput, setCustomInput] = useState("");
  const [hostInput, setHostInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ActionError | undefined>(undefined);

  // The kernel's reach changed (this save or another): start from what it now says.
  useEffect(() => {
    setDraft(draftFrom(gateway));
  }, [baselineKey]);

  const dirty = !sameReach(draft, baseline);
  const customCheck = checkAccessEntry(customInput, draft.custom);
  const hostCheck = checkHostEntry(hostInput, draft.hosts);
  const port = listenPort(gateway.listen) ?? "8080";
  const scope = listenScope(draft.listen);
  const savedScope = listenScope(gateway.listen);

  const save = async (next: ReachDraft): Promise<void> => {
    setBusy(true);
    setError(undefined);
    const r = await getApi().updateGateway(requestFrom(next));
    setBusy(false);
    if (!r.ok) {
      setError(r.error);
      return;
    }
    setDraft(draftFrom(r.data));
    ctx.applied(outcomeOf(r.data));
    ctx.toast(t("reach.saved"));
  };

  const addCustom = (): void => {
    if (!customCheck.ok) return;
    setDraft({ ...draft, custom: [...draft.custom, customCheck.value] });
    setCustomInput("");
  };
  const addHost = (): void => {
    if (!hostCheck.ok) return;
    setDraft({ ...draft, hosts: [...draft.hosts, hostCheck.value] });
    setHostInput("");
  };

  return (
    <section class="reach" data-testid="reach">
      <h3>{t("reach.title")}</h3>
      <p class="hint">{t("reach.hint")}</p>

      {savedScope === "loopback" && (
        <Banner kind="warn" testid="gateway-loopback">
          <p>{t("overview.loopbackOnly")}</p>
          <button
            type="button"
            class="btn"
            disabled={busy}
            onClick={() => void save({ ...baseline, listen: buildListen("all", port) })}
            data-testid="gateway-listen-all"
          >
            {t("overview.listenAll")}
          </button>
        </Banner>
      )}

      <fieldset>
        <legend>{t("reach.title")}</legend>
        <label class="check">
          <input type="checkbox" checked disabled data-testid="reach-localhost" />
          <span>{t("reach.localhost")}</span>
        </label>
        <label class="check">
          <input
            type="checkbox"
            checked={draft.lan}
            onChange={(e) => setDraft({ ...draft, lan: (e.currentTarget as HTMLInputElement).checked })}
            data-testid="reach-lan"
          />
          <span>{t("reach.lan")}</span>
        </label>
        <label class="check">
          <input
            type="checkbox"
            checked={draft.tailnet}
            onChange={(e) => setDraft({ ...draft, tailnet: (e.currentTarget as HTMLInputElement).checked })}
            data-testid="reach-tailnet"
          />
          <span>{t("reach.tailnet")}</span>
        </label>
      </fieldset>

      <div class="stack">
        <h3>{t("reach.custom")}</h3>
        <p class="hint">{t("reach.customHint")}</p>
        <ul class="plain entry-list" data-testid="reach-custom">
          {draft.custom.map((c) => (
            <li key={c} data-testid="reach-custom-entry">
              <code>{c}</code>
              <button type="button" class="btn small" onClick={() => setDraft({ ...draft, custom: draft.custom.filter((x) => x !== c) })}>
                {t("reach.remove")}
              </button>
            </li>
          ))}
        </ul>
        <div class="row">
          <input
            type="text"
            value={customInput}
            placeholder="10.0.0.0/8"
            autocomplete="off"
            aria-invalid={customInput.trim() !== "" && !customCheck.ok}
            onInput={(e) => setCustomInput((e.currentTarget as HTMLInputElement).value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addCustom();
              }
            }}
            data-testid="reach-custom-input"
          />
          <button type="button" class="btn" disabled={!customCheck.ok} onClick={addCustom} data-testid="reach-custom-add">
            {t("reach.add")}
          </button>
        </div>
        {customInput.trim() !== "" && !customCheck.ok && customCheck.problem !== "empty" && (
          <p class="hint" data-testid="reach-custom-error">
            {t(PROBLEM_KEY[customCheck.problem])}
          </p>
        )}
      </div>

      <div class="stack">
        <h3>{t("reach.hosts")}</h3>
        <p class="hint">{t("reach.hostsHint")}</p>
        <ul class="plain entry-list" data-testid="reach-hosts">
          {draft.hosts.map((h) => (
            <li key={h} data-testid="reach-host-entry">
              <code>{h}</code>
              <button type="button" class="btn small" onClick={() => setDraft({ ...draft, hosts: draft.hosts.filter((x) => x !== h) })}>
                {t("reach.remove")}
              </button>
            </li>
          ))}
        </ul>
        <div class="row">
          <input
            type="text"
            value={hostInput}
            placeholder="xmesh.home.example"
            autocomplete="off"
            aria-invalid={hostInput.trim() !== "" && !hostCheck.ok}
            onInput={(e) => setHostInput((e.currentTarget as HTMLInputElement).value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addHost();
              }
            }}
            data-testid="reach-host-input"
          />
          <button type="button" class="btn" disabled={!hostCheck.ok} onClick={addHost} data-testid="reach-host-add">
            {t("reach.add")}
          </button>
        </div>
        {hostInput.trim() !== "" && !hostCheck.ok && hostCheck.problem !== "empty" && (
          <p class="hint" data-testid="reach-host-error">
            {t(HOST_PROBLEM_KEY[hostCheck.problem])}
          </p>
        )}
      </div>

      <fieldset>
        <legend>{t("reach.listen")}</legend>
        <label class="check">
          <input
            type="radio"
            name="reach-listen"
            checked={scope === "all"}
            onChange={() => setDraft(withScope(draft, "all"))}
            data-testid="reach-listen-all"
          />
          <span>{t("reach.listenAll")}</span>
        </label>
        <label class="check">
          <input
            type="radio"
            name="reach-listen"
            checked={scope === "loopback"}
            onChange={() => setDraft(withScope(draft, "loopback"))}
            data-testid="reach-listen-loopback"
          />
          <span>{t("reach.listenLoopback")}</span>
        </label>
        <span class="hint">{t("reach.listenHint", { port })}</span>
      </fieldset>

      {error && <ErrorBanner error={error} testid="reach-error" />}
      <div class="row end">
        {dirty && <span class="hint">{t("config.unsaved")}</span>}
        <button type="button" class="btn primary" disabled={!dirty || busy} onClick={() => void save(draft)} data-testid="reach-save">
          {t("reach.save")}
        </button>
      </div>
    </section>
  );
}
