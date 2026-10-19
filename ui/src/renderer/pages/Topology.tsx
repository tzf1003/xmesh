import type { JSX } from "preact";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import type { CallEventData, Peer } from "../../shared/control";
import { deriveIcon } from "../../shared/state";
import { getApi } from "../api";
import { Badge, Empty, Meter, NotSupported } from "../components";
import { Icon } from "../icons";
import { useT, errorText } from "../i18n";
import { ago, formatDuration } from "../lib/format";
import { assignSlots, CENTER, curve, linkKind, PEER_SLOTS, peerKey, recentCallers, topologyPeers, type Point } from "../lib/topology";

/** HTML controls over an SVG edge layer keep the graph keyboard accessible. */
function Node({ id, point, selected, kind, title, sub, children, onSelect, onHover }: {
  id: string; point: Point; selected: boolean; kind: string; title: string; sub: string;
  children?: JSX.Element; onSelect(): void; onHover(id: string | undefined): void;
}): JSX.Element {
  const ref = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => {
    // CSSOM works with the panel's strict style-src policy.
    ref.current?.style.setProperty("--x", `${point.x / 8}%`);
    ref.current?.style.setProperty("--y", `${point.y / 5.6}%`);
  }, [point.x, point.y]);
  return <button type="button" ref={ref} class={`graph-node ${kind}`} aria-pressed={selected} data-node={id}
    onClick={onSelect} onMouseEnter={() => onHover(id)} onMouseLeave={() => onHover(undefined)}
    onFocus={() => onHover(id)} onBlur={() => onHover(undefined)} title={`${title} · ${sub}`}>
    {children}<span class="graph-node-name"><Icon name={kind.includes("caller") ? "cloud" : kind.includes("relay") ? "network" : "device"}/><strong>{title}</strong></span>
    <span class="graph-node-sub"><i class="status-dot"/>{sub}</span>
  </button>;
}

export function Topology({ ctx, calls }: { ctx: PageContext; calls: CallEventData[] }): JSX.Element | null {
  const t = useT();
  const c = ctx.snapshot.connection;
  const [selected, setSelected] = useState<string | undefined>();
  const [selectedCaller, setSelectedCaller] = useState<string | undefined>();
  const [hover, setHover] = useState<string | undefined>();
  const [onlyOnline, setOnlyOnline] = useState(false);
  const [tag, setTag] = useState("");
  const [mode, setMode] = useState<"connections" | "configuration">("connections");
  const [tab, setTab] = useState<"overview" | "calls" | "config">("overview");
  useEffect(() => {
    if (ctx.focus?.peer) { setSelected(ctx.focus.peer); setSelectedCaller(undefined); setMode("connections"); setOnlyOnline(false); setTag(""); }
  }, [ctx.focus]);
  if (c.kind !== "connected") return null;
  const status = c.status;
  const peers = topologyPeers(status, ctx.can("peers") ? c.peers : []);
  const slots = assignSlots(peers, ctx.topologySlots.get(ctx.snapshot.selectedInstance) ?? new Map());
  ctx.topologySlots.set(ctx.snapshot.selectedInstance, slots);
  const self = peers[0]!;
  const selfId = peerKey(self);
  const picked = peers.find((p) => peerKey(p) === selected) ?? self;
  const active = hover ?? peerKey(picked);
  const visible = peers.filter((p) => p.self || ((!onlyOnline || p.online) && (!tag || p.tags.includes(tag))));
  const tags = [...new Set(peers.flatMap((p) => p.tags))].sort();
  const plotted = visible.filter((p) => !p.self && slots.has(peerKey(p)));
  const unplotted = peers.filter((p) => !p.self && !slots.has(peerKey(p))).length;
  const relays = [...new Set(plotted.filter((p) => linkKind(p) === "derp").map((p) => p.derp_region ?? "?"))];
  const relayPoint = (region: string): Point => ({ x: 735, y: 280 + relays.indexOf(region) * 64 });
  const callers = recentCallers(calls);
  const online = ctx.can("peers") ? peers.filter((p) => !p.self && p.online).length : status.peers.online;
  const total = ctx.can("peers") ? peers.length - 1 : status.peers.total;
  const scopedCalls = calls.filter((call) => selectedCaller ? (call.oauth_client ?? call.caller) === selectedCaller : call.device === picked.name).slice(-6).reverse();
  const select = (id: string): void => { setSelected(id); setSelectedCaller(undefined); setTab("overview"); };
  const selectCaller = (caller: string): void => { select(selfId); setSelectedCaller(caller); setTab("calls"); };
  const copy = (): void => {
    if (!status.gateway) return;
    void getApi().copyGateway({ kind: "codexConfig", url: status.gateway.url }).then((r) => ctx.toast(r.ok ? t("overview.copied") : errorText(t, r.error), !r.ok));
  };
  const connect = (): void => document.getElementById("overview-advanced")?.setAttribute("open", "");
  const onboard = (): void => status.network.mode === "tailnet" ? ctx.go("mesh", { meshTab: "onboard" }) : ctx.go("devices");
  return <div class="network-workspace" data-testid="topology-workspace">
    <div class="network-main">
      <section class="topology-panel" data-testid="topology">
        <div class="topology-toolbar">
          <div class="segs" aria-label={t("topology.view")}>
            <button type="button" aria-pressed={mode === "connections"} onClick={() => setMode("connections")} data-testid="topology-connections">{t("topology.connections")}</button>
            <button type="button" aria-pressed={mode === "configuration"} onClick={() => { setMode("configuration"); select(selfId); }} data-testid="topology-configuration">{t("topology.configuration")}</button>
          </div>
          {mode === "connections" && <div class="topology-filters">
            <label class="compact-check"><input type="checkbox" checked={onlyOnline} onChange={(e) => setOnlyOnline(e.currentTarget.checked)} data-testid="topology-online"/>{t("topology.onlineOnly")}</label>
            <select aria-label={t("col.tags")} value={tag} onChange={(e) => setTag(e.currentTarget.value)} data-testid="topology-tag">
              <option value="">{t("topology.allTags")}</option>{tags.map((x) => <option key={x} value={x}>{x}</option>)}
            </select>
          </div>}
        </div>
        {mode === "connections" ? <>
          <div class="topology-legend"><span class="legend-direct">{t("conn.direct")}</span><span class="legend-derp">{t("topology.relay")}</span><span class="legend-offline">{t("devices.offline")}</span></div>
          {!ctx.can("peers") && <NotSupported what={t("nav.devices")}/>}
          <div class={`graph-stage ${relays.length > 4 ? "graph-dense" : ""}`} data-testid="topology-canvas" onClick={(e) => { if (e.target === e.currentTarget) select(selfId); }}>
            <svg class="graph-edges" viewBox="0 0 800 560" preserveAspectRatio="none" aria-hidden="true">
              <defs><marker id="call-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="5" markerHeight="5" orient="auto"><path d="M 0 0 L 8 4 L 0 8 Z"/></marker></defs>
              <ellipse class="graph-ring" cx="400" cy="295" rx="130" ry="135"/><ellipse class="graph-ring" cx="400" cy="295" rx="275" ry="235"/>
              {plotted.map((p) => {
                const id = peerKey(p), point = PEER_SLOTS[slots.get(id)!]!, kind = linkKind(p);
                const relay = kind === "derp" ? relayPoint(p.derp_region ?? "?") : undefined;
                return <g key={id} data-edge={id} data-kind={kind} class={active !== selfId && active !== id ? "edge-dim" : ""}>
                  <path class={`graph-link ${kind}`} d={relay ? `${curve(CENTER, relay)} ${curve(relay, point)}` : curve(CENTER, point)}/>
                </g>;
              })}
              {callers.map((caller, i) => <path key={caller} class="graph-link caller" d={curve({ x: (i + 1) * 800 / (callers.length + 1), y: 66 }, { x: 400, y: 270 })} marker-end="url(#call-arrow)"/>)}
            </svg>
            {callers.map((caller, i) => <Node key={caller} id={`caller:${caller}`} point={{ x: (i + 1) * 800 / (callers.length + 1), y: 66 }} kind="caller" title={caller} sub={t("topology.observedCaller")} selected={selectedCaller === caller} onSelect={() => selectCaller(caller)} onHover={setHover}/>)}
            <Node id={selfId} point={CENTER} selected={picked.self} kind={`self state-${deriveIcon(c)}`} title={status.hostname} sub={status.roles.join(" + ")} onSelect={() => select(selfId)} onHover={setHover}><span class="node-role">{t("devices.self")}</span></Node>
            {plotted.map((p) => <Node key={peerKey(p)} id={peerKey(p)} point={PEER_SLOTS[slots.get(peerKey(p))!]!} selected={peerKey(picked) === peerKey(p)} kind={linkKind(p)} title={p.name} sub={connectionText(p, t)} onSelect={() => select(peerKey(p))} onHover={setHover}>
              {p.info?.error ? <span class="node-role issue" title={p.info.error}>{t("devices.infoFailed")}</span> : undefined}
            </Node>)}
            {relays.map((region) => <Relay key={region} point={relayPoint(region)} region={region} onClick={() => ctx.go("mesh", { meshTab: "derp" })}/>)}
          </div>
          <div class={`graph-list ${relays.length > 4 ? "graph-dense-list" : ""}`} aria-label={t("nav.devices")}>
            {callers.map((caller) => <button type="button" key={`caller:${caller}`} data-caller={caller} aria-pressed={selectedCaller === caller} onClick={() => selectCaller(caller)} class="graph-list-row"><Icon name="cloud"/><strong>{caller}</strong><span>{t("topology.observedCaller")}</span></button>)}
            {visible.map((p) => <button type="button" key={peerKey(p)} aria-pressed={peerKey(picked) === peerKey(p)} onClick={() => select(peerKey(p))} class="graph-list-row"><Icon name="device"/><strong>{p.name}</strong><span>{connectionText(p, t)}</span></button>)}
          </div>
          {peers.length === 1 && ctx.can("peers") && <div class="topology-empty"><p>{t("topology.empty")}</p><button type="button" class="btn primary" onClick={onboard}>{t("topology.addDevice")}</button></div>}
          {unplotted > 0 && <button type="button" class="graph-overflow" data-testid="topology-all-devices" onClick={() => ctx.go("devices")}>{t("topology.more", { n: unplotted })}</button>}
          <footer class="topology-footer"><span data-testid="peers">{online} / {total} {t("topology.onlinePeers")}</span><span>{t(`mode.${status.network.mode}`)}</span><span>{t("topology.live")}</span></footer>
        </> : <RuntimeConfiguration ctx={ctx}/>}
      </section>
      <div class="quick-actions">
        <button type="button" onClick={onboard}><Icon name="plus"/><span><strong>{t("topology.addDevice")}</strong><small>{t("topology.addHint")}</small></span><Icon name="chevron"/></button>
        <button type="button" onClick={() => ctx.go("sessions")}><Icon name="terminal"/><span><strong>{t("nav.sessions")}</strong><small>{t("topology.sessions", { n: status.limits.pty_in_use })}</small></span><Icon name="chevron"/></button>
        <button type="button" onClick={() => ctx.go("authz")}><Icon name="shield"/><span><strong>{t("nav.authz")}</strong><small>{t("topology.authHint")}</small></span><Icon name="chevron"/></button>
        <button type="button" onClick={() => ctx.go("config")}><Icon name="sliders"/><span><strong>{t("nav.config")}</strong><small>{t("topology.configHint")}</small></span><Icon name="chevron"/></button>
      </div>
      <section class="activity-panel">
        <div class="section-heading"><h2>{t("topology.activity")}</h2><button class="text-button" type="button" onClick={() => ctx.go("audit")}>{t("nav.audit")}<Icon name="chevron"/></button></div>
        {calls.length === 0 ? <Empty>{t("overview.noCalls")}</Empty> : <div class="activity-list" data-testid="recent-calls">
          {calls.slice(-6).reverse().map((call, i) => <button type="button" key={i} class={`activity-row ${hover && call.device !== peers.find((p) => peerKey(p) === hover)?.name ? "dim" : ""}`} onClick={() => { const p = peers.find((p) => p.name === call.device); if (p) { select(peerKey(p)); setTab("calls"); } }}>
            <i class={`status-dot ${call.status === "error" ? "error" : ""}`}/><strong>{call.oauth_client ?? call.caller}</strong><Icon name="arrow"/><code>{call.tool}</code><Icon name="arrow"/><span>{call.device}</span><span class="activity-result">{call.error ?? formatDuration(call.duration_ms)}</span>
          </button>)}
        </div>}
      </section>
    </div>
    <aside class="node-inspector" data-testid="topology-inspector" aria-label={t("topology.inspector")}>
      <div class="inspector-title"><Icon name={selectedCaller ? "cloud" : "device"}/><h2>{selectedCaller ?? picked.name}</h2></div>
      <p class="inspector-sub">{selectedCaller ? t("topology.observedCaller") : `${connectionText(picked, t)}${picked.os ? ` · ${picked.os}` : ""}`}</p>
      <div class="inspector-tabs" role="tablist" aria-label={t("topology.inspector")}>
        {(["overview", "calls", "config"] as const).map((id) => <button type="button" key={id} role="tab" aria-selected={tab === id} onClick={() => { setTab(id); if (id !== "calls") setSelectedCaller(undefined); }} data-testid={`inspector-tab-${id}`}>{t(id === "overview" ? "topology.summary" : id === "calls" ? "topology.calls" : "topology.configuration")}</button>)}
      </div>
      <div role="tabpanel">
        {tab === "overview" && <>
          <div class="row inspector-tags">{picked.self && status.roles.map((r) => <Badge key={r} kind="info">{r}</Badge>)}{picked.tags.map((x) => <Badge key={x}>{x}</Badge>)}</div>
          <h3>{t("topology.connections")}</h3>
          <dl class="inspector-kv"><dt>{t("col.connection")}</dt><dd>{connectionText(picked, t)}</dd><dt>{t("col.addrs")}</dt><dd class="mono">{picked.addrs.join("\n") || "—"}</dd><dt>{t("topology.kernel")}</dt><dd class="mono">{picked.version ?? "—"}</dd><dt>{t("col.lastSeen")}</dt><dd>{picked.self || picked.online ? t("devices.online") : lastSeen(picked, t)}</dd></dl>
          {picked.info?.error && <p class="inspector-warning">{t("devices.infoFailed")}: {picked.info.error}</p>}
          {picked.self ? <>
            {status.gateway && <div class="inspector-section"><h3>Gateway</h3><code class="inspector-url">{status.gateway.url}</code><p class="hint">{t("topology.addresses", { n: status.gateway.addresses.length })}</p><button type="button" class="btn" onClick={copy}><Icon name="copy"/>{t("overview.copyCodex")}</button><button type="button" class="text-button" onClick={() => { connect(); document.getElementById("gateway-panel")?.scrollIntoView({ block: "start", behavior: "smooth" }); }}>{t("topology.manageGateway")}<Icon name="chevron"/></button></div>}
            <div class="inspector-section"><h3>{t("overview.quota")}</h3><Meter used={status.limits.pty_in_use} max={status.limits.pty_max_sessions} label={t("overview.pty")}/><Meter used={status.limits.exec_shared_in_use} max={status.limits.exec_shared} label={t("overview.execShared")}/><Meter used={status.limits.exec_exclusive_in_use} max={status.limits.exec_exclusive} label={t("overview.execExclusive")}/></div>
          </> : <button type="button" class="btn" onClick={() => ctx.go("mesh")}>{t("topology.manageDevice")}<Icon name="chevron"/></button>}
        </>}
        {tab === "calls" && <><p class="hint">{t("topology.callHint")}</p>{scopedCalls.length === 0 ? <Empty>{t("overview.noCalls")}</Empty> : scopedCalls.map((call, i) => <div key={i} class="inspector-call"><strong>{call.oauth_client ?? call.caller}</strong><code>{call.tool}</code><Badge kind={call.status === "ok" ? "ok" : "danger"}>{call.error ?? formatDuration(call.duration_ms)}</Badge></div>)}</>}
        {tab === "config" && <><h3>{t("topology.configuration")}</h3><p class="hint">{t(picked.self ? "topology.localConfig" : "topology.remoteConfig")}</p><button type="button" class="btn primary" onClick={() => picked.self ? ctx.go("config") : ctx.go("mesh")}>{t(picked.self ? "topology.editConfig" : "topology.manageDevice")}<Icon name="chevron"/></button></>}
      </div>
    </aside>
  </div>;
}

function Relay({ point, region, onClick }: { point: Point; region: string; onClick(): void }): JSX.Element {
  const t = useT();
  const ref = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => { ref.current?.style.setProperty("--x", `${point.x / 8}%`); ref.current?.style.setProperty("--y", `${point.y / 5.6}%`); }, [point.x, point.y]);
  return <button type="button" ref={ref} class="relay-node" onClick={onClick}><Icon name="network"/><span>DERP · {region}</span><small>{t("topology.relay")}</small></button>;
}

function connectionText(p: Peer, t: ReturnType<typeof useT>): string {
  if (p.self) return t("devices.self");
  if (!p.online) return t("devices.offline");
  if (p.connection === "direct") return t("conn.direct");
  if (p.connection === "derp") return t("conn.derp", { region: p.derp_region ?? "?" });
  return t("topology.pending");
}
function lastSeen(p: Peer, t: ReturnType<typeof useT>): string {
  const when = ago(p.last_seen);
  return when ? t(`ago.${when.unit}`, { n: when.n }) : "—";
}

/** Connections between local roles, workspaces and backends in the running snapshot. */
function RuntimeConfiguration({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  const c = ctx.snapshot.connection;
  if (c.kind !== "connected") return null;
  const s = c.status;
  return <div class="runtime-config" data-testid="runtime-configuration">
    <p class="hint">{t("topology.runtimeHint")}</p>
    <div class="config-map">
      <div class="config-root"><Icon name="device"/><strong>{s.hostname}</strong><small>{t("topology.running")}</small></div>
      <div class="config-branches">
        <button type="button" class="config-branch" onClick={() => ctx.go("config", { section: "roles" })}><Icon name="network"/><strong>{t("settings.rolesTitle")}</strong><span>{s.roles.join(" · ")}</span></button>
        {s.gateway && <button type="button" class="config-branch" onClick={() => ctx.go("settings", { section: "roles", role: "gateway" })}><Icon name="globe"/><strong>Gateway</strong><code>{s.gateway.listen}</code><span>{s.gateway.access.join(" · ")}</span></button>}
        {s.ingress && <button type="button" class="config-branch" onClick={() => ctx.go("ingress")}><Icon name="shield"/><strong>{t("nav.ingress")}</strong><span>{s.ingress.issuer ?? s.ingress.listen}</span></button>}
        <button type="button" class="config-branch" onClick={() => ctx.go("config", { section: "workdirs" })}><Icon name="folder"/><strong>{t("settings.workdirsTitle")}</strong>{s.workspaces.length === 0 ? <span>{t("topology.noWorkspaces")}</span> : s.workspaces.map((w) => <span key={w.alias} class={w.error ? "text-danger" : ""}><b>{w.alias}</b><code>{w.path}</code>{w.error}</span>)}</button>
        <button type="button" class="config-branch" onClick={() => ctx.go("authz")}><Icon name="code"/><strong>{t("topology.backends")}</strong>{s.backends.length === 0 ? <span>{t("topology.noBackends")}</span> : s.backends.map((b) => <span key={b.name}><b>{b.name}</b><Badge kind={b.state === "running" ? "ok" : b.state === "crashed" ? "danger" : ""}>{b.state}</Badge>{b.policy}</span>)}</button>
      </div>
    </div>
    <button type="button" class="btn" onClick={() => ctx.go("config")}>{t("topology.editConfig")}<Icon name="chevron"/></button>
  </div>;
}
