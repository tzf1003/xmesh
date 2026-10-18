import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Badge, Confirm, NotSupported } from "../../components";
import { useResource } from "../../hooks";
import { errorText, useT } from "../../i18n";
import { ADDRESS_KIND_KEY, addressRows, localhostUrl } from "../../lib/gateway";
import type { GatewayInfo, PatInfo, PatList } from "../../../shared/control";
import type { DataResult, GatewayCopyKind } from "../../../shared/ipc";
import { ReachEditor } from "./ReachEditor";
import { TokenNotice } from "./TokenNotice";

/** The PAT the rotate button and the notice are about: the kernel creates it as "default". */
const GATEWAY_PAT = "default";

const NOT_CAPABLE: DataResult<PatList> = { ok: false, error: { code: "not_implemented", message: "" } };

/**
 * The local gateway on the overview: every address it answers on, the connect commands for each (with
 * the token already in them), the token rotation, and the reach editor.
 */
export function GatewayCard({ ctx, gateway }: { ctx: PageContext; gateway: GatewayInfo }): JSX.Element {
  const t = useT();
  const canPats = ctx.can("pats");
  const pats = useResource(() => (canPats ? getApi().listPats() : Promise.resolve(NOT_CAPABLE)), [ctx.snapshot.selectedInstance, canPats]);
  const [rotating, setRotating] = useState(false);
  const [busy, setBusy] = useState(false);

  const defaultPat: PatInfo | undefined = pats.resource.state === "ready" ? pats.resource.data.pats.find((p) => p.name === GATEWAY_PAT) : undefined;
  const rows = addressRows(gateway);

  const copy = (kind: GatewayCopyKind, url: string): void => {
    void getApi()
      .copyGateway({ kind, url })
      .then((r) => {
        if (r.ok) ctx.toast(t("overview.copied"));
        else ctx.toast(errorText(t, r.error), true);
      });
  };

  const rotate = async (): Promise<void> => {
    setBusy(true);
    const r = await getApi().rotateGatewayPat();
    setBusy(false);
    setRotating(false);
    if (r.ok) {
      ctx.toast(t("overview.rotated"));
      pats.reload();
    } else ctx.toast(errorText(t, r.error), true);
  };

  return (
    <section class="card" data-testid="gateway">
      <div class="page-head">
        <div class="grow">
          <h2>{t("overview.gateway")}</h2>
          <p class="hint">{t("overview.gatewaySub")}</p>
        </div>
        <Badge kind="info">{t("overview.authToken")}</Badge>
      </div>

      {defaultPat && <TokenNotice pat={defaultPat} url={localhostUrl(gateway)} onCopy={copy} />}

      <h3 style={{ marginTop: "12px" }}>{t("overview.addresses")}</h3>
      <div class="table-wrap">
        <table data-testid="gateway-addresses">
          <thead>
            <tr>
              <th scope="col">{t("overview.colKind")}</th>
              <th scope="col">{t("overview.colAddress")}</th>
              <th scope="col">{t("overview.colCopy")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => (
              <tr key={`${a.kind}:${a.url}`} data-testid="gateway-address" data-kind={a.kind}>
                <td>
                  <Badge kind={a.kind === "localhost" ? "" : "info"}>{t(ADDRESS_KIND_KEY[a.kind])}</Badge>
                </td>
                <td class="mono">
                  <code>{a.url}</code>
                </td>
                <td>
                  <div class="row">
                    <button type="button" class="btn small" onClick={() => copy("gatewayUrl", a.url)} data-testid="copy-url">
                      {t("overview.copyUrl")}
                    </button>
                    <button type="button" class="btn small" onClick={() => copy("claudeCodeCommand", a.url)} data-testid="copy-claude">
                      {t("overview.copyClaude")}
                    </button>
                    <button type="button" class="btn small" onClick={() => copy("codexConfig", a.url)} data-testid="copy-codex">
                      {t("overview.copyCodex")}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p class="hint" style={{ marginTop: "8px" }}>
        {t("overview.copyTokenHint")}
      </p>

      {defaultPat && (
        <div class="row" style={{ marginTop: "8px" }}>
          <button type="button" class="btn" onClick={() => setRotating(true)} data-testid="gateway-rotate">
            {t("overview.rotate")}
          </button>
        </div>
      )}

      <div style={{ marginTop: "16px" }}>
        {ctx.can("gateway") ? <ReachEditor ctx={ctx} gateway={gateway} /> : <NotSupported what={t("reach.title")} />}
      </div>

      {rotating && (
        <Confirm
          title={t("overview.rotateTitle")}
          body={<p>{t("overview.rotateBody")}</p>}
          confirmLabel={t("overview.rotateConfirm")}
          danger
          busy={busy}
          onCancel={() => setRotating(false)}
          onConfirm={() => void rotate()}
        />
      )}
    </section>
  );
}
