import { expect, test } from "@playwright/test";
import { launch, navigate } from "./helpers";

test("desktop sessions can be revoked and human takeover pauses Agent control", async () => {
  const h = await launch({ kernel: { capabilities: ["status", "events", "peers", "sessions", "computer", "config.read", "config.write"] } });
  try {
    h.kernel.panel.computer.sessions = [{ session_id: "cs-12345678", desktop_id: "desktop", desktop_epoch: "epoch", mode: "control", expires_at: new Date(Date.now() + 120000).toISOString(), owner: "cloud/session" }];
    const page = await h.openPanel();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await navigate(page, "computer");
    await expect(page.getByTestId("computer-state")).toHaveText("桌面已就绪");
    await page.getByTestId("computer-setup-button").click();
    await expect.poll(async () => (await h.calls()).some((c) => c.op === "computer-setup")).toBe(true);
    await expect(page.getByTestId("computer-sessions")).toContainText("cloud/session");
    await page.getByTestId("computer-revoke-cs-12345678").click();
    await expect(page.getByTestId("page-computer")).toContainText("当前没有桌面会话");
    await page.getByTestId("computer-takeover").click();
    await expect(page.getByTestId("computer-state")).toHaveText("Agent 控制已暂停");
    await expect(page.getByTestId("computer-takeover")).toBeDisabled();
    await page.screenshot({ path: test.info().outputPath("computer-page.png"), fullPage: true });
    await page.getByTestId("computer-pause").click();
    await expect(page.getByTestId("computer-state")).toHaveText("桌面已就绪");
    expect(h.kernel.panel.computer.paused).toBe(false);
    expect(errors).toEqual([]);
  } finally { await h.close(); }
});

test("older kernels explain desktop capability availability", async () => {
  const h = await launch();
  try { const page = await h.openPanel(); await navigate(page, "computer"); await expect(page.getByTestId("page-computer")).toHaveCount(0); await expect(page.locator("main")).toContainText("桌面控制"); }
  finally { await h.close(); }
});

test("desktop preview renders MCP images with observation leases and clears on exit", async () => {
  const h = await launch({ kernel: { playground: true, capabilities: ["status", "events", "peers", "sessions", "computer", "pats", "config.read", "config.write"] } });
  try {
    const desktop = h.kernel.playground!.enableDesktop();
    h.kernel.panel.pats.find((p) => p.name === "default")!.scopes.push("computer:observe:*");
    const page = await h.openPanel();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
    await navigate(page, "computer");
    await expect(page.getByTestId("computer-preview-refresh")).toBeDisabled();
    await page.getByTestId("computer-preview-pat").selectOption("default");
    desktop.image = (await page.screenshot({ scale: "css" })).toString("base64");
    const viewport = await page.evaluate(() => ({ width: window.innerWidth, height: window.innerHeight }));
    desktop.width = viewport.width; desktop.height = viewport.height;
    await page.getByTestId("computer-preview-refresh").click();
    const image = page.getByTestId("computer-preview-image");
    await expect(image).toBeVisible();
    await expect.poll(() => image.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
    await expect.poll(() => desktop.sessions.size).toBe(0);
    expect(desktop.modes).toEqual(["observe"]);
    await page.screenshot({ path: "/tmp/xmesh-computer-preview-desktop.png", fullPage: true });
    await page.setViewportSize({ width: 900, height: 700 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: "/tmp/xmesh-computer-preview-narrow.png", fullPage: true });
    await page.getByTestId("computer-preview-auto").check();
    await expect.poll(() => desktop.captures).toBeGreaterThan(1);
    await page.getByTestId("computer-preview-clear").click();
    await expect(image).toHaveCount(0);
    await expect(page.getByTestId("computer-preview-auto")).not.toBeChecked();
    const captures = desktop.captures;
    await page.waitForTimeout(2200);
    expect(desktop.captures).toBe(captures);
    await page.getByTestId("computer-preview-refresh").click();
    await expect(image).toBeVisible();
    await navigate(page, "overview");
    await navigate(page, "computer");
    await expect(image).toHaveCount(0);
    await expect(page.getByTestId("computer-preview-refresh")).toBeDisabled();
    expect(errors).toEqual([]);
  } finally { await h.close(); }
});

test("desktop preview binds the selected window and rejects a closed target", async () => {
  const h = await launch({ kernel: { playground: true, capabilities: ["status", "events", "peers", "sessions", "computer", "pats", "config.read", "config.write"] } });
  try {
    const playground = h.kernel.playground!;
    const desktop = playground.enableDesktop();
    desktop.targets = [
      { id: "window-one", name: "Fixture editor", app_id: "app.xmesh.fixture" },
      { id: "window-two", name: "Fixture editor", app_id: "app.xmesh.fixture" },
    ];
    h.kernel.panel.pats.find((p) => p.name === "default")!.scopes.push("computer:observe:*");
    const page = await h.openPanel();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
    await navigate(page, "computer");
    await page.getByTestId("computer-preview-pat").selectOption("default");
    desktop.image = (await page.screenshot({ scale: "css" })).toString("base64");
    const viewport = await page.evaluate(() => ({ width: innerWidth, height: innerHeight }));
    desktop.width = viewport.width; desktop.height = viewport.height;
    const refresh = page.getByTestId("computer-preview-refresh");
    const image = page.getByTestId("computer-preview-image");
    await refresh.click();
    await expect(image).toBeVisible();
    const target = page.getByTestId("computer-preview-target");
    await expect(target.locator("option")).toHaveCount(3);
    await target.selectOption("window-two");
    await expect(image).toHaveCount(0);
    await refresh.click();
    await expect(image).toBeVisible();
    await expect.poll(() => image.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
    await expect.poll(() => desktop.sessions.size).toBe(0);
    const opens = () => playground.calls.filter((call) => call.name === "local/computer.open");
    expect(opens().at(-1)!.args).toMatchObject({ mode: "observe", interaction: "background", target_id: "window-two" });
    expect(playground.calls.filter((call) => call.name === "local/computer.observe").at(-1)!.args).not.toHaveProperty("display_id");
    const opened = opens().length;
    desktop.targets = desktop.targets.filter((window) => window.id !== "window-two");
    await refresh.click();
    await expect(page.getByTestId("desktop-preview")).toContainText("Selected application window is unavailable");
    await expect(image).toHaveCount(0);
    expect(opens()).toHaveLength(opened);
    await target.selectOption("");
    await refresh.click();
    await expect(image).toBeVisible();
    await expect.poll(() => desktop.sessions.size).toBe(0);
    expect(opens().at(-1)!.args).not.toHaveProperty("target_id");
    expect(desktop.modes.every((mode) => mode === "observe")).toBe(true);
    await page.screenshot({ path: test.info().outputPath("computer-window-preview.png"), fullPage: true });
    expect(errors).toEqual([]);
  } finally { await h.close(); }
});
