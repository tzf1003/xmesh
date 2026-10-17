import type { JSX } from "preact";
import { useEffect, useMemo, useState } from "preact/hooks";
import type { Capability } from "../shared/control";
import { hasCapability } from "../shared/control";
import { deriveIcon, type Snapshot, type TrayIcon } from "../shared/state";
import { getApi } from "./api";
import { Badge, Banner, Toasts, useToasts } from "./components";
import { useSnapshot } from "./hooks";
import { I18nContext, makeT, useT, type T } from "./i18n";
import type { Key } from "./messages";
import type { Role, Section } from "./lib/settings";
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

export type PageId = "overview" | "devices" | "sessions" | "authz" | "ingress" | "settings" | "config" | "audit" | "mesh" | "setup";

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

/** What a page needs from the kernel before it is worth showing. */
export interface PageContext {
  snapshot: Snapshot;
  /** `undefined` while the kernel is not connected. */
  can(capability: Capability): boolean;
  toast(text: string, error?: boolean): void;
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
  const navigate = (next: PageId, nextFocus?: PageFocus): void => {
    setFocus(nextFocus);
    setPage(next);
  };

  useEffect(() => {
    document.documentElement.lang = snapshot.settings.locale === "en" ? "en" : "zh-CN";
  }, [snapshot.settings.locale]);

  const connected = snapshot.connection.kind === "connected";
  // With no node configured at all, the first thing worth showing is the setup wizard.
  const current: PageId =
    page ?? (snapshot.connection.kind === "offline" && snapshot.instances.length === 0 && snapshot.stoppedNodes.length === 0 ? "setup" : "overview");

  const ctx: PageContext = {
    snapshot,
    can: (c) => snapshot.connection.kind === "connected" && hasCapability(snapshot.connection.status.control.capabilities, c),
    toast: push,
    focus,
    go: navigate,
  };

  return (
    <I18nContext.Provider value={{ t, locale: snapshot.settings.locale }}>
      <div class="shell">
        <Sidebar current={current} connected={connected} snapshot={snapshot} onSelect={(p) => navigate(p)} />
        <div class="main">
          <TopBar snapshot={snapshot} />
          <main class="content" id="content" tabIndex={-1}>
            <PageSwitch page={current} ctx={ctx} />
          </main>
        </div>
      </div>
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
      <div class="brand">xmesh</div>
      {NAV.filter((n) => n.id !== "setup" || !configured).map((n) => (
        <button
          key={n.id}
          type="button"
          class="nav-item"
          data-testid={`nav-${n.id}`}
          aria-current={current === n.id ? "page" : undefined}
          disabled={!connected && n.id !== "setup" && n.id !== "overview" && n.id !== "mesh"}
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

function TopBar({ snapshot }: { snapshot: Snapshot }): JSX.Element {
  const t = useT();
  const icon = deriveIcon(snapshot.connection);
  const c = snapshot.connection;
  return (
    <header class="topbar">
      <div class="title">
        <strong data-testid="node">{c.kind === "connected" ? c.status.hostname : "xmesh"}</strong>
      </div>
      <Badge kind={STATUS_KIND[icon]}>
        <span data-testid="state" data-state={icon}>
          {t(STATUS_KEY[icon])}
        </span>
      </Badge>
    </header>
  );
}

function PageSwitch({ page, ctx }: { page: PageId; ctx: PageContext }): JSX.Element {
  const t = useT();
  const c = ctx.snapshot.connection;
  if (page === "setup") return <Wizard ctx={ctx} />;
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

function Unavailable({ ctx, t, kind }: { ctx: PageContext; t: T; kind: "offline" | "mismatch" }): JSX.Element {
  const c = ctx.snapshot.connection;
  return (
    <div class="page">
      <h1>{kind === "offline" ? t("offline.title") : t("mismatch.title")}</h1>
      {kind === "offline" ? (
        <Banner testid="offline">
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
