import { Features } from "./pages/Features";
import { useQuickCopy } from "./onboarding-copy";
import { setupNeedsAttention } from "../shared/onboarding";
import type { JSX } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import type { Capability } from "../shared/control";
import { hasCapability } from "../shared/control";
import { deriveIcon, EMPTY_SNAPSHOT, type Snapshot, type TrayIcon } from "../shared/state";
import { getApi } from "./api";
import { ApplyBar } from "./apply-bar";
import { Badge, Banner, Confirm, Toasts, useToasts } from "./components";
import type { PanelTarget } from "../shared/tray";
import { peerKey } from "./lib/topology";
import { DisplayDialog, applyDisplay } from "./display";
import { useSnapshot } from "./hooks";
import { I18nContext, makeT, useT, type T } from "./i18n";
import type { Key } from "./messages";
import type { SaveOutcome } from "./lib/reload";
import type { Role, Section } from "./lib/settings";
import { About, outcomeText } from "./pages/About";
import { Audit } from "./pages/Audit";
import { Authorization } from "./pages/Authorization";
import { Ci } from "./pages/Ci";
import { Computer } from "./pages/Computer";
import { Devices } from "./pages/Devices";
import { Ingress } from "./pages/Ingress";
import { Mesh } from "./pages/Mesh";
import { Runtime } from "./pages/Runtime";
import { Overview } from "./pages/Overview";
import { Settings } from "./pages/Settings";
import { Terminal } from "./pages/Terminal";
import { Transfers } from "./pages/Transfers";
import { Playground } from "./pages/Playground";
import { Sessions } from "./pages/Sessions";
import { Wizard } from "./pages/Wizard";
import { Configuration } from "./pages/Configuration";
import { RoleSwitches } from "./role-switches";
import { Icon } from "./icons";
import { CommandPalette, NAVIGATION, pageAvailable } from "./navigation";

export type PageId = "features" | "computer" | "runtime" | "terminal" | "transfers" | "playground" | "overview" | "devices" | "sessions" | "authz" | "ingress" | "ci" | "settings" | "config" | "audit" | "mesh" | "about" | "setup";

/** Where a link into a page lands: a section, and for the ingress role, the role preselected. */
export interface PageFocus {
  device?: string;
  section?: Section;
  role?: Role;
  peer?: string;
  meshTab?: "devices" | "onboard" | "policy" | "matrix" | "derp";
  configView?: "form" | "source";
}

const STATUS_KEY: Record<TrayIcon, Key> = {
  offline: "status.offline",
  mismatch: "status.mismatch",
  ok: "status.ok",
  degraded: "status.degraded",
  draining: "status.draining",
  restart: "status.restart",
};
const STATUS_KIND: Record<TrayIcon, "" | "ok" | "warn" | "danger"> = {
  offline: "",
  mismatch: "danger",
  ok: "ok",
  degraded: "warn",
  draining: "warn",
  restart: "warn",
};

/** A save that left something to do: a restart, a setting the kernel did not take, a file it could not load. */
function outstanding(o: SaveOutcome): boolean {
  return o.restart || o.applyErrors.length > 0 || o.loadError !== undefined;
}

/** What a page needs from the kernel before it is worth showing. */
export interface PageContext {
  snapshot: Snapshot;
  configDirty: boolean;
  configRevision: number;
  setConfigDirty(dirty: boolean): void;
  refreshConfig(): void;
  /** UI-only positions kept per instance across page navigation. */
  topologySlots: Map<string, Map<string, number>>;
  /** `undefined` while the kernel is not connected. */
  can(capability: Capability): boolean;
  toast(text: string, error?: boolean): void;
  /**
   * A page reports what its save returned, and the shell says whether the kernel has taken it: applied, waiting for a
   * restart (with the restart button), or not applied. `undefined` withdraws the "applied" of the last save, for a form
   * that changed again; what is still outstanding from it stays.
   */
  applied(outcome: SaveOutcome | undefined): void;
  /** Where the page opens, when the link to it named a place. Cleared by any other navigation. */
  focus: PageFocus | undefined;
  go(page: PageId, focus?: PageFocus): void;
}

export function App(): JSX.Element {
  const snapshot = useSnapshot();
  const topologySlots = useRef(new Map<string, Map<string, number>>());
  const t = useMemo(() => makeT(snapshot.settings.locale), [snapshot.settings.locale]);
  const [page, setPage] = useState<PageId | undefined>(undefined);
  const [focus, setFocus] = useState<PageFocus | undefined>(undefined);
  const [configDirty, setConfigDirty] = useState(false);
  const [configRevision, setConfigRevision] = useState(0);
  const [pendingNavigation, setPendingNavigation] = useState<PanelTarget>();
  const externalNavigation = useRef<(target: PanelTarget) => void>(() => {});
  const { toasts, push } = useToasts();
  // The answer to the last save, until the user moves on. What the kernel is still waiting on outlives it (see ApplyBar).
  const [fresh, setFresh] = useState<SaveOutcome | undefined>(undefined);
  const navigate = (next: PageId, nextFocus?: PageFocus): void => {
    if (next !== current) setConfigDirty(false);
    setFocus(nextFocus);
    setPage(next);
    setFresh(undefined);
  };
  useEffect(() => { setFresh(undefined); setConfigDirty(false); }, [snapshot.selectedInstance]);

  useEffect(() => {
    document.documentElement.lang = snapshot.settings.locale === "en" ? "en" : "zh-CN";
  }, [snapshot.settings.locale]);

  const [showDisplay, setShowDisplay] = useState(false);
  const [showSearch, setShowSearch] = useState(false);
  useEffect(() => {
    const key = (e: KeyboardEvent): void => {
      if (document.querySelector('.dialog:not([data-testid="command-palette"])')) return;
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") { e.preventDefault(); setShowSearch((open) => !open); }
    };
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, []);
  const { display } = snapshot.settings;
  useEffect(() => applyDisplay(display), [display.uiFont, display.codeFont]);

  const connected = snapshot.connection.kind === "connected";
  // With no node configured at all, the first thing worth showing is the setup wizard.
  const current: PageId =
    page ?? (snapshot.connection.kind === "offline" && snapshot.instances.length === 0 && snapshot.stoppedNodes.length === 0 ? "setup" : "overview");
  const applyNavigation = (target: PanelTarget): void => {
    if (target.instance !== snapshot.selectedInstance) return;
    const c = snapshot.connection;
    const peer = c.kind === "connected" ? c.peers.find(p => p.name === target.peer) : undefined;
    navigate(target.page, { ...(peer && { peer: peerKey(peer) }), ...(target.device && { device: target.device }) });
    setPendingNavigation(undefined);
  };
  externalNavigation.current = target => { if (configDirty) setPendingNavigation(target); else applyNavigation(target); };
  useEffect(() => getApi().onNavigate(target => externalNavigation.current(target)), []);
  useEffect(() => { if (snapshot !== EMPTY_SNAPSHOT) void getApi().navigationReady(configDirty); }, [configDirty, snapshot !== EMPTY_SNAPSHOT]);
  const [meshVisited, setMeshVisited] = useState(false);
  useEffect(() => { if (current === "mesh") setMeshVisited(true); }, [current]);

  const ctx: PageContext = {
    snapshot,
    configDirty,
    configRevision,
    setConfigDirty,
    refreshConfig: () => setConfigRevision((v) => v + 1),
    topologySlots: topologySlots.current,
    can: (c) => snapshot.connection.kind === "connected" && hasCapability(snapshot.connection.status.control.capabilities, c),
    toast: push,
    applied: (outcome) => setFresh((last) => outcome ?? (last && outstanding(last) ? last : undefined)),
    focus,
    go: navigate,
  };

  return (
    <I18nContext.Provider value={{ t, locale: snapshot.settings.locale }}>
      <div class="shell">
        {pendingNavigation && <Confirm title={snapshot.settings.locale === "zh" ? "离开当前页面？" : "Leave this page?"} body={snapshot.settings.locale === "zh" ? "当前页面有未保存的修改。继续将放弃这些修改。" : "This page has unsaved changes. Continuing will discard them."} confirmLabel={snapshot.settings.locale === "zh" ? "放弃并继续" : "Discard and continue"} onCancel={() => setPendingNavigation(undefined)} onConfirm={() => applyNavigation(pendingNavigation)}/>}
        <TopBar ctx={ctx} onDisplay={() => setShowDisplay(true)} onSearch={() => setShowSearch(true)} />
        <div class="body">
          <Sidebar current={current} connected={connected} snapshot={snapshot} onSelect={(p) => navigate(p)} />
          <main class="content" id="content" tabIndex={-1}>
            <UpdateStrip ctx={ctx} current={current} />
            <SetupReminder ctx={ctx} current={current} />
            <StaleState ctx={ctx} />
            <ApplyBar
              fresh={connected ? fresh : undefined}
              status={snapshot.connection.kind === "connected" ? snapshot.connection.status : undefined}
              writable={ctx.can("config.write")}
              onClear={() => setFresh(undefined)}
              toast={push}
            />
            {(meshVisited || current === "mesh") && <div hidden={current !== "mesh"}><Mesh ctx={ctx} /></div>}
            {current !== "mesh" && <PageSwitch key={current === "setup" ? "setup" : `${snapshot.selectedInstance}:${current === "config" || current === "settings" ? configRevision : 0}`} page={current} ctx={ctx} />}
          </main>
        </div>
      </div>
      {showDisplay && <DisplayDialog prefs={display} onClose={() => setShowDisplay(false)} />}
      {showSearch && <CommandPalette snapshot={snapshot} onClose={() => setShowSearch(false)} go={navigate}/>}
      <Toasts toasts={toasts} />
    </I18nContext.Provider>
  );
}

function Sidebar({ current, connected, snapshot, onSelect }: { current: PageId; connected: boolean; snapshot: Snapshot; onSelect: (p: PageId) => void }): JSX.Element {
  const t = useT();
  const [advanced, setAdvanced] = useState(false);
  useEffect(() => { if (NAVIGATION.find((n) => n.id === current)?.group === "advanced") setAdvanced(true); }, [current]);
  const item = (n: (typeof NAVIGATION)[number]): JSX.Element => <button key={n.id} type="button" class="nav-item" title={t(n.label)} data-testid={`nav-${n.id}`} aria-current={current === n.id ? "page" : undefined} disabled={!pageAvailable(n.id, connected)} onClick={() => onSelect(n.id)}><Icon name={n.icon}/><span>{t(n.label)}</span></button>;
  return (
    <nav class="sidebar" aria-label={t("nav.label")}>
      {(["network", "access", "runtime"] as const).map((group) => <div class="nav-group" key={group}><div class="nav-group-label">{t(`nav.${group}`)}</div>{NAVIGATION.filter((n) => n.group === group).map(item)}</div>)}
      <details class="nav-advanced" open={advanced} onToggle={(e) => setAdvanced(e.currentTarget.open)}>
        <summary data-testid="nav-advanced" aria-label={t("nav.advanced")}><Icon name="sliders"/><span>{t("nav.advanced")}</span><Icon name="chevron"/></summary>
        {NAVIGATION.filter((n) => n.group === "advanced").map(item)}
      </details>
      <div class="spacer" />
      <div class="nav-footer">{NAVIGATION.filter((n) => n.group === "settings").map(item)}</div>
      {snapshot.instances.length > 1 && (
        <div class="node-switch">
          <label>
            <span class="hint">{t("nav.node")}</span>
            <select
              data-testid="instance-select"
              value={snapshot.selectedInstance}
              onChange={(e) => void getApi().selectInstance((e.currentTarget as HTMLSelectElement).value)}
            >
              {snapshot.instances.map((i) => (
                <option key={i.id} value={i.id}>
                  {i.label}
                </option>
              ))}
            </select>
          </label>
        </div>
      )}
    </nav>
  );
}

function TopBar({ ctx, onDisplay, onSearch }: { ctx: PageContext; onDisplay: () => void; onSearch(): void }): JSX.Element {
  const t = useT();
  const { snapshot } = ctx;
  const icon = deriveIcon(snapshot.connection);
  const c = snapshot.connection;
  return (
    <header class="topbar">
      <span class="brand">xmesh</span>
      <div class="title">
        <strong data-testid="node">{c.kind === "connected" ? c.status.hostname : "xmesh"}</strong>
      </div>
      <RoleSwitches key={snapshot.selectedInstance} ctx={ctx} />
      <button type="button" class="search-trigger" onClick={onSearch} aria-label={t("nav.find")} data-testid="open-search"><Icon name="search"/><span>{t("nav.find")}</span><kbd>⌘/Ctrl K</kbd></button>
      <button type="button" class="icon-btn" onClick={onDisplay} title={t("display.open")} aria-label={t("display.open")} data-testid="open-display">
        Aa
      </button>
      <Badge kind={STATUS_KIND[icon]}>
        <span data-testid="state" data-state={icon}>
          {t(icon === "offline" && snapshot.unresponsiveNodes.length > 0 ? "status.unresponsive" : STATUS_KEY[icon])}
        </span>
      </Badge>
    </header>
  );
}

/** A new version, or a failed update, is worth saying on every page; the About page has the details and the button. */
function UpdateStrip({ ctx, current }: { ctx: PageContext; current: PageId }): JSX.Element | null {
  const t = useT();
  const u = ctx.snapshot.update;
  if (current === "about") return null;
  const failed = u.outcome !== undefined && !u.outcome.ok;
  const show = u.phase === "available" || u.phase === "downloading" || u.phase === "installing" || failed;
  if (!show) return null;
  return (
    <div class="page strip">
      <Banner kind={failed && u.phase === "idle" ? "danger" : ""} testid="update-strip">
        <div class="row">
          <span class="grow">
            {failed && u.phase === "idle" && u.outcome ? outcomeText(t, u.outcome) : t("update.banner", { version: u.latest?.version ?? "" })}
          </span>
          <button type="button" class="btn small" onClick={() => ctx.go("about")} data-testid="update-strip-view">
            {t("update.bannerView")}
          </button>
        </div>
      </Banner>
    </div>
  );
}

function PageSwitch({ page, ctx }: { page: PageId; ctx: PageContext }): JSX.Element {
  const t = useT();
  const c = ctx.snapshot.connection;
  if (page === "features") return <Features ctx={ctx} />;
  if (page === "setup") return <Wizard ctx={ctx} />;
  // Updating needs no running kernel: it is how a kernel that is too old gets replaced.
  if (page === "about") return <About ctx={ctx} />;
  // The tailnet is administered through the control plane, so this page does not need a running kernel.
  if (page === "mesh") return <Mesh ctx={ctx} />;
  if (c.kind === "offline") return <Unavailable ctx={ctx} t={t} kind="offline" />;
  if (c.kind === "mismatch") return <Unavailable ctx={ctx} t={t} kind="mismatch" />;
  switch (page) {
    case "overview":
      return <Overview ctx={ctx} />;
    case "runtime":
      return <Runtime ctx={ctx} />;
    case "devices":
      return <Devices ctx={ctx} />;
    case "terminal":
      return <Terminal ctx={ctx} />;
    case "transfers":
      return <Transfers ctx={ctx} />;
    case "playground":
      return <Playground ctx={ctx} />;
    case "sessions":
      return <Sessions ctx={ctx} />;
    case "authz":
      return <Authorization ctx={ctx} />;
    case "ingress":
      return <Ingress ctx={ctx} />;
    case "ci":
      return <Ci ctx={ctx} />;
    case "computer":
      return <Computer ctx={ctx} />;
    case "settings":
      return <Settings ctx={ctx} onDirtyChange={ctx.setConfigDirty} />;
    case "config":
      return <Configuration ctx={ctx} />;
    case "audit":
      return <Audit ctx={ctx} />;
  }
}

/** The panel follows the snapshot once the kernel answers, so only a failure is shown here. */
function startKernel(ctx: PageContext, node: string): void {
  void getApi().startKernel(node).then((r) => {
    if (!r.ok) ctx.toast(r.error.message, true);
  });
}

/** The state file is there but nothing answers: restarting the node's service is the way back. */
function restartUnresponsive(ctx: PageContext, node: string): void {
  void getApi().restartUnresponsive(node).then((r) => {
    if (!r.ok) ctx.toast(r.error.message, true);
  });
}

/** The recorded process is gone: the state file is a leftover, and removing it is the way back. */
function cleanupStale(ctx: PageContext, node: string): void {
  void getApi().cleanupStale(node).then((r) => {
    if (!r.ok) ctx.toast(r.error.message, true);
  });
}

function StaleState({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  if (ctx.snapshot.staleNodes.length === 0) return null;
  return (
    <div class="page strip">
      <Banner testid="stale-state">
        <p>{t("stale.body")}</p>
        <div class="row">
          {ctx.snapshot.staleNodes.map((node) => ctx.snapshot.protectedStaleNodes.includes(node) ? (
            <p key={node}>{t("stale.adminNamed", { name: node })}</p>
          ) : (
            <button key={node} type="button" class="btn" onClick={() => cleanupStale(ctx, node)} data-testid={`cleanup-stale-${node}`}>
              {t("stale.cleanupNamed", { name: node })}
            </button>
          ))}
        </div>
      </Banner>
    </div>
  );
}

function Unavailable({ ctx, t, kind }: { ctx: PageContext; t: T; kind: "offline" | "mismatch" }): JSX.Element {
  const c = ctx.snapshot.connection;
  const unresponsiveNodes = ctx.snapshot.unresponsiveNodes;
  const staleNodes = ctx.snapshot.staleNodes;
  const unresponsive = kind === "offline" && unresponsiveNodes.length > 0;
  return (
    <div class="page">
      <h1>{kind === "offline" ? t(unresponsive ? "unresponsive.title" : "offline.title") : t("mismatch.title")}</h1>
      {kind === "offline" ? (
        <Banner testid={unresponsive ? "unresponsive" : "offline"}>
          {unresponsive ? (
            <>
              <p>{t("unresponsive.body")}</p>
              <div class="row">
                {unresponsiveNodes.map((node) => (
                  <button key={node} type="button" class="btn primary" onClick={() => restartUnresponsive(ctx, node)} data-testid={`restart-unresponsive-${node}`}>
                    {unresponsiveNodes.length === 1 ? t("unresponsive.restart") : t("unresponsive.restartNamed", { name: node })}
                  </button>
                ))}
              </div>
            </>
          ) : null}
          {ctx.snapshot.stoppedNodes.length > 0 ? (
            <>
              <p>{t("offline.startBody")}</p>
              <div class="row">
                {ctx.snapshot.stoppedNodes.map((node) => (
                  <button key={node} type="button" class="btn primary" onClick={() => startKernel(ctx, node)} data-testid={`start-kernel-${node}`}>
                    {ctx.snapshot.stoppedNodes.length === 1 ? t("offline.start") : t("offline.startNamed", { name: node })}
                  </button>
                ))}
              </div>
            </>
          ) : !unresponsive ? (
            <>
              {staleNodes.length === 0 && <p>{t("offline.body")}</p>}
              <button type="button" class="btn primary" onClick={() => ctx.go("setup")} data-testid="goto-setup">
                {t("offline.setup")}
              </button>
            </>
          ) : null}
        </Banner>
      ) : (
        <Banner kind="danger" testid="mismatch">
          <p>{c.kind === "mismatch" ? t("mismatch.body", { kernel: c.kernelVersion || "?", api: c.apiVersion || "?" }) : ""}</p>
          <div class="row">
            <button type="button" class="btn" onClick={() => void getApi().openExternal("docs")}>
              {t("mismatch.upgrade")}
            </button>
            <button type="button" class="btn" onClick={() => void getApi().openPath("logs")}>
              {t("overview.openLogs")}
            </button>
          </div>
        </Banner>
      )}
    </div>
  );
}

function SetupReminder({ ctx, current }: { ctx: PageContext; current: PageId }): JSX.Element | null {
  const copy = useQuickCopy();
  const [pending, setPending] = useState(false);
  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async (): Promise<void> => {
      try {
        const r = await getApi().onboarding({ op: "inspect" });
        if (active && r.ok) setPending(r.data.jobs.some(setupNeedsAttention));
      } catch { /* Keep the last known task state during a transient read failure. */ }
      if (active) timer = setTimeout(() => void refresh(), 3000);
    };
    void refresh();
    return () => { active = false; clearTimeout(timer); };
  }, [current]);
  if (!pending || current === "setup" || current === "features") return null;
  return <div class="page strip"><Banner testid="setup-reminder"><div class="row"><span class="grow">{copy.existing}</span><button class="btn small" onClick={() => ctx.go("setup")}>{copy.resume}</button></div></Banner></div>;
}
