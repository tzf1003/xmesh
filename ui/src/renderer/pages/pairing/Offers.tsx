import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Badge, Confirm } from "../../components";
import { useCopy } from "../../design-copy";
import { useResource } from "../../hooks";
import { Icon } from "../../icons";
import { formatTime } from "../../lib/format";
import type { OfferSummary } from "../../../shared/control";

const POLL_MS = 15_000;

/**
 * Connection methods other paired devices have offered this node. Nothing is applied until the user
 * accepts here; the summary never carries a credential, so there is nothing secret to show.
 */
export function Offers({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const copy = useCopy();
  const offers = useResource(() => getApi().listOffers(), [ctx.snapshot.selectedInstance], POLL_MS);
  const [pending, setPending] = useState<{ offer: OfferSummary; verb: "accept" | "reject" }>();
  const [busy, setBusy] = useState(false);
  if (offers.resource.state !== "ready" || offers.resource.data.offers.length === 0) return null;
  const list = offers.resource.data.offers as OfferSummary[];

  const label = (o: OfferSummary): string => (o.kind === "cloudflare" ? "Cloudflare" : "Tailscale / Headscale");
  const detail = (o: OfferSummary): string => {
    if (o.kind === "cloudflare" && o.cloudflare) {
      return copy(`本机将使用隧道主机名 ${o.cloudflare.hostname}，并通过 ${o.cloudflare.peer_hostname} 连接对方。`, `This device will use tunnel hostname ${o.cloudflare.hostname} and reach the sender at ${o.cloudflare.peer_hostname}.`);
    }
    const url = o.tailnet?.control_url;
    return url
      ? copy(`使用控制面 ${url} 的预授权 key 加入。`, `Join with a pre-authorised key for the control plane ${url}.`)
      : copy("使用 Tailscale 官方服务的预授权 key 加入。", "Join with a pre-authorised key for the Tailscale service.");
  };

  const run = async (): Promise<void> => {
    if (!pending) return;
    setBusy(true);
    const { offer, verb } = pending;
    if (verb === "accept") {
      const r = await getApi().acceptOffer(offer.id);
      setBusy(false);
      setPending(undefined);
      if (!r.ok) ctx.toast(r.error.message, true);
      else ctx.toast(r.data.restart_required ? copy("已应用，重启 xmesh 后生效。", "Applied; restart xmesh for it to take effect.") : copy("已应用。", "Applied."));
    } else {
      const r = await getApi().rejectOffer(offer.id);
      setBusy(false);
      setPending(undefined);
      ctx.toast(r.ok ? copy("已拒绝。", "Rejected.") : r.error.message, !r.ok);
    }
    offers.reload();
  };

  return (
    <section class="card" data-testid="offers">
      <h2>{copy("收到的连接方式", "Connection methods offered to you")} <Badge>{list.length}</Badge></h2>
      <p class="hint">{copy("已配对的设备想把它的连接方式分享给本机。只有你点“接受”才会写入本机配置。", "A paired device wants to share a way to connect with this one. Nothing changes until you accept.")}</p>
      {list.map((o) => (
        <div class="row" key={o.id} data-testid={`offer-${o.id}`}>
          <Icon name="link" />
          <div>
            <strong>{label(o)}</strong> <small>{copy("来自", "from")} {o.from_name}</small>
            <p class="hint">{detail(o)}</p>
            <small>{copy("过期时间", "Expires")} {formatTime(o.expires_at)}</small>
          </div>
          {ctx.can("pair") && (
            <div class="row">
              <button type="button" class="btn primary" onClick={() => setPending({ offer: o, verb: "accept" })} data-testid={`offer-accept-${o.id}`}>{copy("接受", "Accept")}</button>
              <button type="button" class="btn" onClick={() => setPending({ offer: o, verb: "reject" })} data-testid={`offer-reject-${o.id}`}>{copy("拒绝", "Reject")}</button>
            </div>
          )}
        </div>
      ))}
      {pending && (
        <Confirm
          danger={pending.verb === "reject"}
          title={pending.verb === "accept" ? copy("接受这个连接方式？", "Accept this connection method?") : copy("拒绝这个连接方式？", "Reject this connection method?")}
          confirmLabel={pending.verb === "accept" ? copy("接受", "Accept") : copy("拒绝", "Reject")}
          body={pending.verb === "accept" ? copy(`来自 ${pending.offer.from_name}。接受后会改写本机配置，并保存对方给的凭据到本机私有文件。`, `From ${pending.offer.from_name}. Accepting rewrites this device's configuration and saves the credentials it sent to a private local file.`) : copy("拒绝后对方给的凭据会被丢弃。", "Rejecting discards the credentials it sent.")}
          busy={busy}
          onCancel={() => setPending(undefined)}
          onConfirm={() => void run()}
        />
      )}
    </section>
  );
}
