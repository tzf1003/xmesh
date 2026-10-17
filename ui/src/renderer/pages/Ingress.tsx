import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Empty, ErrorBanner, Field, NotSupported, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { errorText, useT } from "../i18n";
import { formatTime } from "../lib/format";
import { PASSWORD_MAX, PASSWORD_MIN, hasEdgeSpace, type ActionError } from "../../shared/ipc";
import type { IngressClient } from "../../shared/control";

export function Ingress({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  const c = ctx.snapshot.connection;
  if (c.kind !== "connected") return null;
  const info = c.status.ingress;
  if (!ctx.can("ingress")) {
    return (
      <div class="page">
        <PageHead title={t("nav.ingress")} />
        <NotSupported what={t("nav.ingress")} />
      </div>
    );
  }
  if (!c.status.roles.includes("ingress")) {
    return (
      <div class="page" data-testid="page-ingress">
        <PageHead title={t("nav.ingress")} sub={t("ingress.sub")} />
        <Banner testid="ingress-disabled">
          <p>{t("ingress.disabled")}</p>
          <button type="button" class="btn" onClick={() => ctx.go("config")}>
            {t("ingress.toConfig")}
          </button>
        </Banner>
      </div>
    );
  }
  return (
    <div class="page" data-testid="page-ingress">
      <PageHead title={t("nav.ingress")} sub={t("ingress.sub")} />
      <section class="card">
        <h2>{t("ingress.status")}</h2>
        <p>
          {t("ingress.listen")}: <Badge kind="info">{info?.listen ?? "—"}</Badge>
        </p>
        <p>
          {t("ingress.issuer")}: <code data-testid="issuer">{info?.issuer ?? "—"}</code>
        </p>
      </section>
      <Clients ctx={ctx} />
      <Password ctx={ctx} />
    </div>
  );
}

function Clients({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const { resource, reload } = useResource(() => getApi().listIngressClients(), [ctx.snapshot.selectedInstance]);
  const [revoking, setRevoking] = useState<IngressClient | undefined>();
  const [busy, setBusy] = useState(false);
  const revoke = async (cl: IngressClient): Promise<void> => {
    setBusy(true);
    const r = await getApi().revokeIngressClient(cl.client_id);
    setBusy(false);
    setRevoking(undefined);
    if (r.ok) {
      ctx.toast(t("ingress.revoked", { name: cl.client_name }));
      reload();
    } else ctx.toast(errorText(t, r.error), true);
  };
  return (
    <section class="card" data-testid="clients">
      <h2>{t("ingress.clients")}</h2>
      <p class="hint">{t("ingress.clientsHint")}</p>
      <ResourceView resource={resource}>
        {(data) =>
          data.clients.length === 0 ? (
            <Empty>{t("ingress.noClients")}</Empty>
          ) : (
            <div class="table-wrap">
              <table data-testid="clients-table">
                <thead>
                  <tr>
                    <th scope="col">{t("col.name")}</th>
                    <th scope="col">{t("col.clientId")}</th>
                    <th scope="col">{t("col.scopes")}</th>
                    <th scope="col">{t("col.redirects")}</th>
                    <th scope="col" />
                  </tr>
                </thead>
                <tbody>
                  {data.clients.map((cl) => (
                    <tr key={cl.client_id} data-testid={`client-${cl.client_id}`}>
                      <td>
                        <strong>{cl.client_name || "—"}</strong>
                      </td>
                      <td class="mono">{cl.client_id}</td>
                      <td>
                        <div class="row">
                          {cl.scopes.map((s) => (
                            <Badge key={s}>{s}</Badge>
                          ))}
                        </div>
                      </td>
                      <td class="mono">{cl.redirect_uris.join(", ")}</td>
                      <td>
                        <button type="button" class="btn small danger" onClick={() => setRevoking(cl)}>
                          {t("ingress.revoke")}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </ResourceView>
      {revoking && (
        <Confirm
          title={t("ingress.revokeTitle", { name: revoking.client_name || revoking.client_id })}
          body={<p>{t("ingress.revokeBody")}</p>}
          confirmLabel={t("ingress.revoke")}
          danger
          busy={busy}
          onCancel={() => setRevoking(undefined)}
          onConfirm={() => void revoke(revoking)}
        />
      )}
    </section>
  );
}

function Password({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const [pw, setPw] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const len = [...pw].length;
  const lengthOk = len >= PASSWORD_MIN && len <= PASSWORD_MAX;
  const same = pw === again;
  const edgeSpace = hasEdgeSpace(pw);

  const submit = async (e: Event): Promise<void> => {
    e.preventDefault();
    if (!lengthOk || edgeSpace || !same) return;
    setBusy(true);
    const r = await getApi().rotateIngressPassword(pw);
    setBusy(false);
    if (r.ok) {
      setErr(undefined);
      // The field values are dropped as soon as the kernel has them.
      setPw("");
      setAgain("");
      ctx.toast(t("ingress.rotated"));
    } else setErr(r.error);
  };

  return (
    <section class="card" data-testid="password">
      <h2>{t("ingress.password")}</h2>
      <p class="hint">{t("ingress.passwordHint")}</p>
      <form onSubmit={(e) => void submit(e)}>
        <Field label={t("ingress.newPassword")} hint={t("ingress.lengthHint", { min: PASSWORD_MIN, max: PASSWORD_MAX })}>
          <input type="password" value={pw} onInput={(e) => setPw((e.currentTarget as HTMLInputElement).value)} autocomplete="new-password" aria-invalid={pw !== "" && (!lengthOk || edgeSpace)} data-testid="pw-new" />
        </Field>
        <Field label={t("ingress.repeat")}>
          <input type="password" value={again} onInput={(e) => setAgain((e.currentTarget as HTMLInputElement).value)} autocomplete="new-password" aria-invalid={again !== "" && !same} data-testid="pw-again" />
        </Field>
        {edgeSpace && <p class="hint" data-testid="pw-edge-space">{t("ingress.edgeSpace")}</p>}
        {again !== "" && !same && <p class="hint" data-testid="pw-mismatch">{t("ingress.noMatch")}</p>}
        {err && <ErrorBanner error={err} />}
        <button type="submit" class="btn primary" disabled={!lengthOk || edgeSpace || !same || busy} data-testid="pw-submit">
          {t("ingress.rotate")}
        </button>
      </form>
    </section>
  );
}
