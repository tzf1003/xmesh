import { BrowserWindow, app, session, shell, type WebContents, type WebPreferences } from "electron";
import * as path from "node:path";
import { EXTERNAL_LINKS } from "../shared/ipc";
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
export function createPanelWindow(onClosedByUser: () => void = () => {}): PanelWindow {
  const webPreferences = panelWebPreferences(path.join(__dirname, "..", "preload", "index.js"), app.isPackaged);
  const win = new BrowserWindow({
    width: 960,
    height: 640,
    minWidth: 640,
    minHeight: 420,
    show: false,
    title: "xmesh",
    autoHideMenuBar: true,
    webPreferences,
  });
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
