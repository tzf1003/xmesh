import * as path from "node:path";
import { Notification, app, powerMonitor } from "electron";
import { resolveLocale, translator } from "../shared/i18n";
import { type Snapshot } from "../shared/state";
import { Actions } from "./actions";
import { ShellController } from "./controller";
import { GatewayCredentials } from "./gateway-token";
import { discoverConfigured, discoverInstances, stateRoots } from "./discovery";
import { registerIpc } from "./ipc";
import { fakeMeshRunner, freshFakeMesh } from "./mesh-fake";
import { MeshService, execMeshRunner } from "./mesh-service";
import { PanelService } from "./panel-service";
import { fsSetupHost, previewSetup, runSetup } from "./setup";
import { runXmeshSetup } from "./cli";
import { type Action } from "./menu";
import { diffNotices, noticeText } from "./notifications";
import { type RecordedCall, electronPlatform, recordingPlatform } from "./platform";
import { registerPanelScheme, servePanel } from "./protocol";
import { Autostart, type LoginItemHint } from "./system/autostart";
import { SeenFlags } from "./system/firstrun";
import { Notifier } from "./system/notifier";
import { TrayView } from "./tray";
import { type PanelWindow, createPanelWindow, installSessionHardening, installWebContentsHardening } from "./window";

const UI_ROOT = path.join(__dirname, "..", "..", "..");
const TEST_MODE = process.env.XMESH_UI_TEST === "1" && !app.isPackaged;

/** `dist/renderer` is the esbuild bundle of src/renderer (scripts/build-renderer.mjs). */
function rendererRoot(): string {
  return path.join(UI_ROOT, "dist", "renderer");
}

registerPanelScheme();

if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  start().catch((e: unknown) => {
    console.error("xmesh-ui failed to start:", e);
    app.exit(1);
  });
}

async function start(): Promise<void> {
  if (process.platform === "darwin") app.dock?.hide();
  app.setAppUserModelId("app.xmesh.ui");
  // The tray lives on after the panel closes.
  app.on("window-all-closed", () => {});
  installWebContentsHardening();

  await app.whenReady();
  installSessionHardening();

  const locale = resolveLocale(process.env.XMESH_UI_LANG);
  const t = translator(locale);
  const roots = stateRoots();
  let panel: PanelWindow | undefined;
  const showPanel = (): void => {
    if (!panel || panel.window.isDestroyed()) panel = createPanelWindow();
    panel.show();
  };
  app.on("second-instance", showPanel);

  const notified: string[] = [];
  const notifier = new Notifier({
    supported: () => !TEST_MODE && Notification.isSupported(),
    create: (o) => new Notification(o),
    now: Date.now,
    onClick: showPanel,
    title: t("notice.title"),
    summary: (count) => t("notice.burst", { count }),
  });
  const notify = (items: { key: string; text: string }[]): void => {
    for (const i of items) notified.push(i.text);
    notifier.push(items);
  };

  const autostart = new Autostart(app, { platform: process.platform, packaged: app.isPackaged, execPath: process.execPath });
  const loginHints: Record<LoginItemHint, string | undefined> = {
    unsupported: undefined,
    unpackaged: t("hint.loginItem.unavailable"),
    "requires-approval": t("hint.loginItem.requiresApproval"),
    "disabled-by-user": t("hint.loginItem.disabledByUser"),
  };

  const controller = new ShellController({
    discover: () => discoverInstances(roots),
    configured: () => discoverConfigured(roots),
    locale,
    getLoginItem: () => autostart.get().enabled,
    setLoginItem: (enabled) => {
      const hint = autostart.set(enabled).hint;
      const text = hint === undefined ? undefined : loginHints[hint];
      if (text !== undefined) notify([{ key: `login.${hint}`, text }]);
    },
    ...(TEST_MODE && { pollMs: 150, reconnectMs: 100, refreshDelayMs: 20, streamIdleMs: 2000 }),
  });

  const calls: RecordedCall[] = [];
  const answers: { confirm: boolean; cliFails: boolean; picked?: string } = { confirm: true, cliFails: false };
  const notifyError = (message: string): void => notify([{ key: `error.${message}`, text: message }]);
  const platform = TEST_MODE
    ? recordingPlatform(showPanel, calls, answers)
    : electronPlatform(showPanel, notifyError);
  const actions = new Actions(controller, platform, t, new GatewayCredentials(controller));

  const runAction = (a: Action): void => {
    void actions.dispatch(a);
  };
  const tray = new TrayView(t, runAction, showPanel);

  servePanel(rendererRoot());
  const setupHost = fsSetupHost(roots[0]!, async (args) => {
    if (TEST_MODE) {
      calls.push({ op: "setup-cli", arg: [...args] });
      if (answers.cliFails) throw new Error("simulated cli failure");
      return `ok ${args[0]}`;
    }
    return runXmeshSetup(args);
  });
  // Tailnet administration shells out to `xmesh mesh --json`; the credential lives in that CLI's keychain profile.
  const fakeMesh = TEST_MODE ? freshFakeMesh() : undefined;
  const meshService = new MeshService(fakeMesh ? fakeMeshRunner(fakeMesh) : execMeshRunner(), () => {
    const c = controller.snapshot.connection;
    const file = c.kind === "connected" ? c.status.paths?.config_file : undefined;
    return typeof file === "string" && file !== "" ? file : undefined;
  });
  registerIpc({
    controller,
    actions,
    service: new PanelService(controller),
    previewSetup: (params) => previewSetup(params, setupHost),
    setup: (params) => runSetup(params, setupHost),
    mesh: (request) => meshService.request(request),
    panel: () => (panel && !panel.window.isDestroyed() ? panel.window.webContents : undefined),
  });

  let prev: Snapshot = controller.snapshot;
  tray.update(prev);
  controller.on("change", (snap) => {
    tray.update(snap);
    notify(diffNotices(prev, snap).map((n) => ({ key: n.key, text: noticeText(n, t) })));
    prev = snap;
  });

  app.on("before-quit", () => {
    void controller.stop();
  });
  app.on("will-quit", () => tray.destroy());

  // After sleep or a lock the event stream is stale; do not wait for the idle timeout to notice.
  powerMonitor.on("resume", () => controller.nudge());
  powerMonitor.on("unlock-screen", () => controller.nudge());

  // Windows 11 puts a new tray icon in the overflow flyout; say so once, for the installed app only.
  if (process.platform === "win32" && app.isPackaged && new SeenFlags(path.join(app.getPath("userData"), "seen.json")).markOnce("trayOverflowHint")) {
    notify([{ key: "hint.trayOverflow", text: t("hint.trayOverflow") }]);
  }

  controller.start();

  if (TEST_MODE) {
    // Reachable only from Playwright's main-process evaluate; never set in a packaged app.
    (globalThis as Record<string, unknown>).__xmeshUiTest = {
      tray: () => tray.describe(),
      snapshot: () => controller.snapshot,
      calls: () => calls.slice(),
      notices: () => notified.slice(),
      answers,
      mesh: fakeMesh,
      showPanel,
      panelWebContents: () => panel?.window.webContents,
      panelPreferences: () => panel?.webPreferences,
      panelState: () => ({
        exists: panel !== undefined && !panel.window.isDestroyed(),
        visible: panel !== undefined && !panel.window.isDestroyed() && panel.window.isVisible(),
      }),
      closePanel: () => panel?.window.close(),
      clickMenu: (id: string): Promise<unknown> => {
        const find = (items: ReturnType<TrayView["describe"]>["menu"]): Action | undefined => {
          for (const i of items) {
            if (i.id === id) return i.enabled ? i.action : undefined;
            const n = i.submenu ? find(i.submenu) : undefined;
            if (n) return n;
          }
          return undefined;
        };
        const a = find(tray.describe().menu);
        return a ? actions.dispatch(a) : Promise.reject(new Error(`no enabled action for ${id}`));
      },
    };
  }
}
