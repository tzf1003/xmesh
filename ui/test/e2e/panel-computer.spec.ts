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
    const page = await h.openPanel();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
    await navigate(page, "computer");
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
    expect(errors).toEqual([]);
  } finally { await h.close(); }
});
