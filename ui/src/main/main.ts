import * as fs from "node:fs";
import * as path from "node:path";
import { Notification, app } from "electron";
import { resolveLocale, translator } from "../shared/i18n";
import { type Snapshot } from "../shared/state";
import { Actions } from "./actions";
import { ShellController } from "./controller";
import { discoverInstances, stateRoot } from "./discovery";
import { registerIpc } from "./ipc";
import { type Action } from "./menu";
import { diffNotices, noticeText } from "./notifications";
import { type RecordedCall, electronPlatform, recordingPlatform } from "./platform";
import { registerPanelScheme, servePanel } from "./protocol";
import { TrayView } from "./tray";
import { type PanelWindow, createPanelWindow, installSessionHardening, installWebContentsHardening } from "./window";

const UI_ROOT = path.join(__dirname, "..", "..", "..");
const TEST_MODE = process.env.XMESH_UI_TEST === "1" && !app.isPackaged;

/** `dist/renderer` is the panel build (W1); until it exists the placeholder page is served. */
function rendererRoot(): string {
  const built = path.join(UI_ROOT, "dist", "renderer");
  return fs.existsSync(path.join(built, "index.html")) ? built : path.join(UI_ROOT, "static", "placeholder-panel");
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

  const t = translator(resolveLocale(process.env.XMESH_UI_LANG));
  const root = stateRoot();
  let panel: PanelWindow | undefined;
  const showPanel = (): void => {
    if (!panel || panel.window.isDestroyed()) panel = createPanelWindow();
    panel.show();
  };
  app.on("second-instance", showPanel);

  const controller = new ShellController({
    discover: () => discoverInstances(root),
    getLoginItem: () => app.getLoginItemSettings().openAtLogin,
    setLoginItem: (openAtLogin) => app.setLoginItemSettings({ openAtLogin }),
    ...(TEST_MODE && { pollMs: 150, reconnectMs: 100, refreshDelayMs: 20, streamIdleMs: 2000 }),
  });

  const calls: RecordedCall[] = [];
  const answers = { confirm: true, cliFails: false };
  const notified: string[] = [];
  const notifyError = (message: string): void => notify(message);
  const platform = TEST_MODE
    ? recordingPlatform(showPanel, calls, answers)
    : electronPlatform(showPanel, notifyError);
  const actions = new Actions(controller, platform, t);

  function notify(body: string): void {
    notified.push(body);
    if (TEST_MODE || !Notification.isSupported()) return;
    new Notification({ title: t("notice.title"), body }).show();
  }

  const runAction = (a: Action): void => {
    void actions.dispatch(a);
  };
  const tray = new TrayView(t, runAction, showPanel);

  servePanel(rendererRoot());
  registerIpc({ controller, actions, panel: () => (panel && !panel.window.isDestroyed() ? panel.window.webContents : undefined) });

  let prev: Snapshot = controller.snapshot;
  tray.update(prev);
  controller.on("change", (snap) => {
    tray.update(snap);
    for (const n of diffNotices(prev, snap)) notify(noticeText(n, t));
    prev = snap;
  });

  app.on("before-quit", () => {
    void controller.stop();
  });
  app.on("will-quit", () => tray.destroy());

  controller.start();

  if (TEST_MODE) {
    // Reachable only from Playwright's main-process evaluate; never set in a packaged app.
    (globalThis as Record<string, unknown>).__xmeshUiTest = {
      tray: () => tray.describe(),
      snapshot: () => controller.snapshot,
      calls: () => calls.slice(),
      notices: () => notified.slice(),
      answers,
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
