import type { JSX } from "preact";
import { useEffect, useMemo, useState } from "preact/hooks";
import type { Capability } from "../shared/control";
import { hasCapability } from "../shared/control";
import { deriveIcon, type Snapshot, type TrayIcon } from "../shared/state";
import { getApi } from "./api";
import { ApplyBar } from "./apply-bar";
import { Badge, Banner, Toasts, useToasts } from "./components";
import { DisplayDialog, applyDisplay } from "./display";
import { useSnapshot } from "./hooks";
import { I18nContext, makeT, useT, type T } from "./i18n";
import type { Key } from "./messages";
import type { SaveOutcome } from "./lib/reload";
import type { Role, Section } from "./lib/settings";
import { About, outcomeText } from "./pages/About";
import { Audit } from "./pages/Audit";
import { Authorization } from "./pages/Authorization";
import { Config } from "./pages/Config";
import { Devices } from "./pages/Devices";
import { Ingress } from "./pages/Ingress";
import { Mesh } from "./pages/Mesh";
import { Overview } from "./pages/Overview";
import { Settings } from "./pages/Settings";
import { Sessions } from "./pages/Sessions";
import { Wizard } from "./pages/Wizard";

export type PageId = "overview" | "devices" | "sessions" | "authz" | "ingress" | "settings" | "config" | "audit" | "mesh" | "about" | "setup";

/** Where a link into a page lands: a section, and for the ingress role, the role preselected. */
export interface PageFocus {
  section: Section;
  role?: Role;
}

const NAV: { id: PageId; label: Key }[] = [
  { id: "overview", label: "nav.overview" },
  { id: "devices", label: "nav.devices" },
  { id: "sessions", label: "nav.sessions" },
  { id: "authz", label: "nav.authz" },
  { id: "ingress", label: "nav.ingress" },
  { id: "settings", label: "nav.settings" },
  { id: "config", label: "nav.config" },
  { id: "audit", label: "nav.audit" },
  { id: "mesh", label: "nav.mesh" },
  { id: "about", label: "nav.about" },
  { id: "setup", label: "nav.setup" },
];

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
  const t = useMemo(() => makeT(snapshot.settings.locale), [snapshot.settings.locale]);
  const [page, setPage] = useState<PageId | undefined>(undefined);
  const [focus, setFocus] = useState<PageFocus | undefined>(undefined);
  const { toasts, push } = useToasts();
  // The answer to the last save, until the user moves on. What the kernel is still waiting on outlives it (see ApplyBar).
  const [fresh, setFresh] = useState<SaveOutcome | undefined>(undefined);
  const navigate = (next: PageId, nextFocus?: PageFocus): void => {
    setFocus(nextFocus);
    setPage(next);
    setFresh(undefined);
  };
  useEffect(() => setFresh(undefined), [snapshot.selectedInstance]);

  useEffect(() => {
    document.documentElement.lang = snapshot.settings.locale === "en" ? "en" : "zh-CN";
  }, [snapshot.settings.locale]);

  const [showDisplay, setShowDisplay] = useState(false);
  const { display } = snapshot.settings;
  useEffect(() => applyDisplay(display), [display.uiFont, display.codeFont]);

  const connected = snapshot.connection.kind === "connected";
  // With no node configured at all, the first thing worth showing is the setup wizard.
  const current: PageId =
    page ?? (snapshot.connection.kind === "offline" && snapshot.instances.length === 0 && snapshot.stoppedNodes.length === 0 ? "setup" : "overview");

  const ctx: PageContext = {
    snapshot,
    can: (c) => snapshot.connection.kind === "connected" && hasCapability(snapshot.connection.status.control.capabilities, c),
    toast: push,
    applied: (outcome) => setFresh((last) => outcome ?? (last && outstanding(last) ? last : undefined)),
    focus,
    go: navigate,
  };

  return (
    <I18nContext.Provider value={{ t, locale: snapshot.settings.locale }}>
      <div class="shell">
        <TopBar snapshot={snapshot} onDisplay={() => setShowDisplay(true)} />
        <div class="body">
          <Sidebar current={current} connected={connected} snapshot={snapshot} onSelect={(p) => navigate(p)} />
          <main class="content" id="content" tabIndex={-1}>
            <UpdateStrip ctx={ctx} current={current} />
            <ApplyBar
              fresh={connected ? fresh : undefined}
              status={snapshot.connection.kind === "connected" ? snapshot.connection.status : undefined}
              writable={ctx.can("config.write")}
              onClear={() => setFresh(undefined)}
              toast={push}
            />
            <PageSwitch page={current} ctx={ctx} />
          </main>
        </div>
      </div>
      {showDisplay && <DisplayDialog prefs={display} onClose={() => setShowDisplay(false)} />}
      <Toasts toasts={toasts} />
    </I18nContext.Provider>
  );
}

function Sidebar({ current, connected, snapshot, onSelect }: { current: PageId; connected: boolean; snapshot: Snapshot; onSelect: (p: PageId) => void }): JSX.Element {
  const t = useT();
  // The wizard is for a node that has no config yet; once one exists, its settings live on the settings page.
  const configured = snapshot.instances.length > 0 || snapshot.stoppedNodes.length > 0;
  return (
    <nav class="sidebar" aria-label={t("nav.label")}>
      {NAV.filter((n) => n.id !== "setup" || !configured).map((n) => (
        <button
          key={n.id}
          type="button"
          class="nav-item"
          data-testid={`nav-${n.id}`}
          aria-current={current === n.id ? "page" : undefined}
          disabled={!connected && n.id !== "setup" && n.id !== "overview" && n.id !== "mesh" && n.id !== "about"}
          onClick={() => onSelect(n.id)}
        >
          {t(n.label)}
        </button>
      ))}
      <div class="spacer" />
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

function TopBar({ snapshot, onDisplay }: { snapshot: Snapshot; onDisplay: () => void }): JSX.Element {
  const t = useT();
  const icon = deriveIcon(snapshot.connection);
  const c = snapshot.connection;
  return (
    <header class="topbar">
      <span class="brand">xmesh</span>
      <div class="title">
        <strong data-testid="node">{c.kind === "connected" ? c.status.hostname : "xmesh"}</strong>
      </div>
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
    case "devices":
      return <Devices ctx={ctx} />;
    case "sessions":
      return <Sessions ctx={ctx} />;
    case "authz":
      return <Authorization ctx={ctx} />;
    case "ingress":
      return <Ingress ctx={ctx} />;
    case "settings":
      return <Settings ctx={ctx} />;
    case "config":
      return <Config ctx={ctx} />;
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

function Unavailable({ ctx, t, kind }: { ctx: PageContext; t: T; kind: "offline" | "mismatch" }): JSX.Element {
  const c = ctx.snapshot.connection;
  const unresponsiveNodes = ctx.snapshot.unresponsiveNodes;
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
          ) : ctx.snapshot.stoppedNodes.length > 0 ? (
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
          ) : (
            <>
              <p>{t("offline.body")}</p>
              <button type="button" class="btn primary" onClick={() => ctx.go("setup")} data-testid="goto-setup">
                {t("offline.setup")}
              </button>
            </>
          )}
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
