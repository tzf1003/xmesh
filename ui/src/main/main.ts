import { createOnboardingService, fakeOnboarding } from "./onboarding";
import * as path from "node:path";
import { clipboard, dialog, Notification, app, globalShortcut, net, powerMonitor, shell } from "electron";
import { resolveLocale, translator } from "../shared/i18n";
import { type Snapshot } from "../shared/state";
import { Actions } from "./actions";
import { ShellController } from "./controller";
import { DisplayStore } from "./display-store";
import { GatewayCredentials } from "./gateway-token";
import { discoverConfigured, discoverInstances, stateRoots } from "./discovery";
import { registerIpc } from "./ipc";
import { fakeMeshRunner, freshFakeMesh } from "./mesh-fake";
import * as fs from "node:fs";
import { runBrowserLogin } from "./browser-login";
import { MeshService, execMeshRunner, meshCredentialPath } from "./mesh-service";
import { TransferService } from "./transfers";
import { PlaygroundService } from "./playground";
import { PanelService } from "./panel-service";
import { fsSetupHost, previewSetup, runSetup } from "./setup";
import { bundledXmesh, cliCommandText, runXmeshRaw, runXmeshSetup } from "./cli";
import { type Action } from "./menu";
import { diffNotices, noticeText } from "./notifications";
import { type RecordedCall, electronPlatform, recordingPlatform } from "./platform";
import { registerPanelScheme, servePanel } from "./protocol";
import { Autostart, type LoginItemHint } from "./system/autostart";
import { SeenFlags } from "./system/firstrun";
import { Notifier } from "./system/notifier";
import { macApplier, windowsApplier, type Applier } from "./update/apply";
import { freshFakeUpdate, fakeSource } from "./update/fake";
import { targetFor } from "./update/releases";
import { UpdateActions } from "./update/service";
import { kernelServices } from "./update/kernels";
import { computerHostServices, emergencyStopComputerHost, runComputerHost } from "./computer-host";
import { registerComputerShortcut } from "./computer-shortcut";
import { type UpdateNotice, Updater } from "./update/updater";
import { httpSource } from "./update/source";
import { UpdateStore } from "./update/store";
import { TrayView } from "./tray";
import { type PanelWindow, createPanelWindow, installSessionHardening, installWebContentsHardening } from "./window";

const UI_ROOT = path.join(__dirname, "..", "..", "..");
const TEST_MODE = process.env.XMESH_UI_TEST === "1" && !app.isPackaged;
const BACKGROUND_TEST = TEST_MODE && process.env.XMESH_UI_BACKGROUND_TEST === "1";
if (BACKGROUND_TEST) {
  if (process.platform === "darwin") app.setActivationPolicy("prohibited");
  (globalThis as unknown as { __xmeshUiBackgroundTest: boolean }).__xmeshUiBackgroundTest = true;
}

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
  const display = new DisplayStore(path.join(app.getPath("userData"), "display.json"));
  const showPanel = (): void => {
    if (!panel || panel.window.isDestroyed()) {
      // Ctrl/Cmd +, - and 0 in the panel go through the same path as the display dialog.
      panel = createPanelWindow(undefined, {
        get: () => display.get().zoom,
        set: (zoom) => controller.setDisplay({ ...display.get(), zoom }),
      });
    }
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

  const calls: RecordedCall[] = [];
  // The updater reads the running nodes from the controller (only when an update runs), and the controller starts from its state.
  const updateNotice = (n: UpdateNotice): void =>
    notify([{ key: `update.${n.kind}.${n.version}`, text: t(n.kind === "available" ? "notice.updateAvailable" : n.kind === "updated" ? "notice.updated" : "notice.updateFailed", { version: n.version }) }]);
  // CI runs the end-to-end tests on Windows and macOS; on Linux, where no installer exists, the test pretends to be Windows.
  const fakeAsWindows = TEST_MODE && targetFor(process.platform, process.arch) === undefined;
  const updatePlatform = fakeAsWindows ? "win32" : process.platform;
  const arch = fakeAsWindows ? "x64" : process.arch;
  const target = targetFor(updatePlatform, arch);
  const fakeUpdate = TEST_MODE ? freshFakeUpdate() : undefined;
  const feed = process.env.XMESH_UPDATE_FEED;
  const applier: Applier | undefined = TEST_MODE
    ? { blocked: () => "", start: async (h) => void calls.push({ op: "update-handover", arg: path.basename(h.installer) }) }
    : process.platform === "win32"
      ? windowsApplier()
      : process.platform === "darwin"
        ? macApplier(process.execPath)
        : undefined;
  const kernelBundled = bundledXmesh((process as { resourcesPath?: string }).resourcesPath) !== undefined && !process.env.XMESH_BIN;
  const updateKernels = kernelServices({ discover: () => discoverInstances(roots), run: runXmeshRaw,
    ...(!TEST_MODE && kernelBundled && {prepare: async (node: string): Promise<void> => {
      if (!/^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/.test(node)) throw new Error("Invalid node name");
      const dir = roots.map((root) => path.join(root, node)).find((dir) => fs.existsSync(path.join(dir, "xmesh.yaml")));
      if (dir) await runXmeshSetup(["migrate", "--config", path.join(dir, "xmesh.yaml"), "--state-dir", dir]);
    }}),
    ...(kernelBundled && { version: app.getVersion() }) });
  const updateComputer = computerHostServices({ run: (command) => runComputerHost(command), version: app.getVersion() });
  const updater = new Updater({
    version: TEST_MODE ? "0.1.0-beta.2" : app.getVersion(),
    packaged: app.isPackaged || TEST_MODE,
    platform: updatePlatform,
    arch,
    kernelBundled,
    execPath: process.execPath,
    pid: process.pid,
    dataDir: path.join(app.getPath("userData"), "updates"),
    source:
      fakeUpdate && target
        ? fakeSource(fakeUpdate, target)
        : httpSource({
            fetch: (url, init) => net.fetch(url, init),
            // XMESH_UPDATE_FEED: a directory that serves `releases` and `download/<tag>/<file>` like GitHub does (for trying an update without a release).
            ...(feed && /^https?:\/\//.test(feed) && { apiBase: feed, downloadBase: `${feed.replace(/\/+$/, "")}/download` }),
          }),
    applier,
    store: new UpdateStore(path.join(app.getPath("userData"), "update.json")),
    runningNodes: () => TEST_MODE ? controller.snapshot.instances.filter((i) => !controller.snapshot.staleNodes.includes(i.id)).map((i) => i.id) : discoverInstances(roots).map((i) => i.id),
    stopNode: async (node) => {
      if (TEST_MODE) {
        calls.push({ op: "update-stop", arg: node });
        return "stopped";
      }
      return updateKernels.stop(node);
    },
    startNode: async (node) => {
      if (TEST_MODE) {
        calls.push({ op: "update-start", arg: node });
        return;
      }
      await updateKernels.start(node);
    },
    refreshNode: (node) => updateKernels.refresh(node),
    ...(!TEST_MODE && kernelBundled && { stopComputer: () => updateComputer.stop(), startComputer: () => updateComputer.start() }),
    quit: () => (TEST_MODE ? void calls.push({ op: "quit" }) : app.quit()),
    openPath: (p) => shell.openPath(p),
    notify: updateNotice,
    // A test drives the checks itself; none may start behind its back.
    ...(TEST_MODE && { firstCheckMs: 3_600_000 }),
  });
  const controller = new ShellController({
    getUpdate: () => updater.state,
    discover: () => discoverInstances(roots),
    configured: () => discoverConfigured(roots),
    cliCommand: cliCommandText(),
    locale,
    getLoginItem: () => autostart.get().enabled,
    getDisplay: () => display.get(),
    setDisplay: (prefs) => {
      if (!display.set(prefs)) notify([{ key: "display.save", text: t("display.saveFailed") }]);
      if (panel && !panel.window.isDestroyed()) panel.window.webContents.setZoomFactor(prefs.zoom);
    },
    setLoginItem: (enabled) => {
      const hint = autostart.set(enabled).hint;
      const text = hint === undefined ? undefined : loginHints[hint];
      if (text !== undefined) notify([{ key: `login.${hint}`, text }]);
    },
    // Mock fixtures ping frequently. Real kernels retain the protocol's
    // default idle budget for their fifteen-second event heartbeat.
    ...(TEST_MODE && { pollMs: 150, reconnectMs: 100, refreshDelayMs: 20, ...(!process.env.XMESH_UI_KERNEL_BINARY && { streamIdleMs: 2000 }) }),
    ...(BACKGROUND_TEST && { requestTimeoutMs: 30_000 }),
  });

  const answers: { confirm: boolean; cliFails: boolean; picked?: string; setupWaiting?: boolean; setupConflict?: boolean } = { confirm: true, cliFails: false };
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
  // Tailnet administration shells out to `xmesh mesh --json`. The control-plane
  // credential is a 0600 file (not the keychain), so the panel works on hosts
  // without a desktop secret store and never depends on a CLI profile name.
  const fakeMesh = TEST_MODE ? freshFakeMesh() : undefined;
  const meshCredential = meshCredentialPath(roots[0]!);
  const meshService = new MeshService(
    fakeMesh ? fakeMeshRunner(fakeMesh) : execMeshRunner(),
    () => {
      const c = controller.snapshot.connection;
      const file = c.kind === "connected" ? c.status.paths?.config_file : undefined;
      return typeof file === "string" && file !== "" ? file : undefined;
    },
    () => meshCredential,
  );
  // The first run can mint its own preauthorised key with the credential saved
  // on the Tailnet page, so a new node never needs a hand-copied key file.
  const setupHost = fsSetupHost(
    roots[0]!,
    async (args) => {
      if (TEST_MODE) {
        calls.push({ op: "setup-cli", arg: [...args] });
        if (answers.cliFails) throw new Error("simulated cli failure");
        return `ok ${args[0]}`;
      }
      return runXmeshSetup(args);
    },
    async (file, tags, controlUrl, user) => {
      if (TEST_MODE) {
        calls.push({ op: "setup-authkey", arg: { file, tags } });
        return;
      }
      let cred: { kind?: string; url?: string };
      try { cred = JSON.parse(fs.readFileSync(meshCredential, "utf8")) as typeof cred; }
      catch { throw new Error("请先在 Tailnet 管理页保存管理凭据"); }
      const official = "https://controlplane.tailscale.com";
      const expected = cred.kind === "tailscale" ? official : cred.url;
      if (!expected || new URL(expected).origin !== new URL(controlUrl || official).origin) {
        throw new Error("已保存的管理凭据属于另一个控制面，请切换凭据或控制面地址");
      }
      const r = await meshService.request({ op: "onboard", tags, ttl: "1h", ephemeral: false, file, overwrite: false, ...(user && { user }) });
      if (!r.ok) throw new Error(r.error.message);
    },
    async (config, state, controlUrl) => {
      if (TEST_MODE) { calls.push({ op: "setup-browser-login", arg: { config, state, controlUrl } }); return; }
      await runBrowserLogin(config, state, controlUrl, (url) => platform.openExternal(url));
    },
  );
  const panelService = new PanelService(controller, TEST_MODE ? async () => { calls.push({ op: "computer-setup" }); } : undefined, TEST_MODE ? async () => { calls.push({ op: "computer-resume-host" }); } : undefined);
  const playground = new PlaygroundService(controller, {
    copy: (text) => clipboard.writeText(text),
    save: async (text) => {
      const result = await dialog.showSaveDialog({ defaultPath: "xmesh-tool-result.json", filters: [{ name: "JSON", extensions: ["json"] }] });
      if (result.canceled || !result.filePath) return false;
      await fs.promises.writeFile(result.filePath, text, { mode: 0o600 });
      return true;
    },
  });
  const transfers = new TransferService(playground, controller, {
    upload: async () => {
      if (TEST_MODE) return answers.picked || undefined;
      const result = await dialog.showOpenDialog({ properties: ["openFile"] });
      return result.canceled ? undefined : result.filePaths[0];
    },
    download: async (name) => {
      if (TEST_MODE) return answers.picked || undefined;
      const result = await dialog.showSaveDialog({ defaultPath: name });
      return result.canceled ? undefined : result.filePath;
    },
    overwrite: async (remote, device) => platform.confirm({ title: controller.snapshot.settings.locale === "en" ? "Overwrite remote file?" : "覆盖远端文件？", detail: `${device}: ${remote}`, confirmLabel: controller.snapshot.settings.locale === "en" ? "Overwrite" : "覆盖", cancelLabel: controller.snapshot.settings.locale === "en" ? "Cancel" : "取消" }),
  });
  let playgroundKey = "";
  controller.on("change", (s: import("../shared/state").Snapshot) => {
    const c = s.connection;
    const key = JSON.stringify([s.selectedInstance, c.kind, c.kind === "connected" ? c.status.control.boot_id : "", c.kind === "connected" ? c.status.gateway : null]);
    if (key !== playgroundKey) { playgroundKey = key; transfers.reset(); void playground.close(); }
  });
  registerIpc({
    onboarding: createOnboardingService(roots[0]!, (url) => platform.openExternal(url), TEST_MODE ? fakeOnboarding(roots[0]!, answers, calls) : undefined, (text) => clipboard.writeText(text)),
    playground,
    transfers,
    controller,
    actions,
    service: panelService,
    previewSetup: (params) => TEST_MODE ? previewSetup(params, setupHost) : { ok: false, error: { code: "setup_migrated", message: "请使用开始使用向导生成设置计划。" } },
    setup: async (params) => TEST_MODE ? runSetup(params, setupHost) : { ok: false, error: { code: "setup_migrated", message: "请使用开始使用向导继续设置任务。" } },
    mesh: (request) => meshService.request(request),
    panel: () => (panel && !panel.window.isDestroyed() ? panel.window.webContents : undefined),
    update: new UpdateActions(updater),
  });
  if (!TEST_MODE) controller.setComputerShortcutAvailable(registerComputerShortcut(globalShortcut, () => {
    void panelService.computer({ op: "pause", paused: true });
    void emergencyStopComputerHost().catch((error: unknown) => notifyError(error instanceof Error ? error.message : String(error)));
  }));
  updater.on("change", (state) => controller.setUpdate(state));

  let prev: Snapshot = controller.snapshot;
  tray.update(prev);
  controller.on("change", (snap) => {
    tray.update(snap);
    notify(diffNotices(prev, snap).map((n) => ({ key: n.key, text: noticeText(n, t) })));
    prev = snap;
  });

  app.on("before-quit", () => {
    transfers.reset();
    void playground.close();
    void controller.stop();
  });
  app.on("will-quit", () => { globalShortcut.unregisterAll(); tray.destroy(); });

  // After sleep or a lock the event stream is stale; do not wait for the idle timeout to notice.
  powerMonitor.on("resume", () => controller.nudge());
  powerMonitor.on("unlock-screen", () => controller.nudge());

  // Windows 11 puts a new tray icon in the overflow flyout; say so once, for the installed app only.
  if (process.platform === "win32" && app.isPackaged && new SeenFlags(path.join(app.getPath("userData"), "seen.json")).markOnce("trayOverflowHint")) {
    notify([{ key: "hint.trayOverflow", text: t("hint.trayOverflow") }]);
  }

  controller.start();
  void updater.finishPending().finally(() => updater.start());

  if (discoverConfigured(roots).length === 0 && discoverInstances(roots).length === 0) {
    showPanel();
  }

  if (TEST_MODE) {
    // Reachable only from Playwright's main-process evaluate; never set in a packaged app.
    (globalThis as Record<string, unknown>).__xmeshUiTest = {
      tray: () => tray.describe(),
      snapshot: () => controller.snapshot,
      calls: () => calls.slice(),
      notices: () => notified.slice(),
      answers,
      mesh: fakeMesh,
      update: fakeUpdate,
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
