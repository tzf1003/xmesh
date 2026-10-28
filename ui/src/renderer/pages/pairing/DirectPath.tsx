import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Badge } from "../../components";
import { useCopy } from "../../design-copy";
import { useResource } from "../../hooks";
import { applySaved } from "../../lib/reload";

interface DirectSettings { enabled: boolean; listen: string; advertise: string; allowPairing: boolean; peers: number }

function readDirect(parsed: unknown): { supported: boolean; value: DirectSettings } {
  const cfg = parsed && typeof parsed === "object" ? (parsed as Record<string, unknown>) : {};
  const mesh = cfg.mesh && typeof cfg.mesh === "object" ? (cfg.mesh as Record<string, unknown>) : {};
  const d = mesh.direct && typeof mesh.direct === "object" ? (mesh.direct as Record<string, unknown>) : {};
  const text = (v: unknown): string => (typeof v === "string" ? v : "");
  const peers = d.peers && typeof d.peers === "object" ? Object.keys(d.peers).length : 0;
  return { supported: cfg.schema_version === 2, value: { enabled: d.enabled === true, listen: text(d.listen), advertise: text(d.advertise), allowPairing: d.allow_pairing === true, peers } };
}

const hostPort = (s: string): boolean => /^[^\s/@?#:]+:\d{1,5}$/.test(s) || /^\[[0-9a-fA-F:.]+\]:\d{1,5}$/.test(s);

/**
 * The direct path: this device listens on a public address and paired devices dial it, with no third party.
 * The listener takes only paired devices (strict mTLS); pairing reaches it only while an invite is open unless
 * "always open" is on. It is off by default because it exposes a port.
 */
export function DirectPath({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const copy = useCopy();
  const config = useResource(() => getApi().getConfig(), [ctx.snapshot.selectedInstance]);
  const res = config.resource;
  const data = res.state === "ready" ? res.data : res.state === "error" ? res.stale : undefined;
  const saved = data ? readDirect(data.parsed) : undefined;
  const [enabled, setEnabled] = useState(false);
  const [listen, setListen] = useState("");
  const [advertise, setAdvertise] = useState("");
  const [allowPairing, setAllowPairing] = useState(false);
  const [busy, setBusy] = useState(false);
  const reset = (): void => {
    if (!saved) return;
    setEnabled(saved.value.enabled); setListen(saved.value.listen); setAdvertise(saved.value.advertise); setAllowPairing(saved.value.allowPairing);
  };
  useEffect(reset, [data?.etag]);
  if (!data || !saved?.supported || !ctx.can("config.write")) return null;

  const listenOk = listen.trim() === "" || hostPort(listen.trim());
  const advertiseOk = advertise.trim() === "" || hostPort(advertise.trim());
  const dirty = enabled !== saved.value.enabled || listen.trim() !== saved.value.listen || advertise.trim() !== saved.value.advertise || allowPairing !== saved.value.allowPairing;

  const save = async (): Promise<void> => {
    setBusy(true);
    try {
      // One whole-object edit keeps the change atomic and drops fields that were cleared; the peer addresses stay as they are.
      const existing = (data.parsed as { mesh?: { direct?: { peers?: unknown } } }).mesh?.direct?.peers;
      const value = {
        enabled,
        ...(listen.trim() ? { listen: listen.trim() } : {}),
        ...(advertise.trim() ? { advertise: advertise.trim() } : {}),
        ...(allowPairing ? { allow_pairing: true } : {}),
        ...(existing ? { peers: existing } : {}),
      };
      const r = await getApi().patchConfig({ edits: [{ path: "/mesh/direct", value }], etag: data.etag, dryRun: false });
      if (!r.ok) { ctx.toast(r.error.message, true); return; }
      ctx.applied(await applySaved(r.data.restart_required));
      ctx.toast(copy("直连设置已保存。", "Direct settings saved."));
      config.reload();
    } finally { setBusy(false); }
  };

  return (
    <section class="card" data-testid="direct-path">
      <h3>{copy("直连", "Direct")} {saved.value.enabled ? <Badge kind="ok">{copy("已启用", "On")}</Badge> : <Badge>{copy("已关闭", "Off")}</Badge>}</h3>
      <p class="hint">{copy(
        "本机在公网地址上监听，配对过的设备直接拨过来，不经任何第三方。监听端口只接受已配对设备；默认关闭，因为它会暴露一个端口。",
        "This device listens on a public address and paired devices dial it directly, with no third party. The port accepts paired devices only. It is off by default because it exposes a port.",
      )}</p>
      <label class="check"><input type="checkbox" checked={enabled} disabled={busy} onChange={(e) => setEnabled(e.currentTarget.checked)} data-testid="direct-enabled" /><span>{copy("启用直连", "Enable direct")}</span></label>
      <label class="field"><span>{copy("监听地址", "Listen address")}</span><input type="text" class="mono" value={listen} placeholder="0.0.0.0:7435" disabled={busy} onInput={(e) => setListen(e.currentTarget.value)} spellcheck={false} data-testid="direct-listen" /></label>
      <label class="field"><span>{copy("对外地址（对端拨入用，主机:端口）", "Public address (peers dial this, host:port)")}</span><input type="text" class="mono" value={advertise} placeholder="home.example.com:7435" disabled={busy} onInput={(e) => setAdvertise(e.currentTarget.value)} spellcheck={false} data-testid="direct-advertise" /></label>
      <label class="check"><input type="checkbox" checked={allowPairing} disabled={busy} onChange={(e) => setAllowPairing(e.currentTarget.checked)} data-testid="direct-allow-pairing" /><span>{copy("公网端口始终开放配对（默认只在邀请期间开放）", "Keep pairing open on the public port (default: only while an invite is open)")}</span></label>
      {(!listenOk || !advertiseOk) && <p class="hint" data-testid="direct-invalid">{copy("地址要写成 主机:端口。", "Addresses must be host:port.")}</p>}
      <p class="hint">{copy(`已配置 ${saved.value.peers} 台设备的直连地址；添加设备时选“直连”并填对方的 主机:端口（默认 7435）。`, `${saved.value.peers} peer address(es) configured. To add a device over direct, pick "Direct" and enter its host:port (default 7435).`)}</p>
      <div class="row">
        <button type="button" class="btn primary" disabled={busy || !dirty || !listenOk || !advertiseOk} onClick={() => void save()} data-testid="direct-save">{copy("保存", "Save")}</button>
        <button type="button" class="btn" disabled={busy || !dirty} onClick={reset}>{copy("还原", "Reset")}</button>
      </div>
    </section>
  );
}
