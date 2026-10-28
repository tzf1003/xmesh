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
import { assignSlots, callBelongsTo, layoutTopology, linkKind, peerKey, toolName, topologyPeers, type CallDirection, type Point } from "../lib/topology";

/** Track online transitions per instance after the initial peer snapshot. */
function usePeerArrivals(instance: string, peers: Peer[] | undefined): Set<string> {
  const previous = useRef<{ instance: string; online: Set<string> }>();
  const [arrivals, setArrivals] = useState(new Set<string>());
  useEffect(() => {
    const online = new Set(peers?.filter((p) => !p.self && p.online).map(peerKey));
    if (!peers || previous.current?.instance !== instance) {
      setArrivals(new Set());
    } else {
      const joined = [...online].filter((id) => !previous.current!.online.has(id));
      if (joined.length) setArrivals((current) => new Set([...current, ...joined]));
    }
    previous.current = peers ? { instance, online } : undefined;
  }, [instance, peers]);
  useEffect(() => {
    if (!arrivals.size) return;
    const timer = setTimeout(() => setArrivals(new Set()), 850);
    return () => clearTimeout(timer);
  }, [arrivals]);
  return arrivals;
}

/** HTML controls over an SVG edge layer keep the graph keyboard accessible. */
function Node({ id, point, selected, kind, title, sub, children, onSelect, onHover }: {
  id: string; point: Point; selected: boolean; kind: string; title: string; sub: string;
  children?: JSX.Element; onSelect(): void; onHover(id: string | undefined): void;
}): JSX.Element {
  const ref = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => {
    // CSSOM works with the panel's strict style-src policy.
    ref.current?.style.setProperty("--x", `${point.x}px`);
    ref.current?.style.setProperty("--y", `${point.y}px`);
  }, [point.x, point.y]);
  return <button type="button" ref={ref} class={`graph-node ${kind}`} aria-pressed={selected} data-node={id}
    onClick={onSelect} onMouseEnter={() => onHover(id)} onMouseLeave={() => onHover(undefined)}
    onFocus={() => onHover(id)} onBlur={() => onHover(undefined)} title={`${title} · ${sub}`}>
    {children}<span class="graph-node-name"><Icon name="device"/><strong>{title}</strong></span>
    <span class="graph-node-sub"><i class="status-dot"/>{sub}</span>
  </button>;
}

export function Topology({ ctx, calls }: { ctx: PageContext; calls: CallEventData[] }): JSX.Element | null {
  const t = useT();
  const c = ctx.snapshot.connection;
  const arrivals = usePeerArrivals(ctx.snapshot.selectedInstance, c.kind === "connected" && ctx.can("peers") ? c.peers : undefined);
  const [selected, setSelected] = useState<string | undefined>();
  const [direction, setDirection] = useState<CallDirection>("all");
  const stage = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(800);
  const [hover, setHover] = useState<string | undefined>();
  const [onlyOnline, setOnlyOnline] = useState(false);
  const [tag, setTag] = useState("");
  const [mode, setMode] = useState<"connections" | "configuration">("connections");
  const [tab, setTab] = useState<"overview" | "calls" | "config">("overview");
  useEffect(() => {
    if (ctx.focus?.peer) { setSelected(ctx.focus.peer); setMode("connections"); setOnlyOnline(false); setTag(""); }
  }, [ctx.focus]);
  useLayoutEffect(() => {
    const el = stage.current?.parentElement;
    if (!el) return;
    // The panel remains measurable while the canvas uses its compact fallback.
    const measure = (): void => setWidth(Math.max(0, el.clientWidth - 16));
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    measure();
    return () => observer.disconnect();
  }, [mode, c.kind]);
  useLayoutEffect(() => {
    stage.current?.style.setProperty("height", `${stage.current.dataset.height}px`);
  });
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
  // Lay out the full snapshot before filtering so controls preserve device geometry.
  const layout = layoutTopology(peers, slots, width);
  const activeSource = layout.edges.find(edge => edge.to === active)?.from;
  const plotted = layout.nodes.flatMap(n => n.peer && !n.peer.self ? [n.peer] : []);
  const unplotted = peers.length - 1 - plotted.length;
  const visibleIds = new Set(visible.map(peerKey));
  const visibleRelays = new Set(layout.edges.filter(e => visibleIds.has(e.to)).map(e => e.from));
  const online = ctx.can("peers") ? peers.filter((p) => !p.self && p.online).length : status.peers.online;
  const total = ctx.can("peers") ? peers.length - 1 : status.peers.total;
  const scopedCalls = calls.filter(call => callBelongsTo(call, picked, direction)).slice(-12).reverse();
  const select = (id: string): void => { setSelected(id); setTab("overview"); };
  const copy = (): void => {
    if (!status.gateway) return;
    void getApi().copyGateway({ kind: "codexConfig", url: status.gateway.url }).then((r) => ctx.toast(r.ok ? t("overview.copied") : errorText(t, r.error), !r.ok));
  };
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
          <div ref={stage} class={`graph-stage ${width < 560 ? "graph-dense" : ""}`} data-height={layout.height} data-testid="topology-canvas" onClick={(e) => { if (e.target === e.currentTarget) select(selfId); }}>
            <svg class="graph-edges" viewBox={`0 0 ${layout.width} ${layout.height}`} preserveAspectRatio="none" aria-hidden="true">
              {layout.edges.filter(edge => visibleIds.has(edge.to) || visibleRelays.has(edge.to)).map(edge => <g key={edge.id} data-edge={edge.to} data-kind={edge.kind} class={active !== selfId && active !== edge.to && active !== edge.from && activeSource !== edge.to ? "edge-dim" : ""}>
                <path class={`graph-link ${edge.kind}`} d={edge.path}/>
                {(edge.kind === "direct" || edge.kind === "derp") && <path class={`graph-flow ${edge.kind}`} d={edge.path}/>}
              </g>)}
            </svg>
            {layout.nodes.map(node => node.peer ? visibleIds.has(node.id) && <Node key={node.id} id={node.id} point={node} selected={peerKey(picked) === node.id}
              kind={node.peer.self ? `self state-${deriveIcon(c)}` : `${linkKind(node.peer)}${node.peer.online && arrivals.has(node.id) ? " arriving" : ""}`}
              title={node.peer.name} sub={node.peer.self ? status.roles.join(" + ") : connectionText(node.peer, t)} onSelect={() => select(node.id)} onHover={setHover}>
              {node.peer.self ? <span class="node-role">{t("devices.self")}</span> : node.peer.info?.error ? <span class="node-role issue" title={node.peer.info.error}>{t("devices.infoFailed")}</span> : undefined}
            </Node> : visibleRelays.has(node.id) && <Relay key={node.id} id={node.id} point={node} region={node.region!} onHover={setHover} onClick={() => ctx.go("mesh", { meshTab: "derp" })}/>)}
          </div>
          <div class={`graph-list ${width < 560 ? "graph-dense-list" : ""}`} aria-label={t("nav.devices")}>
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
    </div>
    <aside class="node-inspector" data-testid="topology-inspector" aria-label={t("topology.inspector")}>
      <div class="inspector-title"><Icon name="device"/><h2>{picked.name}</h2></div>
      <p class="inspector-sub">{`${connectionText(picked, t)}${picked.os ? ` · ${picked.os}` : ""}`}</p>
      <div class="inspector-tabs" role="tablist" aria-label={t("topology.inspector")}>
        {(["overview", "calls", "config"] as const).map((id) => <button type="button" key={id} role="tab" aria-selected={tab === id} onClick={() => setTab(id)} data-testid={`inspector-tab-${id}`}>{t(id === "overview" ? "topology.summary" : id === "calls" ? "topology.calls" : "topology.configuration")}</button>)}
      </div>
      <div role="tabpanel">
        {tab === "overview" && <>
          <div class="row inspector-tags">{picked.self && status.roles.map((r) => <Badge key={r} kind="info">{r}</Badge>)}{picked.tags.map((x) => <Badge key={x}>{x}</Badge>)}</div>
          <h3>{t("topology.connections")}</h3>
          <dl class="inspector-kv"><dt>{t("col.connection")}</dt><dd>{connectionText(picked, t)}</dd>{!!picked.links?.length&&<><dt>{t("device.links")}</dt><dd>{picked.links.map((l) => <div key={`${l.direction}-${l.kind}`}>{t(l.direction === "outgoing" ? "device.role.client" : "device.role.server")} · {t(`mode.${l.kind}`)}</div>)}</dd></>}<dt>{t("col.addrs")}</dt><dd class="mono">{picked.addrs.join("\n") || "—"}</dd><dt>{t("topology.kernel")}</dt><dd class="mono">{picked.version ?? "—"}</dd><dt>{t("col.lastSeen")}</dt><dd>{picked.self || picked.online ? t("devices.online") : lastSeen(picked, t)}</dd></dl>
          {picked.info?.error && <p class="inspector-warning">{t("devices.infoFailed")}: {picked.info.error}</p>}
          {picked.self ? <>
            {status.gateway && <div class="inspector-section"><h3>Gateway</h3><code class="inspector-url">{status.gateway.url}</code><p class="hint">{t("topology.addresses", { n: status.gateway.addresses.length })}</p><button type="button" class="btn" onClick={copy}><Icon name="copy"/>{t("overview.copyCodex")}</button><button type="button" class="text-button" onClick={() => ctx.go("runtime")}>{t("topology.manageGateway")}<Icon name="chevron"/></button></div>}
            <div class="inspector-section"><h3>{t("overview.quota")}</h3><Meter used={status.limits.pty_in_use} max={status.limits.pty_max_sessions} label={t("overview.pty")}/><Meter used={status.limits.exec_shared_in_use} max={status.limits.exec_shared} label={t("overview.execShared")}/><Meter used={status.limits.exec_exclusive_in_use} max={status.limits.exec_exclusive} label={t("overview.execExclusive")}/></div>
          </> : <button type="button" class="btn" onClick={() => ctx.go("mesh")}>{t("topology.manageDevice")}<Icon name="chevron"/></button>}
        </>}
        {tab === "calls" && <>
          <div class="segs inspector-call-filters" aria-label={t("topology.callDirection")}>
            {(["all", "initiated", "executed"] as const).map(id => <button key={id} type="button" aria-pressed={direction === id} onClick={() => setDirection(id)} data-testid={`calls-${id}`}>{t(`topology.${id}`)}</button>)}
          </div>
          <p class="hint">{t("topology.callHint")}</p>
          {scopedCalls.length === 0 ? <Empty>{t("overview.noCalls")}</Empty> : scopedCalls.map((call, i) => <div key={i} class="inspector-call" data-testid="inspector-call">
            <div><code>{toolName(call.tool)}</code><span class={`inspector-call-direction ${callBelongsTo(call, picked, "initiated") ? "initiated" : "executed"}`}>{t(callBelongsTo(call, picked, "initiated") ? "topology.initiated" : "topology.executed")}</span></div>
            <Badge kind={call.status === "ok" ? "ok" : "danger"}>{call.error ?? formatDuration(call.duration_ms)}</Badge>
            <button type="button" class="text-button" aria-label={`${t("topology.viewLogs")} · ${toolName(call.tool)}`} onClick={() => ctx.go("sessions", { sessionsTab: "history", device: call.device, auditTool: call.tool })}>{t("topology.viewLogs")}<Icon name="terminal"/></button>
          </div>)}
        </>}
        {tab === "config" && <><h3>{t("topology.configuration")}</h3><p class="hint">{t(picked.self ? "topology.localConfig" : "topology.remoteConfig")}</p><button type="button" class="btn primary" onClick={() => picked.self ? ctx.go("config") : ctx.go("mesh")}>{t(picked.self ? "topology.editConfig" : "topology.manageDevice")}<Icon name="chevron"/></button></>}
      </div>
    </aside>
  </div>;
}

function Relay({ id, point, region, onClick, onHover }: { id: string; point: Point; region: string; onClick(): void; onHover(id: string | undefined): void }): JSX.Element {
  const t = useT();
  const ref = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => { ref.current?.style.setProperty("--x", `${point.x}px`); ref.current?.style.setProperty("--y", `${point.y}px`); }, [point.x, point.y]);
  return <button type="button" ref={ref} class="relay-node" onClick={onClick}
    onMouseEnter={() => onHover(id)} onMouseLeave={() => onHover(undefined)}
    onFocus={() => onHover(id)} onBlur={() => onHover(undefined)}><Icon name="network"/><span>DERP · {region}</span><small>{t("topology.relay")}</small></button>;
}

function connectionText(p: Peer, t: ReturnType<typeof useT>): string {
  if (p.self) return t("devices.self");
  if (p.connection_state === "unstable") return t("device.unstable");
  if (p.connection_state === "reconnecting") return t("device.reconnecting");
  if (p.connection_state === "identity_conflict") return t("device.conflict");
  if (!p.online) return t("devices.offline");
  if (p.active_path === "lan") return t("mode.lan");
  if (p.active_path === "direct") return t("mode.direct");
  if (p.active_path === "cloudflare") return t("mode.cloudflare");
  if (p.active_path === "tailnet") return `${t("mode.tailnet")} · ${p.connection === "derp" ? t("conn.derp", {region: p.derp_region ?? "?"}) : t("conn.direct")}`;
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
