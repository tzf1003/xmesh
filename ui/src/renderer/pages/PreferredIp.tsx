import type { JSX } from "preact";
import { useEffect, useMemo, useState } from "preact/hooks";
import type { PreferredIPEntry, PreferredIPInspectResponse, PreferredIPState } from "../../shared/control";
import { PREFERRED_IP_TEXT_MAX } from "../../shared/preferred-ip";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Dialog, Empty, NotSupported, PageHead, ResourceView } from "../components";
import { useCopy } from "../design-copy";
import { useResource } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { formatTime } from "../lib/format";
import {
  MAX_SOURCES, MAX_SOURCE_IPS, REFRESH_INTERVALS, durationSeconds, SCAN_INTERVALS, blankSource, parseIps, policyProblem, policyValue, readPolicy, samePolicy,
  type PeerMode, type Policy, type Source, type SourceType,
} from "../lib/preferred-ip";
import { applySaved } from "../lib/reload";

type Tab = "overview" | "access" | "pool";
type Copy = (zh: string, en: string) => string;

const TABS: readonly Tab[] = ["overview", "access", "pool"];

const SOURCE_LABEL: Record<PreferredIPEntry["source"], [string, string]> = {
  manual: ["手动", "Manual"],
  file: ["文件", "File"],
  url: ["订阅", "Subscription"],
  scan: ["测速", "Scan"],
};

export function PreferredIp({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  if (!ctx.can("config.read")) return <div class="page"><PageHead title={t("nav.preferredIp")} /><NotSupported what={t("nav.preferredIp")} /></div>;
  return <PreferredIpPage ctx={ctx} />;
}

function PreferredIpPage({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const c = useCopy();
  const writable = ctx.can("config.write");
  const config = useResource(() => getApi().getConfig(), [ctx.snapshot.selectedInstance, ctx.configRevision]);
  const pool = useResource(() => getApi().preferredIp({ op: "get" }), [ctx.snapshot.selectedInstance], 4000);
  const [state, setState] = useState<PreferredIPState | undefined>();
  const [tab, setTab] = useState<Tab>("overview");
  const cfgData = config.resource.state === "ready" ? config.resource.data : config.resource.state === "error" ? config.resource.stale : undefined;
  const saved = useMemo(() => (cfgData ? readPolicy(cfgData.parsed) : undefined), [cfgData?.etag]);
  const [draft, setDraft] = useState<Policy | undefined>();
  const [busy, setBusy] = useState(false);
  const [importing, setImporting] = useState(false);
  useEffect(() => { if (saved) setDraft(saved.policy); }, [saved]);
  const live = pool.resource.state === "ready" ? pool.resource.data : pool.resource.state === "error" ? pool.resource.stale : undefined;
  useEffect(() => { if (live) setState(live); }, [live]);
  const dirty = !!draft && !!saved && !samePolicy(draft, saved.policy);
  useEffect(() => { ctx.setConfigDirty(dirty); return () => ctx.setConfigDirty(false); }, [dirty]);

  const edit = (change: (p: Policy) => Policy): void => { setDraft((d) => (d ? change(d) : d)); ctx.applied(undefined); };
  const problem = draft ? policyProblem(draft) : undefined;
  const PROBLEM: Record<NonNullable<typeof problem>, string> = {
    fixed: c("固定模式至少要填一个地址。", "Fixed mode needs at least one address."),
    url: c("订阅地址必须以 https:// 开头。", "A subscription address must start with https://."),
    file: c("文件来源要填写绝对路径。", "A file source needs an absolute path."),
    peer: c("按设备设为固定时，要为它填写地址。", "A device set to fixed needs addresses."),
    ips: c(`一个来源最多 ${MAX_SOURCE_IPS} 个地址，导入的多余地址已丢弃。`, `A source holds at most ${MAX_SOURCE_IPS} addresses; extra imported ones were dropped.`),
    sources: c(`来源最多 ${MAX_SOURCES} 个。`, `At most ${MAX_SOURCES} sources.`),
  };

  const save = async (): Promise<void> => {
    if (!draft || !cfgData || problem) return;
    setBusy(true);
    try {
      const r = await getApi().patchConfig({ edits: [{ path: "/mesh/cloudflare/preferred_ip", value: policyValue(draft) }], etag: cfgData.etag, dryRun: false });
      if (!r.ok) { ctx.toast(r.error.message, true); return; }
      ctx.applied(await applySaved(r.data.restart_required));
      ctx.toast(c("优选 IP 设置已保存，对新连接立即生效。", "Preferred-IP settings saved; new connections use them at once."));
      config.reload();
      pool.reload();
    } finally { setBusy(false); }
  };
  const run = async (task: "scan" | "refresh"): Promise<void> => {
    setBusy(true);
    try {
      const r = await getApi().preferredIp({ op: "run", task });
      if (!r.ok) { ctx.toast(r.error.message, true); return; }
      setState(r.data);
      ctx.toast(task === "scan" ? c("已开始测速，结果稍后出现在池里。", "Speed test started; results will appear in the pool.") : c("订阅已刷新。", "Subscriptions refreshed."));
      pool.reload();
    } finally { setBusy(false); }
  };

  return <div class="page pip" data-testid="page-preferred-ip">
    <PageHead title={t("nav.preferredIp")} sub={c("让连接 Cloudflare 隧道时优先走你选定的边缘 IP：来源、定时测速、轮换与按设备的接入方式都在这里。", "Connect to Cloudflare tunnels through edge addresses you choose: sources, scheduled speed tests, failover and per-device access live here.")}>
      <button type="button" class="btn" onClick={() => { config.reload(); pool.reload(); }}>{t("common.refresh")}</button>
    </PageHead>
    {saved && !saved.supported && <Banner kind="warn">{c("这个节点的配置不是 schema_version 2，不能设置优选 IP。", "This node's configuration is not schema_version 2, so preferred IPs cannot be set.")}</Banner>}
    {saved?.supported && !saved.cloudflareEnabled && <Banner kind="warn" testid="pip-cloudflare-off">{c("Cloudflare 通道还没有启用，优选 IP 暂时不会被用到。可以先设置好，启用通道后立即生效。", "The Cloudflare path is not enabled yet, so preferred IPs are not used. You can set them up now; they apply once the path is on.")}</Banner>}
    <div class="execution-tabs" role="tablist" aria-label={t("nav.preferredIp")}>
      {TABS.map((id) => <button type="button" role="tab" key={id} aria-selected={tab === id} data-testid={`pip-tab-${id}`} onClick={() => setTab(id)}>{id === "overview" ? c("总览", "Overview") : id === "access" ? c("接入与轮换", "Access & failover") : c("IP 池与来源", "Pool & sources")}</button>)}
    </div>
    <ResourceView resource={config.resource}>{() => <>
      {draft && saved?.supported && <>
        {tab === "overview" && <Overview c={c} draft={draft} state={state} loaded={saved} writable={writable} busy={busy} edit={edit} run={run} />}
        {tab === "access" && <Access c={c} draft={draft} state={state} loaded={saved} writable={writable} edit={edit} />}
        {tab === "pool" && <PoolTab c={c} draft={draft} state={state} loaded={saved} writable={writable} busy={busy} edit={edit} run={run} openImport={() => setImporting(true)} />}
        {problem && <Banner kind="warn" testid="pip-problem">{PROBLEM[problem]}</Banner>}
        {dirty && <div class="pip-savebar" role="region" aria-label={c("未保存的修改", "Unsaved changes")} data-testid="pip-savebar">
          <span>{c("有未保存的修改", "You have unsaved changes")}</span>
          <button type="button" class="btn" disabled={busy} onClick={() => { if (saved) setDraft(saved.policy); }} data-testid="pip-reset">{c("还原", "Reset")}</button>
          <button type="button" class="btn primary" disabled={busy || !writable || !!problem} onClick={() => void save()} data-testid="pip-save">{c("保存并生效", "Save and apply")}</button>
        </div>}
      </>}
    </>}</ResourceView>
    {importing && draft && <ImportDialog c={c} onClose={() => setImporting(false)} onImport={(ips) => {
      edit((p) => {
        const mine = p.sources.findIndex((s) => s.type === "manual" && s.name === IMPORT_NAME);
        if (mine >= 0) return { ...p, sources: p.sources.map((s, i) => (i === mine ? { ...s, ips: [...new Set([...s.ips, ...ips])].slice(0, MAX_SOURCE_IPS) } : s)) };
        return { ...p, sources: [...p.sources, { ...blankSource("manual"), name: IMPORT_NAME, ips: ips.slice(0, MAX_SOURCE_IPS) }] };
      });
      setImporting(false);
      setTab("pool");
    }} />}
  </div>;
}

const IMPORT_NAME = "导入";

interface PanelProps {
  c: Copy;
  draft: Policy;
  state: PreferredIPState | undefined;
  loaded: ReturnType<typeof readPolicy>;
  writable: boolean;
  edit(change: (p: Policy) => Policy): void;
}
interface ActionProps extends PanelProps {
  busy: boolean;
  run(task: "scan" | "refresh"): Promise<void>;
}

function Switch({ checked, disabled, onChange, label, hint, testid }: { checked: boolean; disabled?: boolean; onChange(v: boolean): void; label: string; hint?: string; testid?: string }): JSX.Element {
  return <label class="check pip-switch"><input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.currentTarget.checked)} data-testid={testid} /><span><strong>{label}</strong>{hint && <span class="hint">{hint}</span>}</span></label>;
}

function Overview({ c, draft, state, writable, busy, edit, run }: ActionProps): JSX.Element {
  const entries = state?.pool ?? [];
  const ok = entries.filter((e) => e.status === "ok").length;
  const cooling = entries.filter((e) => e.status === "cooling").length;
  const scan = state?.scan;
  const hostMissing = !!scan && scan.host === "";
  return <>
    <section class="card pip-master" data-testid="pip-master">
      <Switch checked={draft.enabled} disabled={!writable} onChange={(v) => edit((p) => ({ ...p, enabled: v }))} testid="pip-enabled"
        label={c("启用 Cloudflare 优选 IP", "Use preferred Cloudflare IPs")}
        hint={c("关闭后所有连接都走 DNS 解析出的地址，池和设置会保留。", "When off, every connection uses the address DNS returns; the pool and settings are kept.")} />
      <div class="pip-chips">
        <Badge kind={draft.enabled ? "ok" : ""}>{draft.enabled ? c("已启用", "On") : c("已关闭", "Off")}</Badge>
        <Badge kind="info">{draft.mode === "smart" ? c("默认：智能", "Default: smart") : c("默认：固定", "Default: fixed")}</Badge>
        {draft.noDnsFallback && <Badge kind="warn">{c("不回退 DNS", "No DNS fallback")}</Badge>}
      </div>
    </section>
    <div class="pip-stats" data-testid="pip-stats">
      <div class="card stat"><span class="hint">{c("池内地址", "In pool")}</span><span class="value" data-testid="pip-count">{entries.length}</span></div>
      <div class="card stat"><span class="hint">{c("可用", "Healthy")}</span><span class="value">{ok}</span></div>
      <div class="card stat"><span class="hint">{c("冷却中", "Cooling")}</span><span class="value">{cooling}</span></div>
      <div class="card stat"><span class="hint">{c("上次测速", "Last scan")}</span><span class="value pip-small">{scan?.last_at ? formatTime(scan.last_at) : c("还没有", "Never")}</span></div>
    </div>
    <section class="card" data-testid="pip-scan">
      <h2>{c("定时自动优选", "Scheduled selection")}</h2>
      <p class="hint">{c("按间隔从 Cloudflare 公布的 IP 段随机抽样，对对端的隧道主机名做 TLS 握手，按丢包和延时留下最好的几个加入池，并重测池里已有的地址。", "On a schedule, sample Cloudflare's published ranges, handshake with a peer's tunnel hostname, keep the best few by loss and latency, and re-measure what is already pooled.")}</p>
      <div class="row">
        <Switch checked={draft.scan.enabled} disabled={!writable} onChange={(v) => edit((p) => ({ ...p, scan: { ...p.scan, enabled: v } }))} testid="pip-scan-enabled" label={c("定时测速", "Scheduled scan")} />
        <label class="field pip-inline"><span>{c("间隔", "Every")}</span>
          <select value={draft.scan.interval} disabled={!writable || !draft.scan.enabled} onChange={(e) => edit((p) => ({ ...p, scan: { ...p.scan, interval: e.currentTarget.value } }))} data-testid="pip-scan-interval">
            {withCurrent(SCAN_INTERVALS, draft.scan.interval).map((v) => <option value={v} key={v}>{intervalLabel(v, c)}</option>)}
          </select></label>
        <span class="grow" />
        <button type="button" class="btn primary" disabled={busy || !writable || scan?.running || hostMissing} onClick={() => void run("scan")} data-testid="pip-scan-now">{scan?.running ? c("测速中…", "Scanning…") : c("立即测速", "Scan now")}</button>
      </div>
      {scan?.running && <p class="hint" role="status" data-testid="pip-scan-running">{c(`正在测速：抽样 ${scan.sample} 个地址，对 ${scan.host} 握手。`, `Scanning ${scan.sample} sampled addresses against ${scan.host}.`)}</p>}
      {hostMissing && <p class="hint">{c("还没有可用于测速的对端主机名：先配对一台带 Cloudflare 主机名的设备，或在「IP 池与来源」里填写测速主机名。", "No peer hostname is available for the test: pair a device with a Cloudflare hostname, or set the test hostname under Pool & sources.")}</p>}
      {scan?.error && !scan.running && <Banner kind="warn" testid="pip-scan-error">{scan.error}</Banner>}
      {draft.scan.enabled && scan?.next_at && <p class="hint">{c(`下次测速：${formatTime(scan.next_at)}`, `Next scan: ${formatTime(scan.next_at)}`)}</p>}
    </section>
    <section class="card" data-testid="pip-pool">
      <h2>{c("当前池", "Current pool")}</h2>
      {entries.length === 0
        ? <Empty>{c("池是空的：到「IP 池与来源」添加地址，或开启定时测速。池为空时连接走 DNS 地址。", "The pool is empty: add addresses under Pool & sources or turn on scheduled scans. With an empty pool, connections use the DNS address.")}</Empty>
        : <PoolTable c={c} entries={entries} />}
    </section>
  </>;
}

function PoolTable({ c, entries }: { c: Copy; entries: PreferredIPEntry[] }): JSX.Element {
  const status = (e: PreferredIPEntry): JSX.Element => e.status === "ok" ? <Badge kind="ok">{c("可用", "Healthy")}</Badge> : e.status === "cooling" ? <Badge kind="warn">{c("冷却中", "Cooling")}</Badge> : <Badge>{c("未测", "Untested")}</Badge>;
  return <div class="table-wrap"><table data-testid="pip-pool-table">
    <thead><tr><th scope="col">IP</th><th scope="col">{c("来源", "Source")}</th><th scope="col">{c("状态", "Status")}</th><th scope="col">{c("延时", "Latency")}</th><th scope="col">{c("丢包", "Loss")}</th><th scope="col">{c("最近使用", "Last used")}</th></tr></thead>
    <tbody>{entries.map((e) => <tr key={e.ip} data-testid={`pip-row-${e.ip}`}>
      <td class="mono">{e.ip}</td>
      <td>{c(...SOURCE_LABEL[e.source])}</td>
      <td>{status(e)}{e.status === "cooling" && e.cooldown_until && <span class="hint"> {c(`至 ${formatTime(e.cooldown_until)}`, `until ${formatTime(e.cooldown_until)}`)}</span>}{e.failures > 0 && e.status !== "cooling" && <span class="hint"> {c(`失败 ${e.failures} 次`, `${e.failures} failed`)}</span>}</td>
      <td>{e.latency_ms === null ? "—" : `${e.latency_ms} ms`}</td>
      <td>{e.loss_percent === null ? "—" : `${e.loss_percent}%`}</td>
      <td>{e.last_used_at ? formatTime(e.last_used_at) : "—"}</td>
    </tr>)}</tbody>
  </table></div>;
}

function Access({ c, draft, state, loaded, writable, edit }: PanelProps): JSX.Element {
  const ids = Object.keys(loaded.peers).sort();
  const live = new Map((state?.peers ?? []).map((p) => [p.device_id, p]));
  const number = (v: string, min: number, max: number, fallback: number): number => { const n = Number(v); return Number.isFinite(n) ? Math.min(max, Math.max(min, Math.trunc(n))) : fallback; };
  return <>
    <section class="card" data-testid="pip-default-mode">
      <h2>{c("默认接入方式", "Default access")}</h2>
      <fieldset class="stack">
        <label class="check"><input type="radio" name="pip-mode" checked={draft.mode === "smart"} disabled={!writable} onChange={() => edit((p) => ({ ...p, mode: "smart" }))} data-testid="pip-mode-smart" /><span><strong>{c("智能", "Smart")}</strong><span class="hint">{c("按实测延时从好到差依次尝试池里的地址；测过的排前面，没测过的按来源顺序排后面；连续失败的地址进入冷却。", "Try pooled addresses best first by measured latency; unmeasured ones follow in source order; addresses that keep failing cool down.")}</span></span></label>
        <label class="check"><input type="radio" name="pip-mode" checked={draft.mode === "fixed"} disabled={!writable} onChange={() => edit((p) => ({ ...p, mode: "fixed" }))} data-testid="pip-mode-fixed" /><span><strong>{c("固定", "Fixed")}</strong><span class="hint">{c("只尝试下面指定的地址，按填写顺序，不看评分，也不冷却。", "Only try the addresses below, in order. No scoring and no cooldown.")}</span></span></label>
      </fieldset>
      {draft.mode === "fixed" && <label class="field"><span>{c("固定地址（每行一个）", "Fixed addresses (one per line)")}</span>
        <IpBox rows={3} value={draft.fixedIps} disabled={!writable} onChange={(ips) => edit((p) => ({ ...p, fixedIps: ips }))} testid="pip-fixed-ips" /></label>}
    </section>
    <section class="card" data-testid="pip-peers">
      <h2>{c("按设备覆盖", "Per-device override")}</h2>
      <p class="hint">{c("默认所有已配对设备用上面的方式；某台设备在你这条线路上表现特别差或特别好时，可以单独指定。", "Every paired device follows the default above; override one when it behaves very differently on your line.")}</p>
      {ids.length === 0 ? <Empty>{c("还没有配置了 Cloudflare 主机名的已配对设备。", "No paired device has a Cloudflare hostname yet.")}</Empty>
        : <div class="table-wrap"><table>
          <thead><tr><th scope="col">{c("设备", "Device")}</th><th scope="col">{c("接入方式", "Access")}</th><th scope="col">{c("固定地址", "Fixed addresses")}</th><th scope="col">{c("最近连接", "Last connection")}</th></tr></thead>
          <tbody>{ids.map((id) => {
            const o = draft.peers[id] ?? { mode: "default" as PeerMode, ips: [] };
            const l = live.get(id);
            const set = (next: Partial<typeof o>): void => edit((p) => ({ ...p, peers: { ...p.peers, [id]: { ...o, ...next } } }));
            return <tr key={id} data-testid={`pip-peer-${id.slice(0, 12)}`}>
              <td><strong>{loaded.peers[id]}</strong><div class="hint mono">{id.slice(0, 16)}…</div></td>
              <td><select value={o.mode} disabled={!writable} onChange={(e) => set({ mode: e.currentTarget.value as PeerMode })} aria-label={c("接入方式", "Access")} data-testid={`pip-peer-mode-${id.slice(0, 12)}`}>
                <option value="default">{c("跟随默认", "Follow default")}</option><option value="smart">{c("智能", "Smart")}</option><option value="fixed">{c("固定", "Fixed")}</option><option value="off">{c("不用优选（走 DNS）", "Off (use DNS)")}</option></select></td>
              <td>{o.mode === "fixed" ? <IpBox value={o.ips} disabled={!writable} onChange={(ips) => set({ ips })} label={c("固定地址", "Fixed addresses")} /> : <span class="hint">—</span>}</td>
              <td>{l?.last_at ? <><span class="mono">{l.current || c("DNS 地址", "DNS address")}</span><div class="hint">{formatTime(l.last_at)}</div></> : <span class="hint">—</span>}</td>
            </tr>;
          })}</tbody></table></div>}
    </section>
    <section class="card" data-testid="pip-failover">
      <h2>{c("轮换与故障切换", "Failover")}</h2>
      <p class="hint">{c("每个地址完整尝试一遍（TCP、TLS、Access、WebSocket 和设备间握手）；失败就换下一个，所有优选地址都失败才回退 DNS 地址。", "Each address gets a full attempt (TCP, TLS, Access, WebSocket and the device handshake); on failure the next one is tried, and only when all fail does the DNS address get its turn.")}</p>
      <div class="pip-fields">
        <label class="field"><span>{c("单个地址的尝试时限（秒）", "Time limit per address (seconds)")}</span><input type="number" min={1} max={30} value={Math.round((durationSeconds(draft.attemptTimeout) ?? 4) * 10) / 10} disabled={!writable} onInput={(e) => { const v = e.currentTarget.value; edit((p) => ({ ...p, attemptTimeout: `${number(v, 1, 30, 4)}s` })); }} data-testid="pip-attempt" /></label>
        <label class="field"><span>{c("连续失败几次进入冷却", "Failures before cooldown")}</span><input type="number" min={1} max={20} value={draft.failThreshold} disabled={!writable} onInput={(e) => { const v = e.currentTarget.value; edit((p) => ({ ...p, failThreshold: number(v, 1, 20, p.failThreshold) })); }} data-testid="pip-threshold" /></label>
        <label class="field"><span>{c("冷却时长（分钟）", "Cooldown (minutes)")}</span><input type="number" min={1} max={1440} value={Math.round(((durationSeconds(draft.cooldown) ?? 1800) / 60) * 10) / 10} disabled={!writable} onInput={(e) => { const v = e.currentTarget.value; edit((p) => ({ ...p, cooldown: `${number(v, 1, 1440, 30)}m` })); }} data-testid="pip-cooldown" /></label>
      </div>
      <Switch checked={draft.noDnsFallback} disabled={!writable} onChange={(v) => edit((p) => ({ ...p, noDnsFallback: v }))} testid="pip-no-dns"
        label={c("优选地址全部失败时不回退 DNS 地址", "Do not fall back to the DNS address")}
        hint={c("只在确定直连的 Cloudflare 地址一定被阻断时打开；否则优选地址全坏了就连不上。", "Turn on only when the default Cloudflare address is blocked for sure; otherwise a bad pool leaves you unable to connect.")} />
    </section>
  </>;
}

function PoolTab({ c, draft, state, writable, busy, edit, run, openImport }: ActionProps & { openImport(): void }): JSX.Element {
  const [adding, setAdding] = useState<SourceType | undefined>();
  const live = state?.sources.filter((s) => s.index >= 0) ?? [];
  const legacy = state?.sources.filter((s) => s.index < 0) ?? [];
  const update = (i: number, change: Partial<Source>): void => edit((p) => ({ ...p, sources: p.sources.map((s, j) => (j === i ? { ...s, ...change } : s)) }));
  const hasUrl = draft.sources.some((s) => s.type === "url" && !s.disabled);
  const full = draft.sources.length >= MAX_SOURCES;
  return <>
    <section class="card" data-testid="pip-sources">
      <div class="row"><h2 class="grow">{c("来源", "Sources")}</h2>
        <button type="button" class="btn" disabled={!writable || full} onClick={openImport} data-testid="pip-import">{c("导入列表…", "Import list…")}</button>
        <button type="button" class="btn" disabled={busy || !writable || !hasUrl} onClick={() => void run("refresh")} data-testid="pip-refresh">{c("立即刷新订阅", "Refresh subscriptions")}</button>
      </div>
      <p class="hint">{c("池由所有来源合并去重而成，只收 Cloudflare 公布 IP 段内的地址，最多 64 个。每个来源可以单独关闭。", "The pool merges and de-duplicates all sources, accepts only addresses inside Cloudflare's published ranges, and holds at most 64. Each source can be switched off.")}</p>
      {legacy.map((s) => <div class="pip-source" key={`${s.type}-${s.name}`}><Icon name={s.type === "file" ? "folder" : "code"} /><div class="grow"><strong>{s.name}</strong><div class="hint">{c(`${s.count} 个地址 · 来自配置里较早的 preferred_ips 字段，在配置文件里修改`, `${s.count} addresses · from the older preferred_ips setting; edit it in the config file`)}</div></div></div>)}
      {draft.sources.length === 0 && legacy.length === 0 && <Empty>{c("还没有来源。添加手动地址、文件或订阅，或者只开定时测速。", "No sources yet. Add manual addresses, a file or a subscription, or rely on scheduled scans alone.")}</Empty>}
      {draft.sources.map((s, i) => {
        const l = live.find((x) => x.index === i);
        return <div class="pip-source" key={i} data-testid={`pip-source-${i}`}>
          <Icon name={s.type === "url" ? "globe" : s.type === "file" ? "folder" : "code"} />
          <div class="grow">
            <div class="row"><input class="pip-name" value={s.name} placeholder={s.type === "manual" ? c("手动地址", "Manual addresses") : s.type === "file" ? c("本地文件", "Local file") : c("订阅", "Subscription")} disabled={!writable} onInput={(e) => update(i, { name: e.currentTarget.value })} aria-label={c("来源名称", "Source name")} />
              <Badge>{c(...SOURCE_LABEL[s.type])}</Badge>{l && !s.disabled && <Badge kind={l.error ? "warn" : "ok"}>{c(`${l.count} 个`, `${l.count}`)}</Badge>}</div>
            {s.type === "manual" && <IpBox rows={2} value={s.ips} placeholder="104.18.32.77" disabled={!writable} onChange={(ips) => update(i, { ips })} label={c("地址", "Addresses")} />}
            {s.type === "file" && <input class="mono" value={s.path} placeholder="/path/to/result.csv" disabled={!writable} onInput={(e) => update(i, { path: e.currentTarget.value })} aria-label={c("文件路径", "File path")} />}
            {s.type === "url" && <div class="row"><input class="mono grow" value={s.url} placeholder="https://" disabled={!writable} onInput={(e) => update(i, { url: e.currentTarget.value.trim() })} aria-label={c("订阅地址", "Subscription address")} data-testid={`pip-source-url-${i}`} />
              <label class="field pip-inline"><span>{c("每", "Every")}</span><select value={s.refresh} disabled={!writable} onChange={(e) => update(i, { refresh: e.currentTarget.value })}>{withCurrent(REFRESH_INTERVALS, s.refresh).map((v) => <option value={v} key={v}>{intervalLabel(v, c)}</option>)}</select></label></div>}
            {l?.error && <div class="hint pip-error" role="status">{l.error}</div>}
            {s.type === "url" && l?.fetched_at && !l.error && <div class="hint">{c(`上次刷新 ${formatTime(l.fetched_at)}`, `Fetched ${formatTime(l.fetched_at)}`)}</div>}
          </div>
          <Switch checked={!s.disabled} disabled={!writable} onChange={(v) => update(i, { disabled: !v })} label={c("启用", "On")} />
          <button type="button" class="btn small" disabled={!writable} onClick={() => edit((p) => ({ ...p, sources: p.sources.filter((_, j) => j !== i) }))} aria-label={c("删除来源", "Remove source")} data-testid={`pip-source-remove-${i}`}>{c("删除", "Remove")}</button>
        </div>;
      })}
      <div class="row pip-add">
        {(["manual", "file", "url"] as const).map((type) => <button type="button" class="btn" key={type} disabled={!writable || full} onClick={() => { setAdding(type); edit((p) => ({ ...p, sources: [...p.sources, blankSource(type)] })); }} data-testid={`pip-add-${type}`}><Icon name="plus" /> {type === "manual" ? c("手动地址", "Manual addresses") : type === "file" ? c("本地文件", "Local file") : c("订阅地址", "Subscription")}</button>)}
      </div>
      {adding === "url" && <Banner kind="warn" testid="pip-url-warning">{c("订阅地址会让这台设备定期访问第三方服务器，请只添加你信任的来源。只有落在 Cloudflare IP 段内的地址会被采用，其他行一律忽略。", "A subscription makes this device fetch a third-party address on a schedule. Add only sources you trust. Only addresses inside Cloudflare's ranges are used; every other line is ignored.")}</Banner>}
    </section>
    <section class="card" data-testid="pip-scan-settings">
      <h2>{c("测速参数", "Speed-test settings")}</h2>
      <p class="hint">{c("定时测速的开关和间隔在「总览」里。这里设置每次测多少个、留下几个。", "The schedule is on the Overview tab. Here you set how many addresses to test and how many to keep.")}</p>
      <div class="pip-fields">
        <label class="field"><span>{c("每次抽样", "Sample size")}</span><input type="number" min={10} max={2000} value={draft.scan.sample} disabled={!writable} onInput={(e) => { const n = Number(e.currentTarget.value); edit((p) => ({ ...p, scan: { ...p.scan, sample: Number.isFinite(n) ? Math.min(2000, Math.max(10, Math.trunc(n))) : p.scan.sample } })); }} data-testid="pip-sample" /></label>
        <label class="field"><span>{c("留下最好的", "Keep best")}</span><input type="number" min={1} max={64} value={draft.scan.keep} disabled={!writable} onInput={(e) => { const n = Number(e.currentTarget.value); edit((p) => ({ ...p, scan: { ...p.scan, keep: Number.isFinite(n) ? Math.min(64, Math.max(1, Math.trunc(n))) : p.scan.keep } })); }} data-testid="pip-keep" /></label>
        <label class="field"><span>{c("测速主机名（可空）", "Test hostname (optional)")}</span><input class="mono" value={draft.scan.host} placeholder={state?.scan.host || "xmesh-….example.com"} disabled={!writable} onInput={(e) => { const v = e.currentTarget.value.trim(); edit((p) => ({ ...p, scan: { ...p.scan, host: v } })); }} data-testid="pip-scan-host" /></label>
      </div>
      <Switch checked={draft.scan.ipv6} disabled={!writable} onChange={(v) => edit((p) => ({ ...p, scan: { ...p.scan, ipv6: v } }))} label={c("同时抽样 IPv6 地址", "Also sample IPv6")} hint={c("本机要有 IPv6 出口才有意义。", "Only useful when this machine has IPv6 connectivity.")} />
    </section>
  </>;
}

function ImportDialog({ c, onClose, onImport }: { c: Copy; onClose(): void; onImport(ips: string[]): void }): JSX.Element {
  const [text, setText] = useState("");
  const [result, setResult] = useState<PreferredIPInspectResponse | undefined>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setResult(undefined);
    if (text.trim() === "" || text.length > PREFERRED_IP_TEXT_MAX) return;
    let live = true;
    const timer = setTimeout(() => {
      setBusy(true);
      void getApi().preferredIp({ op: "inspect", text }).then((r) => {
        if (!live) return;
        setBusy(false);
        if (r.ok) { setResult(r.data); setError(""); } else setError(r.error.message);
      });
    }, 300);
    return () => { live = false; clearTimeout(timer); };
  }, [text]);
  const rejected = result?.items.filter((i) => i.status === "rejected").length ?? 0;
  const existing = result?.items.filter((i) => i.status === "existing").length ?? 0;
  const tooLong = text.length > PREFERRED_IP_TEXT_MAX;
  return <Dialog title={c("导入优选 IP", "Import preferred IPs")} onClose={onClose} testid="pip-import-dialog">
    <div class="stack">
    <p class="hint">{c("粘贴别人优选好的列表（每行一个 IP），或 CloudflareSpeedTest 的 result.csv。", "Paste a list someone else selected (one IP per line) or a CloudflareSpeedTest result.csv.")}</p>
    <textarea rows={7} class="mono pip-paste" value={text} onInput={(e) => setText(e.currentTarget.value)} aria-label={c("要导入的地址", "Addresses to import")} data-testid="pip-import-text" />
    <label class="field"><span>{c("或选择文件", "Or choose a file")}</span><input type="file" accept=".txt,.csv,text/plain,text/csv" onChange={(e) => { const f = e.currentTarget.files?.[0]; if (f && f.size <= PREFERRED_IP_TEXT_MAX) void f.text().then(setText); else if (f) setError(c("文件太大：最多 32 KiB，可以分几次导入。", "File too large: 32 KiB at most; import it in parts.")); }} /></label>
    {tooLong && <Banner kind="warn">{c("粘贴的内容超过 32 KiB，请分几次导入。", "The pasted text is over 32 KiB; import it in parts.")}</Banner>}
    {error && <Banner kind="danger">{error}</Banner>}
    {result && <>
      <div class="row" data-testid="pip-import-summary">
        <Badge kind="ok">{c(`${result.accepted.length} 个可新增`, `${result.accepted.length} new`)}</Badge>
        {existing > 0 && <Badge>{c(`${existing} 个已存在`, `${existing} already there`)}</Badge>}
        {rejected > 0 && <Badge kind="danger">{c(`${rejected} 个被拒绝`, `${rejected} rejected`)}</Badge>}
      </div>
      <ul class="plain pip-lines">{result.items.slice(0, 60).map((i) => <li key={`${i.line}-${i.ip}`} class={`pip-line ${i.status}`}><span class="mono">{i.ip}</span><span class="hint">{i.status === "new" ? c("新增", "New") : i.note}</span></li>)}</ul>
    </>}
    <div class="row end">
      <button type="button" class="btn" onClick={onClose}>{c("取消", "Cancel")}</button>
      <button type="button" class="btn primary" disabled={busy || !result || result.accepted.length === 0} onClick={() => result && onImport(result.accepted)} data-testid="pip-import-confirm">{c(`导入 ${result?.accepted.length ?? 0} 个`, `Import ${result?.accepted.length ?? 0}`)}</button>
    </div>
    <p class="hint">{c("导入的地址保存为名为“导入”的手动来源，点「保存并生效」后才会写入配置。", "Imported addresses go into a manual source named “导入” and are written to the configuration only when you press Save and apply.")}</p>
    </div>
  </Dialog>;
}

function withCurrent<T extends string>(options: readonly T[], current: string): string[] {
  return options.includes(current as T) ? [...options] : [...options, current];
}

function intervalLabel(v: string, c: Copy): string {
  const h = /^(\d+)h$/.exec(v);
  if (!h) return v;
  const n = Number(h[1]);
  if (n % 24 === 0) return c(`${n / 24} 天`, `${n / 24} d`);
  return c(`${n} 小时`, `${n} h`);
}

/** An address list typed by hand: the text stays as typed, so a separator is not eaten before the next address. */
function IpBox({ value, onChange, rows, placeholder, disabled, label, testid }: { value: string[]; onChange(ips: string[]): void; rows?: number; placeholder?: string; disabled?: boolean; label?: string; testid?: string }): JSX.Element {
  const [text, setText] = useState(value.join(rows ? "\n" : ", "));
  useEffect(() => { if (parseIps(text).join() !== value.join()) setText(value.join(rows ? "\n" : ", ")); }, [value.join()]);
  const input = (v: string): void => { setText(v); onChange(parseIps(v)); };
  return rows
    ? <textarea rows={rows} class="mono" value={text} placeholder={placeholder} disabled={disabled} onInput={(e) => input(e.currentTarget.value)} aria-label={label} data-testid={testid} />
    : <input class="mono" value={text} placeholder={placeholder} disabled={disabled} onInput={(e) => input(e.currentTarget.value)} aria-label={label} data-testid={testid} />;
}
