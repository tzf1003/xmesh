import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import Module from "node:module";
import { test } from "node:test";
import type { TrayState } from "../../src/shared/tray";

function loadWindow() {
  const instances: Window[] = [];
  class Window extends EventEmitter {
    webContents = Object.assign(new EventEmitter(), { isLoading: () => false, send: () => {}, setZoomFactor: () => {} });
    destroyed = false;
    positions: unknown[] = [];
    shows = 0;
    resolve!: () => void;
    reject!: (error: Error) => void;
    constructor(_options: unknown) { super(); instances.push(this); }
    loadURL(): Promise<void> { return new Promise((resolve, reject) => { this.resolve = resolve; this.reject = reject; }); }
    setMenu(): void {}
    setAlwaysOnTop(): void {}
    setVisibleOnAllWorkspaces(): void {}
    setBounds(bounds: unknown): void { this.positions.push(bounds); }
    getBounds(): unknown { return this.positions.at(-1); }
    isDestroyed(): boolean { return this.destroyed; }
    destroy(): void { this.destroyed = true; }
    show(): void { this.shows++; }
    focus(): void {}
    hide(): void {}
  }
  const screen = Object.assign(new EventEmitter(), { getCursorScreenPoint: () => ({ x: 100, y: 20 }), getDisplayMatching: (r: { x: number }) => ({ workArea: { x: r.x < 1000 ? 0 : 1440, y: 24, width: 1440, height: 850 } }) });
  const fake = { BrowserWindow: Window, app: Object.assign(new EventEmitter(), { isPackaged: false }), nativeTheme: { shouldUseDarkColors: false }, screen };
  const m = Module as unknown as { _load(req: string, ...rest: unknown[]): unknown };
  const original = m._load;
  m._load = (req, ...args) => req === "electron" ? fake : original.call(m, req, ...args);
  const name = require.resolve("../../src/main/tray-window");
  delete require.cache[name];
  try {
    const { TrayWindow } = require(name) as typeof import("../../src/main/tray-window");
    let failures = 0;
    const tray = new TrayWindow(() => ({ display: { zoom: 1 } }) as TrayState, () => {}, () => { failures++; });
    return { tray, instances, failures: () => failures };
  } finally { m._load = original; }
}

test("only the latest show request may position a loading tray after hide and reopen on another display", async () => {
  const h = loadWindow();
  try {
    const first = h.tray.show({ x: 100, y: 20, width: 24, height: 24 });
    h.tray.hide();
    const second = h.tray.show({ x: 1700, y: 20, width: 24, height: 24 });
    h.instances[0]!.resolve();
    await Promise.all([first, second]);
    assert.equal(h.instances[0]!.positions.length, 1);
    assert.ok((h.tray.describe().bounds?.x ?? 0) >= 1440);
  } finally { h.tray.destroy(); }
});

test("an abandoned load failure cannot reopen fallback UI after the tray was hidden", async () => {
  const h = loadWindow();
  try {
    const pending = h.tray.show({ x: 100, y: 20, width: 24, height: 24 });
    h.tray.hide();
    h.instances[0]!.reject(new Error("cancelled load"));
    await pending;
    assert.equal(h.failures(), 0);
    assert.equal(h.tray.isOpen, false);
  } finally { h.tray.destroy(); }
});
