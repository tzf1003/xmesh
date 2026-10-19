import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Field } from "../../components";
import { useT } from "../../i18n";
import type { ActionError } from "../../../shared/ipc";
import { type MeshAccess, type MeshControlPlane, parseMeshRequest } from "../../../shared/mesh";
import { MeshError } from "./common";

/**
 * Saves a control-plane credential with `xmesh mesh login`. The CLI verifies
 * the key and writes it to a 0600 file the panel names. The secret travels on
 * the request to the main process, which writes it to the CLI's stdin; it is
 * cleared from this page as soon as the request is sent, whatever the answer.
 * A write credential also covers reads, so it is the default.
 */
export function MeshLogin({
  ctx,
  open,
  onSaved,
  verify,
}: {
  ctx: PageContext;
  open: boolean;
  onSaved(): void;
  /** Runs after a save; resolves to the failure to show, if the credential does not work. */
  verify?: () => Promise<ActionError | undefined>;
}): JSX.Element {
  const t = useT();
  const [kind, setKind] = useState<MeshControlPlane>("tailscale");
  const [access, setAccess] = useState<MeshAccess>("write");
  const [url, setUrl] = useState("");
  const [tailnet, setTailnet] = useState("");
  const [clientId, setClientId] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const headscale = kind === "headscale";
  // The same validation the main process applies; an invalid form is never sent.
  const parsed = parseMeshRequest({
    op: "login",
    kind,
    access,
    secret,
    ...(headscale ? { url } : { ...(tailnet !== "" && { tailnet }), ...(clientId !== "" && { clientId }) }),
  });

  const save = async (): Promise<void> => {
    if (!parsed.ok) return;
    setBusy(true);
    setErr(undefined);
    const request = parsed.value;
    setSecret("");
    let r;
    try {
      r = await getApi().mesh(request);
    } catch {
      setBusy(false);
      return setErr({ code: "unavailable", message: t("mesh.login.failed") });
    }
    if (!r.ok) {
      setBusy(false);
      return setErr(r.error);
    }
    // Say whether it actually works, not just that it was stored.
    let bad: ActionError | undefined;
    try { bad = verify === undefined ? undefined : await verify(); }
    catch { bad = { code: "unavailable", message: t("mesh.login.failed") }; }
    setBusy(false);
    if (bad !== undefined) return setErr(bad);
    ctx.toast(t("mesh.login.saved"));
    onSaved();
  };

  return (
    <details class="card" open={open} data-testid="mesh-login">
      <summary>
        <strong>{t("mesh.login.title")}</strong>
      </summary>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <p class="hint">{t("mesh.login.intro")}</p>
        <Field label={t("mesh.login.kind")}>
          <select value={kind} disabled={busy} onChange={(e) => setKind((e.currentTarget as HTMLSelectElement).value as MeshControlPlane)} data-testid="mesh-login-kind">
            <option value="tailscale">Tailscale</option>
            <option value="headscale">Headscale</option>
          </select>
        </Field>
        <Field label={t("mesh.login.access")} hint={t("mesh.login.accessHint")}>
          <select value={access} disabled={busy} onChange={(e) => setAccess((e.currentTarget as HTMLSelectElement).value as MeshAccess)} data-testid="mesh-login-access">
            <option value="read">{t("mesh.login.access.read")}</option>
            <option value="write">{t("mesh.login.access.write")}</option>
          </select>
        </Field>
        {headscale ? (
          <Field label={t("mesh.login.url")} hint={t("mesh.login.urlHint")}>
            <input
              type="text"
              value={url}
              disabled={busy}
              onInput={(e) => setUrl((e.currentTarget as HTMLInputElement).value)}
              aria-invalid={!parsed.ok && parsed.field === "url" && url !== ""}
              autocomplete="off"
              spellcheck={false}
              data-testid="mesh-login-url"
            />
          </Field>
        ) : (
          <>
            <Field label={t("mesh.login.tailnet")} hint={t("mesh.login.tailnetHint")}>
              <input
                type="text"
                value={tailnet}
                disabled={busy}
                onInput={(e) => setTailnet((e.currentTarget as HTMLInputElement).value)}
                aria-invalid={!parsed.ok && parsed.field === "tailnet"}
                autocomplete="off"
                spellcheck={false}
                data-testid="mesh-login-tailnet"
              />
            </Field>
            <Field label={t("mesh.login.clientId")} hint={t("mesh.login.clientIdHint")}>
              <input
                type="text"
                value={clientId}
                disabled={busy}
                onInput={(e) => setClientId((e.currentTarget as HTMLInputElement).value)}
                aria-invalid={!parsed.ok && parsed.field === "clientId"}
                autocomplete="off"
                spellcheck={false}
                data-testid="mesh-login-client-id"
              />
            </Field>
          </>
        )}
        <Field label={t("mesh.login.secret")} hint={t("mesh.login.secretHint")}>
          <input
            type="password"
            value={secret}
            disabled={busy}
            onInput={(e) => setSecret((e.currentTarget as HTMLInputElement).value)}
            autocomplete="off"
            spellcheck={false}
            data-testid="mesh-login-secret"
          />
        </Field>
        {err && <MeshError error={err} toast={ctx.toast} />}
        <div class="row">
          <button type="submit" class="btn primary" disabled={!parsed.ok || busy} data-testid="mesh-login-submit">
            {busy ? t("mesh.login.busy") : t("mesh.login.submit")}
          </button>
        </div>
      </form>
    </details>
  );
}
