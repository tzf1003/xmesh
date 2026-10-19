// Electron hardening, asserted against the running app (docs/design/12 "面板", "验收 / Electron 安全").

import { expect, test } from "@playwright/test";
import { spawn } from "node:child_process";
import * as path from "node:path";
import { pathToFileURL } from "node:url";
import { PANEL_API_METHODS } from "../../src/shared/ipc";
import { type Harness, UI_ROOT, expectIcon, launch } from "./helpers";

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

test("the renderer has no Node, no electron, and exactly the whitelisted API", async () => {
  h = await launch();
  const page = await h.openPanel();

  const probe = await page.evaluate(() => ({
    require: typeof (globalThis as Record<string, unknown>).require,
    process: typeof (globalThis as Record<string, unknown>).process,
    module: typeof (globalThis as Record<string, unknown>).module,
    buffer: typeof (globalThis as Record<string, unknown>).Buffer,
    ipcRenderer: typeof (globalThis as Record<string, unknown>).ipcRenderer,
    electron: typeof (globalThis as Record<string, unknown>).electron,
    api: Object.keys((globalThis as unknown as { xmesh: object }).xmesh).sort(),
    frozen: Object.isFrozen((globalThis as unknown as { xmesh: object }).xmesh),
    origin: location.origin,
  }));
  expect(probe.require).toBe("undefined");
  expect(probe.process).toBe("undefined");
  expect(probe.module).toBe("undefined");
  expect(probe.buffer).toBe("undefined");
  expect(probe.ipcRenderer).toBe("undefined");
  expect(probe.electron).toBe("undefined");
  expect(probe.api).toEqual([...PANEL_API_METHODS].sort());
  expect(probe.frozen).toBe(true);
  expect(probe.origin).toBe("app://panel");
});

test("the window is created with contextIsolation, sandbox and no node integration (the behaviour is checked in the first test)", async () => {
  h = await launch();
  await h.openPanel();
  const prefs = await h.app.evaluate(() => {
    const t = (globalThis as unknown as { __xmeshUiTest: { panelPreferences(): Electron.WebPreferences } }).__xmeshUiTest;
    const p = t.panelPreferences();
    return {
      contextIsolation: p.contextIsolation,
      sandbox: p.sandbox,
      nodeIntegration: p.nodeIntegration,
      nodeIntegrationInWorker: p.nodeIntegrationInWorker,
      nodeIntegrationInSubFrames: p.nodeIntegrationInSubFrames,
      webSecurity: p.webSecurity,
      webviewTag: p.webviewTag,
      allowRunningInsecureContent: p.allowRunningInsecureContent,
    };
  });
  expect(prefs).toEqual({
    contextIsolation: true,
    sandbox: true,
    nodeIntegration: false,
    nodeIntegrationInWorker: false,
    nodeIntegrationInSubFrames: false,
    webSecurity: true,
    webviewTag: false,
    allowRunningInsecureContent: false,
  });
});

test("the CSP blocks inline script, eval and remote script", async () => {
  h = await launch();
  const page = await h.openPanel();
  const result = await page.evaluate(async () => {
    const violations: string[] = [];
    document.addEventListener("securitypolicyviolation", (e) => violations.push(e.violatedDirective));

    const inline = document.createElement("script");
    inline.textContent = "window.__inline = 1";
    document.head.append(inline);

    // CDP lets a script it evaluates call eval regardless of CSP, so run it from a timer task.
    let evalBlocked = false;
    setTimeout(() => {
      try {
        // eslint-disable-next-line no-eval
        (0, eval)("window.__eval = 1");
      } catch {
        evalBlocked = true;
      }
    }, 0);

    const remote = document.createElement("script");
    remote.src = "https://example.invalid/x.js";
    document.head.append(remote);

    await new Promise((r) => setTimeout(r, 300));
    return {
      inlineRan: (window as unknown as Record<string, unknown>).__inline === 1,
      evalRan: (window as unknown as Record<string, unknown>).__eval === 1,
      evalBlocked,
      violations,
    };
  });
  expect(result.inlineRan).toBe(false);
  expect(result.evalRan).toBe(false);
  expect(result.evalBlocked).toBe(true);
  expect(result.violations.filter((v) => v.startsWith("script-src")).length).toBeGreaterThanOrEqual(2);
});

test("navigation and new windows are refused", async () => {
  h = await launch();
  const page = await h.openPanel();
  await page.evaluate(() => {
    (window as unknown as Record<string, unknown>).__marker = 1;
  });

  await page.evaluate(() => {
    location.href = "https://example.invalid/";
  });
  await page.waitForTimeout(500);
  await page.evaluate(() => {
    location.assign("app://panel/index.html");
  });
  await page.waitForTimeout(500);
  const opened = await page.evaluate(() => window.open("https://example.invalid/") === null);

  expect(opened).toBe(true);
  // A completed navigation would have reset the page, dropping the marker.
  expect(await page.evaluate(() => (window as unknown as Record<string, unknown>).__marker)).toBe(1);
  expect(await page.evaluate(() => location.href)).toBe("app://panel/index.html");
  expect(h.app.windows()).toHaveLength(1);
});

test("a window that is not the panel cannot call the control IPC", async () => {
  h = await launch();
  await h.openPanel();
  await expectIcon(h, "ok");
  const preload = await h.app.evaluate(() => {
    const t = (globalThis as unknown as { __xmeshUiTest: { panelPreferences(): Electron.WebPreferences } }).__xmeshUiTest;
    return t.panelPreferences().preload!;
  });
  // Same preload, same API, different window: the main process must still refuse it.
  const other = h.app.waitForEvent("window");
  await h.app.evaluate(({ BrowserWindow }, p) => {
    const w = new BrowserWindow({
      show: false,
      webPreferences: { preload: p, contextIsolation: true, sandbox: true },
    });
    void w.loadURL("about:blank");
  }, preload);
  const page = await other;
  await page.waitForLoadState();
  const outcome = await page.evaluate(async () => {
    try {
      await (window as unknown as { xmesh: { drain(): Promise<unknown> } }).xmesh.drain();
      return "allowed";
    } catch (e) {
      return String(e);
    }
  });
  expect(outcome).toContain("untrusted sender");
  expect(h.kernel.requests.some((r) => r.path === "/v2/drain")).toBe(false);
});

test("panel calls reach the kernel through the main process, with validated arguments", async () => {
  h = await launch();
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", "ok");
  await expect(page.getByTestId("node")).toHaveText("win-dev-01");

  await page.getByTestId("overview-advanced").locator("summary").click();
  await page.getByRole("button", { name: "暂停接收新调用" }).click();
  await expect.poll(() => h.kernel.requests.some((r) => r.path === "/v2/drain")).toBe(true);
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", "draining");
  await expectIcon(h, "draining");

  const bad = await page.evaluate(async () => {
    const api = (window as unknown as { xmesh: Record<string, (a?: unknown) => Promise<{ ok: boolean; error?: { code: string } }>> }).xmesh;
    return [
      await api.copy!("rm -rf /"),
      await api.openPath!({ kind: "config" }),
      await api.openExternal!("https://evil.invalid/"),
      await api.selectInstance!("../../etc"),
      await api.setLoginItem!("yes"),
    ].map((r) => r.error?.code);
  });
  expect(bad).toEqual(["invalid_args", "invalid_args", "invalid_args", "invalid_args", "invalid_args"]);
});

test("closing the panel only hides it, and a second launch brings it back instead of starting another app", async () => {
  h = await launch();
  await h.openPanel();
  const state = (): Promise<{ exists: boolean; visible: boolean }> =>
    h.app.evaluate(() =>
      (globalThis as unknown as { __xmeshUiTest: { panelState(): { exists: boolean; visible: boolean } } }).__xmeshUiTest.panelState(),
    );
  expect(await state()).toEqual({ exists: true, visible: true });

  await h.app.evaluate(() => (globalThis as unknown as { __xmeshUiTest: { closePanel(): void } }).__xmeshUiTest.closePanel());
  expect(await state()).toEqual({ exists: true, visible: false });
  await expectIcon(h, "ok");

  // Same user-data dir means the same single-instance lock: the newcomer exits and the first app shows its panel.
  const args = [`--user-data-dir=${h.userData}`, UI_ROOT];
  if (process.platform === "linux") args.unshift("--no-sandbox");
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const electronBinary = require("electron") as string;
  const second = spawn(electronBinary, args, {
    env: { ...process.env, XMESH_UI_TEST: "1", XMESH_UI_STATE_ROOT: h.root },
    stdio: "ignore",
  });
  const code = await new Promise<number | null>((resolve) => second.once("exit", resolve));
  expect(code).toBe(0);
  await expect.poll(async () => (await state()).visible).toBe(true);
});

test("the Electron binary under test has the expected fuses", async () => {
  const fuses = await import("@electron/fuses");
  const cfg = (await import(pathToFileURL(path.join(UI_ROOT, "scripts", "fuses.config.mjs")).href)) as {
    profiles: Record<string, Record<string, boolean | string>>;
    ALWAYS: number[];
  };
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const binary = require("electron") as string;
  const wire = await fuses.getCurrentFuseWire(binary);
  for (const key of cfg.ALWAYS) {
    expect(wire[key as unknown as keyof typeof wire], `fuse ${fuses.FuseV1Options[key]}`).toBe(fuses.FuseState.DISABLE);
  }
  expect(wire[fuses.FuseV1Options.EnableCookieEncryption]).toBe(fuses.FuseState.ENABLE);
});
