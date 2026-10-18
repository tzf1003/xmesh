import { BrowserWindow, app, nativeTheme, session, shell, type WebContents, type WebPreferences } from "electron";
import * as path from "node:path";
import { EXTERNAL_LINKS } from "../shared/ipc";
import { stepZoom } from "../shared/display";
import { overlayColors, windowChrome } from "./chrome";
import { PANEL_ENTRY, panelWebPreferences } from "./policy";

/**
 * Applied to every WebContents the app ever creates, not just the panel, so a
 * window added later cannot skip the rules: no navigation, no new windows, no
 * webviews, no permissions.
 */
export function hardenWebContents(contents: WebContents): void {
  contents.on("will-navigate", (e) => e.preventDefault());
  contents.on("will-redirect", (e) => e.preventDefault());
  contents.on("will-attach-webview", (e) => e.preventDefault());
  contents.setWindowOpenHandler(({ url }) => {
    // Links open in the system browser, and only the fixed allowlist.
    if ((Object.values(EXTERNAL_LINKS) as string[]).includes(url)) void shell.openExternal(url);
    return { action: "deny" };
  });
}

export function hardenSession(s: Electron.Session): void {
  s.setPermissionRequestHandler((_wc, _permission, callback) => callback(false));
  s.setPermissionCheckHandler(() => false);
  s.setDevicePermissionHandler(() => false);
}

/** Register before any window exists. */
export function installWebContentsHardening(): void {
  app.on("web-contents-created", (_e, contents) => hardenWebContents(contents));
}

/** Needs the app to be ready. */
export function installSessionHardening(): void {
  hardenSession(session.defaultSession);
}

export interface PanelWindow {
  window: BrowserWindow;
  /** The exact options the window was created with. */
  webPreferences: WebPreferences;
  show(): void;
}

/** Creates the panel hidden. Closing it hides it: the tray keeps running. */
export interface ZoomControl {
  get(): number;
  set(zoom: number): void;
}

/** Ctrl/Cmd + "+", "-" and "0" step or reset the panel's text size, as in a browser; nothing else is intercepted. */
export function zoomKey(input: { type: string; key: string; control: boolean; meta: boolean; alt: boolean }): 1 | -1 | 0 | undefined {
  if (input.type !== "keyDown" || input.alt || !(input.control || input.meta)) return undefined;
  if (input.key === "+" || input.key === "=") return 1;
  if (input.key === "-" || input.key === "_") return -1;
  if (input.key === "0") return 0;
  return undefined;
}

export function createPanelWindow(onClosedByUser: () => void = () => {}, zoom?: ZoomControl): PanelWindow {
  const webPreferences = panelWebPreferences(path.join(__dirname, "..", "preload", "index.js"), app.isPackaged);
  const win = new BrowserWindow({
    width: 960,
    height: 640,
    minWidth: 640,
    minHeight: 420,
    show: false,
    title: "xmesh",
    autoHideMenuBar: true,
    ...windowChrome(process.platform, nativeTheme.shouldUseDarkColors),
    webPreferences,
  });
  if (process.platform === "win32") {
    // The caption area takes the page's colour, so it follows the system theme with it.
    nativeTheme.on("updated", () => {
      if (!win.isDestroyed()) win.setTitleBarOverlay(overlayColors(nativeTheme.shouldUseDarkColors));
    });
  }
  let quitting = false;
  app.on("before-quit", () => {
    quitting = true;
  });
  win.on("close", (e) => {
    if (quitting) return;
    e.preventDefault();
    win.hide();
    onClosedByUser();
  });
  if (zoom) {
    win.webContents.on("did-finish-load", () => win.webContents.setZoomFactor(zoom.get()));
    win.webContents.on("before-input-event", (e, input) => {
      const dir = zoomKey(input);
      if (dir === undefined) return;
      e.preventDefault();
      zoom.set(dir === 0 ? 1 : stepZoom(zoom.get(), dir));
    });
  }
  void win.loadURL(PANEL_ENTRY);
  return {
    window: win,
    webPreferences,
    show: () => {
      if (win.isMinimized()) win.restore();
      win.show();
      win.focus();
    },
  };
}
