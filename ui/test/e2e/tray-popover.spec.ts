import { test, expect, type Page } from "@playwright/test";
import { launch, expectIcon, openModuleSettings, type Harness } from "./helpers";
import { eventFixture, peersFixture, peerNamed } from "../../testserver/fixtures";
import type { CallEvent, Capability } from "../../src/shared/control";
import * as fs from "node:fs";
import * as path from "node:path";
import { FakeKernel } from "../../testserver/server";
const CAPS: Capability[] = [
  "status",
  "events",
  "peers",
  "sessions",
  "drain",
  "config.read",
  "config.write",
  "pats",
  "audit",
  "gateway",
];
let h: Harness;
test.afterEach(async () => {
  if (h) await h.close();
});
async function openTray(): Promise<Page> {
  const existing = h.app.windows().find((p) => p.url().startsWith("app://tray/"));
  const page = existing ? Promise.resolve(existing) : h.app.waitForEvent("window");
  await h.app.evaluate(() =>
    (globalThis as unknown as { __xmeshUiTest: { showTray(): Promise<void> } }).__xmeshUiTest.showTray(),
  );
  const result = await page;
  await result.waitForURL("app://tray/index.html");
  await expect(result.getByTestId("tray-panel")).toBeVisible();
  return result;
}
async function emitCall(): Promise<void> {
  h.kernel.emit("call", eventFixture<CallEvent>("call").data);
}
test("compact tray renders real status and four tabs using one event stream", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  await expectIcon(h, "ok");
  const page = await openTray();
  await expect(page.getByRole("tab", { name: "概览", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("win-dev-01", { exact: true })).toBeVisible();
  const state = await h.app.evaluate(() =>
    (
      globalThis as unknown as {
        __xmeshUiTest: {
          trayWindow(): { bounds: { width: number; height: number } };
        };
      }
    ).__xmeshUiTest.trayWindow(),
  );
  expect(state.bounds.width).toBe(400);
  expect(state.bounds.height).toBe(580);
  await emitCall();
  await page.getByRole("tab", { name: "活动", exact: true }).click();
  await expect(page.locator(".call-row")).toHaveCount(1);
  await page.locator(".call-row").click();
  await expect(page.getByRole("dialog", { name: "调用详情" })).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "调用详情" }).click();
  await h.openPanel();
  expect(h.kernel.connectedStreams).toBe(1);
});
test("device search keeps focus and filters online devices; service controls copy through the main process", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  await expectIcon(h, "ok");
  h.kernel.setPeers(peersFixture().map((p) => (p.name === "cloud-vm" ? peerNamed("cloud-vm", { online: false }) : p)));
  const page = await openTray();
  await page.getByRole("tab", { name: /^设备/ }).click();
  const input = page.getByRole("searchbox", { name: "搜索设备" });
  await input.pressSequentially("mac");
  await expect(input).toBeFocused();
  await expect(page.locator(".device-row")).toHaveCount(1);
  await input.fill("not-a-device");
  await expect(page.getByText("没有匹配的设备")).toBeVisible();
  await input.fill("");
  await page.getByRole("checkbox", { name: "仅在线" }).check();
  await expect(page.locator(".device-row")).toHaveCount(1);
  await page.getByRole("tab", { name: "服务", exact: true }).click();
  await page.getByRole("button", { name: "Claude Code", exact: true }).click();
  await expect.poll(async () => (await h.calls()).filter((c) => c.op === "clipboard").length).toBe(1);
  expect((await h.calls()).find((c) => c.op === "clipboard")?.arg).toContain("Authorization: Bearer");
  await expect(page.getByRole("status")).toContainText("连接配置已复制");
});
test("pause/resume and maintenance confirmations execute actual actions once", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  await expectIcon(h, "ok");
  const page = await openTray();
  await page.getByRole("button", { name: "暂停接入", exact: true }).click();
  await expectIcon(h, "draining");
  await page.getByRole("button", { name: "恢复接入", exact: true }).click();
  await expectIcon(h, "ok");
  await page.getByRole("button", { name: "设置", exact: true }).click();
  await page.getByRole("button", { name: "停止内核", exact: true }).click();
  await page.getByRole("button", { name: "取消", exact: true }).click();
  expect((await h.calls()).some((c) => c.op === "cli")).toBe(false);
  await page.getByRole("button", { name: "停止内核", exact: true }).click();
  await page.getByTestId("tray-confirm").click();
  await expect.poll(async () => (await h.calls()).filter((c) => c.op === "cli").map((c) => c.arg)).toEqual(["stop"]);
  expect((await h.calls()).some((c) => c.op === "confirm")).toBe(false);
});
test("tray renderer cannot invoke panel APIs or choose arbitrary actions", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  await expectIcon(h, "ok");
  const page = await openTray();
  const result = await h.app.evaluate(async () => {
    const hook = (
      globalThis as unknown as {
        __xmeshUiTest: { trayWebContents(): Electron.WebContents };
      }
    ).__xmeshUiTest;
    const contents = hook.trayWebContents();
    return contents.executeJavaScript(`({api:typeof window.xmesh, keys:Object.keys(window.xmeshTray)})`);
  });
  expect(result.api).toBe("undefined");
  expect(result.keys.sort()).toEqual(["command", "getState", "hide", "onPresent", "onState"]);
  const invalid = await page.evaluate(async () => {
    const api = (
      window as unknown as {
        xmeshTray: {
          getState(): Promise<{ instance: string; bootId: string }>;
          command(request: unknown): Promise<{ ok: boolean }>;
        };
      }
    ).xmeshTray;
    const state = await api.getState();
    return api.command({
      instance: state.instance,
      bootId: state.bootId,
      op: "action",
      id: "run-shell",
    });
  });
  expect(invalid.ok).toBe(false);
  expect((await h.calls()).some((c) => c.op === "cli")).toBe(false);
  await page.getByRole("tab", { name: "概览", exact: true }).press("ArrowRight");
  await expect(page.getByRole("tab", { name: /^设备/ })).toHaveAttribute("aria-selected", "true");
});
test("main window navigation waits for renderer readiness and reaches settings", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  await expectIcon(h, "ok");
  const tray = await openTray();
  await tray.getByRole("button", { name: "设置", exact: true }).click();
  await tray.getByRole("button", { name: "外观与语言", exact: true }).click();
  const panel = await h.openPanel();
  await expect(panel.getByTestId("page-settings")).toBeVisible();
});
test("hidden tray resynchronizes activity, clears old boot history, and remains sandboxed", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  await expectIcon(h, "ok");
  const page = await openTray();
  await page.getByRole("tab", { name: "活动", exact: true }).click();
  await emitCall();
  await expect(page.locator(".call-row")).toHaveCount(1);
  await page.getByRole("tab", { name: "活动", exact: true }).press("Escape");
  await emitCall();
  await openTray();
  await expect(page.locator(".call-row")).toHaveCount(2);
  h.kernel.restartBoot();
  h.publish();
  await expect(page.locator(".call-row")).toHaveCount(0);
  const prefs = await h.app.evaluate(() =>
    (
      globalThis as unknown as {
        __xmeshUiTest: { trayPreferences(): Electron.WebPreferences };
      }
    ).__xmeshUiTest.trayPreferences(),
  );
  expect(prefs.sandbox).toBe(true);
  expect(prefs.contextIsolation).toBe(true);
  expect(prefs.nodeIntegration).toBe(false);
});

test("offline and incompatible kernels expose recovery actions with business controls disabled", async () => {
  h = await launch({ kernel: { controlVersion: "3.0" } });
  await expectIcon(h, "mismatch");
  const page = await openTray();
  await expect(page.getByRole("heading", { name: "桌面端与内核版本不兼容" })).toBeVisible();
  await expect(page.getByRole("button", { name: "查看版本", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "设置", exact: true }).click();
  await expect(page.getByRole("button", { name: "停止内核", exact: true })).toBeDisabled();
});

test("a missing gateway credential produces an error and leaves the clipboard unchanged", async () => {
  h = await launch({ kernel: { capabilities: ["status", "events", "peers", "drain"] } });
  await expectIcon(h, "ok");
  const page = await openTray();
  await page.getByRole("button", { name: "复制连接信息", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("gateway token unavailable");
  expect((await h.calls()).some((c) => c.op === "clipboard")).toBe(false);
});

test("unsaved main-window changes block instance switching and external navigation asks before discarding", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  const second = await FakeKernel.start({ pingMs: 300, capabilities: CAPS, status: { hostname: "second-node" } });
  try {
    second.writeManifest(h.root, "node-b");
    const panel = await h.openPanel();
    await openModuleSettings(panel,"node");
    await panel.getByTestId("settings-name").fill("unsaved-node-name");
    const page = await openTray();
    await page.locator(".instance-picker > button").click();
    await page.getByRole("menuitemradio", { name: "second-node", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("未保存");
    await expect(panel.getByRole("dialog", { name: "离开当前页面？" })).toBeVisible();
    await panel.getByTestId("confirm-cancel").click();
    await expect(panel.getByTestId("settings-name")).toHaveValue("unsaved-node-name");
    const selected = await h.app.evaluate(
      () =>
        (
          globalThis as unknown as { __xmeshUiTest: { snapshot(): { selectedInstance: string } } }
        ).__xmeshUiTest.snapshot().selectedInstance,
    );
    expect(selected).toBe("node-a");
  } finally {
    await second.stop();
  }
});

test("English and dark compact layouts keep the footer visible and record native screenshot evidence", async () => {
  h = await launch({ lang: "en", kernel: { capabilities: CAPS } });
  await expectIcon(h, "ok");
  const page = await openTray();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await expect(page.getByRole("tab", { name: "Overview", exact: true })).toBeVisible();
  await emitCall();
  await expect(page.locator(".call-row")).toHaveCount(1);
  for (const theme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: theme });
    for (const [name, file] of [
      ["Overview", "overview"],
      ["Devices", "devices"],
      ["Services", "services"],
      ["Activity", "activity"],
    ]) {
      await page.getByRole("tab", { name: name === "Devices" ? /^Devices/ : name, exact: name !== "Devices" }).click();
      await expect(page.getByRole("button", { name: "Open xmesh", exact: true })).toBeInViewport();
      if (process.env.XMESH_TRAY_EVIDENCE_DIR) {
        fs.mkdirSync(process.env.XMESH_TRAY_EVIDENCE_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.XMESH_TRAY_EVIDENCE_DIR, `compact-${file}-${theme}.png`) });
      }
    }
  }
  expect(errors).toEqual([]);
});

test("a second renderer using the tray preload cannot read state or execute tray commands", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  await openTray();
  const preferences = await h.app.evaluate(() =>
    (
      globalThis as unknown as { __xmeshUiTest: { trayPreferences(): Electron.WebPreferences } }
    ).__xmeshUiTest.trayPreferences(),
  );
  const created = h.app.waitForEvent("window");
  await h.app.evaluate(({ BrowserWindow }, preload) => {
    const other = new BrowserWindow({
      show: false,
      webPreferences: { preload, sandbox: true, contextIsolation: true },
    });
    void other.loadURL("app://tray/index.html");
  }, preferences.preload!);
  const other = await created;
  await other.waitForLoadState();
  const result = await other.evaluate(async () => {
    try {
      await (window as unknown as { xmeshTray: { getState(): Promise<unknown> } }).xmeshTray.getState();
      return "allowed";
    } catch (e) {
      return String(e);
    }
  });
  expect(result).toContain("untrusted sender");
});

test("application quit closes an open tray window instead of being cancelled by hide-on-close", async () => {
  h = await launch();
  await openTray();
  const child = h.app.process();
  const closing = h.app.waitForEvent("close");
  await h.app
    .evaluate(({ app }) => {
      app.quit();
    })
    .catch(() => {});
  await closing;
  await expect.poll(() => child.exitCode).toBe(0);
});

test("native tray restart confirms once and cancellation leaves running work intact", async () => {
  h = await launch(); await expectIcon(h, "ok");
  await h.app.evaluate(() => { (globalThis as unknown as { __xmeshUiTest: { answers: { confirm: boolean } } }).__xmeshUiTest.answers.confirm = false; });
  await h.clickMenu("settings.restartKernel");
  expect((await h.calls()).filter(c => c.op === "confirm")).toHaveLength(1);
  expect((await h.calls()).some(c => c.op === "cli")).toBe(false);
  await h.app.evaluate(() => { (globalThis as unknown as { __xmeshUiTest: { answers: { confirm: boolean } } }).__xmeshUiTest.answers.confirm = true; });
  await h.clickMenu("settings.restartKernel");
  expect((await h.calls()).filter(c => c.op === "confirm")).toHaveLength(2);
  expect((await h.calls()).filter(c => c.op === "cli").map(c => c.arg)).toEqual(["restart"]);
});

test("320 DIP work areas keep device relationships, service actions and footer inside the viewport", async () => {
  h = await launch({ lang: "en", kernel: { capabilities: CAPS } }); await expectIcon(h, "ok");
  const page = await openTray();
  await page.setViewportSize({ width: 304, height: 364 });
  for (const tab of ["Overview", "Devices", "Services", "Activity"]) {
    await page.getByRole("tab", { name: tab === "Devices" ? /^Devices/ : tab }).click();
    const geometry = await page.evaluate(() => {
      const viewport = document.documentElement.clientWidth;
      const targets = [...document.querySelectorAll<HTMLElement>("button, input, .node-pill, .backend-row")].filter(el => el.getClientRects().length);
      return targets.filter(el => { const r = el.getBoundingClientRect(); return r.left < -1 || r.right > viewport + 1; }).map(el => el.textContent || el.getAttribute("aria-label"));
    });
    expect(geometry, `${tab} controls must fit horizontally`).toEqual([]);
    if (process.env.XMESH_TRAY_EVIDENCE_DIR) {
      fs.mkdirSync(process.env.XMESH_TRAY_EVIDENCE_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.XMESH_TRAY_EVIDENCE_DIR, `narrow-${tab.toLowerCase()}.png`) });
    }
    await expect(page.getByRole("button", { name: "Open xmesh", exact: true })).toBeInViewport();
    if (tab === "Services") {
      await page.getByRole("button", { name: "Claude Code", exact: true }).click();
      await expect.poll(async () => (await h.calls()).some(c => c.op === "clipboard")).toBe(true);
    }
  }
});
