import * as path from "node:path";
import { BrowserWindow, app, nativeTheme, screen, type WebContents, type WebPreferences } from "electron";
import { TRAY_ENTRY, TRAY_EVENT, TRAY_PRESENT, type TrayState } from "../shared/tray";
import { panelWebPreferences } from "./policy";
import { trayPosition, type Rect } from "./tray-position";

/** One sandboxed renderer; hidden windows neither receive activity pushes nor poll. */
export class TrayWindow {
  private window?: BrowserWindow;
  private loading?: Promise<void>;
  private anchor?: Rect;
  private opened = false;
  private lastBlur = 0;
  private failures = 0;
  private destroyed = false;
  private quitting = false;
  private generation = 0;
  private moreOnOpen = false;
  private readonly background =
    !app.isPackaged && process.env.XMESH_UI_TEST === "1" && process.env.XMESH_UI_BACKGROUND_TEST === "1";
  private readonly reposition = (): void => {
    if (this.opened && this.window && this.anchor) this.position(this.anchor);
  };
  private readonly beforeQuit = (): void => {
    this.quitting = true;
  };
  readonly preferences: WebPreferences;
  constructor(
    private readonly state: () => TrayState,
    private readonly onHide: () => void,
    private readonly failed: () => void,
  ) {
    this.preferences = {
      ...panelWebPreferences(path.join(__dirname, "..", "preload", "tray.js"), app.isPackaged),
      ...(this.background && { backgroundThrottling: false }),
    };
    screen.on("display-metrics-changed", this.reposition);
    screen.on("display-removed", this.reposition);
    app.on("before-quit", this.beforeQuit);
  }
  get contents(): WebContents | undefined {
    return this.window && !this.window.isDestroyed() ? this.window.webContents : undefined;
  }
  get isOpen(): boolean {
    return this.opened;
  }
  async toggle(anchor: Rect, more = false): Promise<void> {
    if (this.opened && !more) {
      this.hide();
      return;
    }
    if (!more && Date.now() - this.lastBlur < 200) return;
    await this.show(anchor, more);
  }
  async show(anchor: Rect, more = false): Promise<void> {
    if (this.destroyed) return;
    this.anchor = anchor;
    this.moreOnOpen = more;
    this.opened = true;
    const generation = ++this.generation;
    try {
      if (!this.window || this.window.isDestroyed()) this.create();
      await this.loading;
      if (!this.opened || this.destroyed || generation !== this.generation || !this.window || this.window.isDestroyed())
        return;
      this.position(anchor);
      this.push();
      this.present();
      if (!this.background) {
        this.window.show();
        this.window.focus();
      }
    } catch {
      if (generation !== this.generation || this.destroyed) return;
      this.hide();
      this.failures++;
      this.window?.destroy();
      this.window = undefined;
      this.failed();
    }
  }
  hide(blur = false): void {
    this.generation++;
    if (blur) this.lastBlur = Date.now();
    this.opened = false;
    this.window?.hide();
    this.onHide();
  }
  push(): void {
    if (this.opened && this.contents && !this.contents.isLoading()) this.contents.send(TRAY_EVENT, this.state());
  }
  present(): void {
    if (this.opened) this.contents?.send(TRAY_PRESENT, this.moreOnOpen);
  }
  describe(): { open: boolean; bounds?: Rect; failures: number } {
    return {
      open: this.opened,
      bounds: this.window?.getBounds(),
      failures: this.failures,
    };
  }
  destroy(): void {
    this.destroyed = true;
    this.opened = false;
    this.generation++;
    screen.off("display-metrics-changed", this.reposition);
    screen.off("display-removed", this.reposition);
    app.off("before-quit", this.beforeQuit);
    this.window?.destroy();
    this.window = undefined;
  }
  private create(): void {
    const window = new BrowserWindow({
      width: 400,
      height: 580,
      show: false,
      frame: false,
      resizable: false,
      minimizable: false,
      maximizable: false,
      skipTaskbar: true,
      fullscreenable: false,
      autoHideMenuBar: true,
      title: "xmesh",
      backgroundColor: nativeTheme.shouldUseDarkColors ? "#242428" : "#f3f3f5",
      roundedCorners: true,
      ...(this.background && { focusable: false }),
      webPreferences: this.preferences,
    });
    this.window = window;
    window.setMenu(null);
    window.setAlwaysOnTop(true, "pop-up-menu");
    if (process.platform === "darwin") window.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true });
    window.on("blur", () => {
      if (this.opened) this.hide(true);
    });
    window.on("close", (e) => {
      if (!this.destroyed && !this.quitting) {
        e.preventDefault();
        this.hide();
      }
    });
    window.webContents.on("render-process-gone", () => {
      if (this.destroyed || window !== this.window) return;
      this.failures++;
      this.generation++;
      this.hide();
      window.destroy();
      this.window = undefined;
      if (this.failures >= 2) this.failed();
    });
    window.webContents.on("did-finish-load", () => window.webContents.setZoomFactor(this.state().display.zoom));
    this.loading = window.loadURL(TRAY_ENTRY);
  }
  private position(anchor: Rect): void {
    const usable =
      anchor.width > 0 && anchor.height > 0 ? anchor : { ...screen.getCursorScreenPoint(), width: 1, height: 1 };
    const display = screen.getDisplayMatching(usable);
    this.window?.setBounds(trayPosition(usable, display.workArea));
  }
}
