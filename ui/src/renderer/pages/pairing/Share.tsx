import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Banner, Confirm, Field } from "../../components";
import { useCopy } from "../../design-copy";
import { useCloudflareLogin } from "./cloudflare-login";
import type { ActionError } from "../../../shared/ipc";
import { parseMeshRequest } from "../../../shared/mesh";
import type { Peer } from "../../../shared/control";

type Kind = "cloudflare" | "tailnet";

/**
 * Gives a paired device one of this node's connection methods: a Cloudflare tunnel made for it, or a
 * Tailscale pre-authorised key. The work runs in the xmesh CLI with the admin credential saved on this
 * machine; the credential never goes to the other device. The device applies the offer only after its
 * user accepts, and the panel here only ever sees whether it arrived.
 */
export function Share({ peer, ctx }: { peer: Peer; ctx: PageContext }): JSX.Element | null {
  const copy = useCopy();
  const [ask, setAsk] = useState<Kind>();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError>();
  const [login, setLogin] = useState(false);
  const cf = useCloudflareLogin();
  const [user, setUser] = useState("");
  if (!peer.device_id || !ctx.can("pair")) return null;
  const device = peer.device_id;
  const name = peer.display_name || peer.name;

  const done = (delivery: string): void => {
    ctx.toast(delivery === "queued"
      ? copy(`${name} 暂时连不上，已排队，等它在线时会自己来取。`, `${name} is not reachable right now; the offer is queued until it collects it.`)
      : copy(`已发给 ${name}，等对方点“接受”。`, `Sent to ${name}; it takes effect when they accept.`));
  };

  const share = async (kind: Kind): Promise<void> => {
    setBusy(true);
    setErr(undefined);
    const r = kind === "cloudflare" ? await getApi().mesh({ op: "cloudflare.share", device }) : await getApi().mesh({ op: "share.tailnet", device, ...(user !== "" && { user }) });
    setBusy(false);
    if (r.ok) {
      setAsk(undefined);
      setLogin(false);
      done(r.data.delivery);
      return;
    }
    if (kind === "cloudflare" && r.error.code === "no_credential") {
      setLogin(true);
      return;
    }
    setErr(r.error);
  };

  const saveLogin = async (): Promise<void> => {
    if (!cf.request) return;
    setBusy(true);
    setErr(undefined);
    const r = await getApi().mesh(cf.request);
    if (!r.ok) {
      setBusy(false);
      setErr(r.error);
      return;
    }
    cf.reset();
    setLogin(false);
    await share("cloudflare");
  };

  return (
    <section class="card" data-testid={`device-share-${peer.name}`}>
      <h3>{copy("把本机的连接方式分享给它", "Share this device's connection methods")}</h3>
      <p class="hint">{copy(
        "它没有公网地址时，本机可以替它建好 Cloudflare 隧道，或生成 Tailscale 预授权 key，经你们已有的加密连接发过去。对方点“接受”后才生效；管理员凭据只留在本机。",
        "If it has no public address, this device can create a Cloudflare tunnel for it or generate a Tailscale pre-authorised key and send it over your existing encrypted link. It takes effect only when the other side accepts; admin credentials stay on this device.",
      )}</p>
      <div class="row">
        <button type="button" class="btn" disabled={busy} onClick={() => { setErr(undefined); setAsk("cloudflare"); }} data-testid={`device-share-cloudflare-${peer.name}`}>{copy("分享 Cloudflare 隧道", "Share a Cloudflare tunnel")}</button>
        <button type="button" class="btn" disabled={busy} onClick={() => { setErr(undefined); setAsk("tailnet"); }} data-testid={`device-share-tailnet-${peer.name}`}>{copy("分享 Tailscale 连接方式", "Share Tailscale access")}</button>
      </div>
      {err && !ask && <Banner kind="danger" testid="device-share-error">{err.message}</Banner>}
      {ask && !login && (
        <Confirm
          title={ask === "cloudflare" ? copy("为它创建 Cloudflare 隧道？", "Create a Cloudflare tunnel for it?") : copy("给它一个 Tailscale 预授权 key？", "Give it a Tailscale pre-authorised key?")}
          confirmLabel={copy("发送", "Send")}
          body={<>
            <p>{ask === "cloudflare"
              ? copy(`会用本机保存的 Cloudflare 令牌为 ${name} 创建隧道、DNS 名称和 Access 应用，再发给它。`, `Uses the Cloudflare token saved on this device to create a tunnel, DNS name and Access app for ${name}, then sends them.`)
              : copy(`会用本机保存的 Tailscale / Headscale 管理凭据为 ${name} 生成一个单次有效的 key（48 小时内有效），再发给它。没有管理凭据时会提示怎么办。`, `Uses the Tailscale / Headscale admin credential saved on this device to make a single-use key for ${name} (valid 48 hours), then sends it. If none is saved you will be told what to do.`)}</p>
            {ask === "tailnet" && (
              <Field label={copy("Headscale 用户（可选）", "Headscale user (optional)")} hint={copy("Headscale 有多个用户时必填；Tailscale 官方服务留空。", "Required when Headscale has several users; leave empty for Tailscale.")}>
                <input value={user} onInput={(e) => setUser(e.currentTarget.value.trim())} data-testid="share-user" />
              </Field>
            )}
            {err && <Banner kind="danger" testid="device-share-error">{err.message}</Banner>}
          </>}
          busy={busy || (ask === "tailnet" && user !== "" && !parseMeshRequest({ op: "share.tailnet", device, user }).ok)}
          onCancel={() => { setAsk(undefined); setErr(undefined); }}
          onConfirm={() => void share(ask)}
        />
      )}
      {ask === "cloudflare" && login && (
        <Confirm
          title={copy("先保存 Cloudflare 令牌", "Save a Cloudflare token first")}
          confirmLabel={copy("保存并发送", "Save and send")}
          body={<>
            <p class="hint">{copy("本机还没有保存 Cloudflare 令牌。", "No Cloudflare token is saved on this device.")}</p>
            {cf.fields}
            {err && <Banner kind="danger" testid="device-share-error">{err.message}</Banner>}
          </>}
          busy={busy || !cf.request}
          onCancel={() => { setLogin(false); setAsk(undefined); setErr(undefined); cf.reset(); }}
          onConfirm={() => void saveLogin()}
        />
      )}
    </section>
  );
}
