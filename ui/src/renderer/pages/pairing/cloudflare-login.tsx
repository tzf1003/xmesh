import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { getApi } from "../../api";
import { Banner, Field } from "../../components";
import { useCopy } from "../../design-copy";
import { PasswordInput } from "../../password-input";
import type { ActionError } from "../../../shared/ipc";
import { parseMeshRequest } from "../../../shared/mesh";
import type { CloudflareDiscovery, MeshRequest } from "../../../shared/mesh";

/**
 * The fields for saving a Cloudflare token. Pasting the token is enough: the CLI lists the accounts and
 * domains it can see, the user picks a domain and says which hostname suffix devices should use, and the
 * IDs fill themselves in. If the token cannot list them (it lacks the Read permissions), or the user
 * prefers, the IDs can be typed. The token stays in this state until the page that owns the form closes it.
 */
export function useCloudflareLogin(): { fields: JSX.Element; request: MeshRequest | undefined; reset: () => void } {
  const copy = useCopy();
  const [token, setToken] = useState("");
  const [found, setFound] = useState<CloudflareDiscovery>();
  const [zoneId, setZoneId] = useState("");
  const [domain, setDomain] = useState("");
  const [manual, setManual] = useState(false);
  const [account, setAccount] = useState("");
  const [zone, setZone] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError>();

  const reset = (): void => {
    setToken(""); setFound(undefined); setZoneId(""); setDomain(""); setManual(false); setAccount(""); setZone(""); setErr(undefined);
  };

  const picked = found?.zones.find((z) => z.id === zoneId);
  const inZone = picked !== undefined && (domain === picked.name || domain.endsWith(`.${picked.name}`));
  const ids = manual ? { account, zone } : picked ? { account: picked.accountId, zone: picked.id } : undefined;
  const parsed = ids && (manual || inZone) ? parseMeshRequest({ op: "cloudflare.login", account: ids.account, zone: ids.zone, domain, secret: token }) : undefined;
  const request = parsed?.ok ? parsed.value : undefined;

  const discover = async (): Promise<void> => {
    const req = parseMeshRequest({ op: "cloudflare.discover", secret: token });
    if (!req.ok) return;
    setBusy(true);
    setErr(undefined);
    try {
      const r = await getApi().mesh(req.value);
      if (!r.ok) { setErr(r.error); return; }
      const data = r.data as CloudflareDiscovery;
      setFound(data);
      const only = data.zones.length === 1 ? data.zones[0] : undefined;
      if (only) { setZoneId(only.id); setDomain(`mesh.${only.name}`); }
    } catch (e) {
      setErr({ code: "unavailable", message: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const choose = (id: string): void => {
    setZoneId(id);
    const z = found?.zones.find((x) => x.id === id);
    if (z) setDomain(`mesh.${z.name}`);
  };

  const accountName = (id: string): string => found?.accounts.find((a) => a.id === id)?.name || id.slice(0, 8);

  const fields = (
    <>
      <p class="hint">{copy(
        "令牌只存进本机系统钥匙串，不写进配置文件，也不会发给其他设备。需要权限：Account 的 Cloudflare Tunnel、Access: Apps and Policies、Access: Service Tokens（Edit），Zone 的 DNS（Edit）；想自动获取账号和域名，再加 Account Settings 与 Zone 的 Read。",
        "The token goes only into the system keychain, never into the config file or to other devices. Required: Account Cloudflare Tunnel, Access Apps and Policies, Access Service Tokens (Edit), Zone DNS (Edit); for automatic lookup of accounts and domains also Account Settings and Zone (Read).",
      )}</p>
      <Field label={copy("API 令牌", "API token")}>
        <PasswordInput value={token} onInput={(v) => { setToken(v); setFound(undefined); setErr(undefined); }} autocomplete="off" testid="cf-token" />
      </Field>
      {!manual && (
        <div class="row">
          {!found && <button type="button" class="btn" disabled={busy || !parseMeshRequest({ op: "cloudflare.discover", secret: token }).ok} onClick={() => void discover()} data-testid="cf-discover">{busy ? copy("查找中…", "Looking…") : copy("查找账号和域名", "Find accounts and domains")}</button>}
          <button type="button" class="text-button" onClick={() => { setManual(true); setErr(undefined); }} data-testid="cf-manual">{copy("手动填写 ID", "Enter IDs by hand")}</button>
        </div>
      )}
      {err && <Banner kind="warn" testid="cf-discover-error">{err.message}{" "}{copy("可以改为手动填写账号 ID 和区域 ID。", "You can enter the account and zone IDs by hand instead.")}</Banner>}
      {!manual && found && (
        found.zones.length === 0
          ? <Banner kind="warn" testid="cf-no-zones">{copy("这个令牌看不到任何域名。确认它有 Zone（Read）权限，或手动填写 ID。", "This token sees no domains. Check it has Zone (Read), or enter the IDs by hand.")}</Banner>
          : <>
            <Field label={copy("域名（区域）", "Domain (zone)")}>
              <select value={zoneId} onChange={(e) => choose(e.currentTarget.value)} data-testid="cf-zone-select">
                <option value="">{copy("请选择", "Choose")}</option>
                {found.zones.map((z) => <option key={z.id} value={z.id}>{z.name}（{accountName(z.accountId)}）</option>)}
              </select>
            </Field>
            {picked && (
              <Field label={copy("设备主机名所用的域名", "Domain for device hostnames")} hint={copy(`须是 ${picked.name} 或它的子域名，设备会得到形如 laptop-xxxx.${domain || picked.name} 的主机名。`, `Must be ${picked.name} or a subdomain of it; devices get hostnames like laptop-xxxx.${domain || picked.name}.`)}>
                <input value={domain} onInput={(e) => setDomain(e.currentTarget.value.trim().toLowerCase())} data-testid="cf-domain" />
              </Field>
            )}
            {picked && domain !== "" && domain !== picked.name && (
              <Banner kind="warn" testid="cf-deep-domain">{copy(`设备主机名会在 ${domain} 之下再加一级，比 ${picked.name} 深了多级。Cloudflare 的通用证书只覆盖一级子域，经它连接可能在 TLS 握手时失败，建议直接填 ${picked.name}。`, `Device hostnames get one more label under ${domain}, which is several levels below ${picked.name}. Cloudflare's universal certificate covers one level only, so connections may fail at the TLS handshake; use ${picked.name} instead.`)}</Banner>
            )}
          </>
      )}
      {manual && (
        <>
          <Field label={copy("账号 ID", "Account ID")}><input value={account} onInput={(e) => setAccount(e.currentTarget.value.trim())} data-testid="cf-account" /></Field>
          <Field label={copy("区域 ID", "Zone ID")}><input value={zone} onInput={(e) => setZone(e.currentTarget.value.trim())} data-testid="cf-zone" /></Field>
          <Field label={copy("设备主机名所用的域名", "Domain for device hostnames")}><input value={domain} placeholder="example.com" onInput={(e) => setDomain(e.currentTarget.value.trim().toLowerCase())} data-testid="cf-domain" /></Field>
          <button type="button" class="text-button" onClick={() => { setManual(false); setErr(undefined); }}>{copy("改为自动查找", "Look them up instead")}</button>
        </>
      )}
    </>
  );
  return { fields, request, reset };
}
