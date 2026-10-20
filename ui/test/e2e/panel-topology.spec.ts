import { expect, test, type Page } from "@playwright/test";
import * as fs from "node:fs";
import * as path from "node:path";
import type { Capability } from "../../src/shared/control";
import { peersFixture } from "../../testserver/fixtures";
import { launch, navigate, type Harness } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit", "gateway"];
let h: Harness;
test.afterEach(async () => { await h?.close(); });
async function open(): Promise<Page> {
  h = await launch({ kernel: { capabilities: ALL } });
  const page = await h.openPanel();
  await expect(page.getByTestId("topology")).toBeVisible();
  return page;
}
async function capture(page: Page, name: string): Promise<void> {
  if (!process.env.XMESH_UI_QA_DIR) return;
  fs.mkdirSync(process.env.XMESH_UI_QA_DIR, { recursive: true });
  await page.screenshot({ path: path.join(process.env.XMESH_UI_QA_DIR, name), fullPage: true, animations: "disabled", scale: "css" });
}

async function configuration(page: Page): Promise<void> {
  await navigate(page, "settings");
  await page.getByTestId("settings-tab-diagnostics").click();
  await page.getByRole("button", { name: "修复配置", exact: true }).click();
  await page.getByTestId("config-form-tab").click();
}

test("topology renders real routes, selects details and filters without moving peers", async () => {
  const page = await open();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  await page.setViewportSize({ width: 1440, height: 1180 });
  await page.emulateMedia({ colorScheme: "light" });
  const mac = page.locator('[data-node="mac-dev-01"]');
  await expect(page.locator('[data-edge="mac-dev-01"]')).toHaveAttribute("data-kind", "direct");
  await expect(page.locator('[data-edge="cloud-vm"]')).toHaveAttribute("data-kind", "derp");
  await expect(page.locator('[data-edge="linux-box"]')).toHaveAttribute("data-kind", "offline");
  expect(await page.title()).toBe("xmesh");
  expect(page.url()).toMatch(/^app:\/\/panel/);
  await mac.click();
  await expect(page.getByTestId("topology-inspector")).toContainText("mac-dev-01");
  const position = await mac.getAttribute("style");
  await page.getByTestId("topology-online").check();
  await expect(page.locator('[data-node="linux-box"]')).toHaveCount(0);
  await page.getByTestId("topology-online").uncheck();
  expect(await mac.getAttribute("style")).toBe(position);
  await page.getByTestId("topology-tag").selectOption("tag:ingress");
  await expect(mac).toHaveCount(0);
  await expect(page.locator('[data-node="linux-box"]')).toBeVisible();
  await page.getByTestId("topology-tag").selectOption("");
  h.kernel.emit("call", { tool: "exec.run", device: "win-dev-01", caller: "codex", oauth_client: "Codex Cloud", status: "ok", error: null, duration_ms: 842 });
  h.kernel.emit("call", { tool: "fs.read", device: "mac-dev-01", caller: "claude", oauth_client: "Claude Cloud", status: "ok", error: null, duration_ms: 38 });
  await expect(page.locator('[data-node="caller:Codex Cloud"]')).toBeVisible();
  await page.locator('[data-node="win-dev-01"]').click();
  await capture(page, "desktop-light.png");
  await page.emulateMedia({ colorScheme: "dark" });
  await capture(page, "desktop-dark.png");
  expect(errors).toEqual([]);
});

test("soft echoes stay centered behind the graph and follow reduced-motion preferences", async () => {
  const page = await open();
  await page.setViewportSize({ width: 1440, height: 1050 });
  await page.emulateMedia({ reducedMotion: "no-preference", colorScheme: "dark" });
  const background = page.locator(".graph-background");
  const echo = page.locator(".graph-echo");
  const wave = page.locator(".graph-wave").first();
  await expect(background).toBeVisible();
  const local = await page.locator(".graph-node.self").boundingBox();
  const light = await echo.boundingBox();
  expect(Math.abs(light!.width - light!.height)).toBeLessThan(1);
  expect(Math.abs(light!.x + light!.width / 2 - local!.x - local!.width / 2)).toBeLessThan(1);
  expect(Math.abs(light!.y + light!.height / 2 - local!.y - local!.height / 2)).toBeLessThan(1);
  const before = await wave.evaluate((el) => getComputedStyle(el).transform);
  await expect.poll(() => wave.evaluate((el) => getComputedStyle(el).transform)).not.toBe(before);
  await page.locator('[data-node="mac-dev-01"]').click();
  await expect(page.getByTestId("topology-inspector").locator("h2")).toHaveText("mac-dev-01");
  await page.getByTestId("topology-canvas").click({ position: { x: 8, y: 8 } });
  await expect(page.locator(".graph-node.self")).toHaveAttribute("aria-pressed", "true");
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(background).toBeHidden();
  await expect(page.locator(".graph-ring").first()).toBeVisible();
  await expect(wave).toHaveCSS("animation-name", "none");
});

test("new and reconnected peers receive one arrival echo per online transition", async () => {
  const page = await open();
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await expect(page.locator(".graph-node.arriving")).toHaveCount(0);
  const joined = { ...peersFixture()[1]!, name: "new-device", node_id: "new-device", device_id: "new-device" };
  let peers = [...peersFixture(), joined];
  h.kernel.setPeers(peers);
  h.kernel.emit("peer", { change: "added", peer: joined });
  const node = page.locator('[data-node="new-device"]');
  await expect(node).toHaveClass(/arriving/);
  expect(await node.evaluate((el) => getComputedStyle(el, "::after").animationName)).toBe("graph-arrival");
  await expect(node).not.toHaveClass(/arriving/);
  const requests = h.kernel.requests.filter((r) => r.path === "/v2/peers").length;
  h.kernel.emit("peer", { change: "updated", peer: joined });
  await expect.poll(() => h.kernel.requests.filter((r) => r.path === "/v2/peers").length).toBeGreaterThan(requests);
  await expect(node).not.toHaveClass(/arriving/);
  await page.getByTestId("topology-tag").selectOption("tag:ingress");
  await expect(node).toHaveCount(0);
  await page.getByTestId("topology-tag").selectOption("");
  await node.click();
  await expect(node).not.toHaveClass(/arriving/);

  peers = peers.map((p) => p.name === joined.name ? { ...p, online: false } : p);
  h.kernel.setPeers(peers);
  h.kernel.emit("peer", { change: "updated", peer: { ...joined, online: false } });
  await expect(node).toHaveClass(/offline/);
  peers = peers.map((p) => p.name === joined.name ? joined : p);
  h.kernel.setPeers(peers);
  h.kernel.emit("peer", { change: "updated", peer: joined });
  await expect(node).toHaveClass(/arriving/);
  await expect(node).not.toHaveClass(/arriving/);
  await navigate(page, "sessions");
  await navigate(page, "overview");
  await expect(node).toBeVisible();
  await expect(page.locator(".graph-node.arriving")).toHaveCount(0);

  await page.emulateMedia({ reducedMotion: "reduce" });
  const recovered = { ...peersFixture()[3]!, online: true };
  h.kernel.setPeers(peers.map((p) => p.name === recovered.name ? recovered : p));
  h.kernel.emit("peer", { change: "updated", peer: recovered });
  const recoveredNode = page.locator(`[data-node="${recovered.node_id}"]`);
  await expect(recoveredNode).toHaveClass(/arriving/);
  expect(await recoveredNode.evaluate((el) => getComputedStyle(el, "::after").animationName)).toBe("none");
});

test("recent calls identify observed callers and the inspector filters by device", async () => {
  const page = await open();
  h.kernel.emit("call", { tool: "exec.run", device: "mac-dev-01", caller: "test-cli", oauth_client: null, status: "ok", error: null, duration_ms: 120 });
  await expect(page.locator('[data-node="caller:test-cli"]')).toContainText("最近调用方");
  await page.locator('[data-node="caller:test-cli"]').click();
  await expect(page.getByTestId("topology-inspector")).toContainText("exec.run");
  await expect(page.getByTestId("topology-inspector").locator("h2")).toHaveText("test-cli");
  await page.locator('[data-node="mac-dev-01"]').click();
  await page.getByTestId("inspector-tab-calls").click();
  await expect(page.getByTestId("topology-inspector")).toContainText("exec.run");
  await page.getByTestId("topology-configuration").click();
  await expect(page.getByTestId("runtime-configuration")).toContainText("Gateway");
  await expect(page.getByTestId("runtime-configuration")).toContainText("未配置 MCP 后端");
  await capture(page, "configuration.png");
});

test("the primary client action copies usable configuration through the main process", async () => {
  const page = await open();
  await page.getByTestId("topology-copy-client").click();
  await expect.poll(async () => (await h.calls()).filter((c) => c.op === "clipboard").length).toBe(1);
  const copied = (await h.calls()).find((c) => c.op === "clipboard")?.arg as string;
  expect(copied).toContain("http://127.0.0.1:8080/mcp");
  expect(copied).toContain(h.kernel.panel.tokenOf("default"));
  await expect(page.getByTestId("toast").first()).toContainText("已复制");
  expect(await page.content()).not.toContain(h.kernel.panel.tokenOf("default")!);
});

test("command search uses keyboard navigation and opens a device or configuration", async () => {
  const page = await open();
  await page.keyboard.press("Control+k");
  await page.getByTestId("command-input").fill("linux-box");
  await page.getByTestId("command-input").press("Enter");
  await expect(page.getByTestId("command-palette")).toHaveCount(0);
  await expect(page.getByTestId("topology-inspector")).toContainText("linux-box");
  await page.locator('[data-node="mac-dev-01"]').click();
  await page.getByTestId("open-search").click();
  await page.getByTestId("command-input").fill("linux-box");
  await page.getByTestId("command-input").press("Enter");
  await expect(page.getByTestId("topology-inspector").locator("h2")).toHaveText("linux-box");
  await page.getByTestId("open-search").click();
  await page.getByTestId("command-input").fill("不存在的设备");
  await expect(page.getByTestId("command-palette")).toContainText("没有找到匹配项");
  await page.keyboard.press("Escape");
  await configuration(page);
  await expect(page.getByTestId("settings-name")).toBeVisible();
  await page.getByTestId("settings-name").fill("changed-node");
  await page.getByTestId("config-source-tab").click();
  await expect(page.getByTestId("confirm")).toBeVisible();
  await page.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("settings-name")).toHaveValue("changed-node");
  await page.getByTestId("config-source-tab").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("config-editor")).toBeVisible();
  await page.getByTestId("config-editor").fill("# draft\n");
  await page.getByTestId("config-form-tab").click();
  await page.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("config-editor")).toHaveValue("# draft\n");
});

test("compact layout, large text and empty state remain usable", async () => {
  const page = await open();
  await page.setViewportSize({ width: 640, height: 720 });
  await expect(page.locator(".graph-list")).toBeVisible();
  await page.locator(".graph-list-row").filter({ hasText: "mac-dev-01" }).click();
  await expect(page.getByTestId("topology-inspector")).toContainText("mac-dev-01");
  h.kernel.emit("call", { tool: "exec.run", device: "mac-dev-01", caller: "compact-cli", oauth_client: null, status: "ok", error: null, duration_ms: 20 });
  await page.locator('[data-caller="compact-cli"]').click();
  await expect(page.getByTestId("topology-inspector").locator("h2")).toHaveText("compact-cli");
  await expect(page.getByTestId("topology-inspector")).toContainText("exec.run");
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false);
  await capture(page, "compact.png");
  await navigate(page, "settings");
  await page.getByRole("combobox", {name:"界面缩放"}).selectOption("1.25");
  await navigate(page, "overview");
  await expect(page.locator(".graph-list")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false);
  expect(await page.locator(".content").evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(false);
  await capture(page, "compact-125.png");
  h.kernel.setPeers(peersFixture().filter((p) => p.self));
  // The controller refreshes /peers on a peer event.
  h.kernel.emit("peer", { change: "removed", peer: peersFixture()[1]! });
  await expect(page.getByTestId("topology")).toContainText("还没有其他设备入网");
});

test("peer updates retain geometry, distinguish pending routes and show sys.info errors separately", async () => {
  const page = await open();
  const mac = page.locator('[data-node="mac-dev-01"]');
  const position = await mac.getAttribute("style");
  const peers = peersFixture().reverse().map((p) => p.name === "mac-dev-01" ? { ...p, info: { refreshed_at: null, error: "sys.info timed out" } } : p.name === "cloud-vm" ? { ...p, connection: "unknown" as const } : p);
  h.kernel.setPeers(peers);
  h.kernel.emit("peer", { change: "updated", peer: peers[1]! });
  await expect(page.locator('[data-edge="cloud-vm"]')).toHaveAttribute("data-kind", "unknown");
  await expect(mac).toContainText("读取失败");
  expect(await mac.getAttribute("style")).toBe(position);
  await expect(page.locator('[data-edge="mac-dev-01"]')).toHaveAttribute("data-kind", "direct");
  await mac.click();
  await expect(page.getByTestId("topology-inspector")).toContainText("sys.info timed out");
});

test("configuration form saves through preview, source reflects the saved file, and onboarding opens its drawer", async () => {
  const page = await open();
  await page.getByTestId("page-overview").getByRole("button", { name: "接入新设备", exact: true }).first().click();
  await expect(page.getByTestId("mesh-onboard-drawer")).toBeVisible();
  await page.keyboard.press("Escape");
  h.kernel.panel.setContent([
    "node:", "  name: original-node", "  roles: [agent, gateway]", "mesh:", "  mode: lan",
    "gateway:", "  listen: 127.0.0.1:8080", "agent:", "  default_cwd: /tmp", "  cwd_policy:", "    allow: [/tmp]", "",
  ].join("\n"));
  await configuration(page);
  await page.getByTestId("settings-name").fill("saved-node");
  await page.getByTestId("settings-review").click();
  expect(h.kernel.panel.writes).toEqual([]);
  await page.getByTestId("confirm-ok").click();
  await expect.poll(() => h.kernel.panel.content).toContain("saved-node");
  await page.getByTestId("config-source-tab").click();
  await expect(page.getByTestId("config-editor")).toHaveValue(/saved-node/);
  await expect(page.getByTestId("confirm")).toHaveCount(0);
});

test("a disconnected kernel removes the live graph and still offers independent network management", async () => {
  const page = await open();
  await h.kernel.stop();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", "offline");
  await expect(page.getByTestId("topology")).toHaveCount(0);
  await navigate(page, "mesh");
  await expect(page.getByTestId("mesh-devices")).toBeVisible();
});

test("leaving the topology preserves slots when a colliding device joins", async () => {
  const page = await open();
  const mac = page.locator('[data-node="mac-dev-01"]');
  await expect(mac).toHaveAttribute("style", /--x:/);
  const before = await mac.getAttribute("style");
  await navigate(page, "sessions");
  const joined = { ...peersFixture()[1]!, name: "aa", node_id: "aa" };
  h.kernel.setPeers([...peersFixture(), joined]);
  h.kernel.emit("peer", { change: "added", peer: joined });
  await expect.poll(() => h.kernel.requests.filter((r) => r.path === "/v2/peers").length).toBeGreaterThan(1);
  await navigate(page, "overview");
  await expect(page.locator('[data-node="aa"]')).toBeVisible();
  await expect(mac).toHaveAttribute("style", before!);
});

test("the complete device list stays reachable when filters hide overflow peers", async () => {
  const page = await open();
  const peers = Array.from({ length: 12 }, (_, i) => ({ ...peersFixture()[1]!, name: `device-${i}`, node_id: `device-${i}`, online: i === 0 }));
  h.kernel.setPeers([peersFixture()[0]!, ...peers]);
  h.kernel.emit("peer", { change: "added", peer: peers[0]! });
  await expect(page.getByTestId("topology-all-devices")).toBeVisible();
  await page.getByTestId("topology-online").check();
  await expect(page.getByTestId("topology-all-devices")).toBeVisible();
  await page.getByTestId("topology-all-devices").click();
  await expect(page.getByTestId("peers-table").locator('button[data-testid^="peer-"]')).toHaveCount(12);
  await expect(page.getByTestId("device-local")).toContainText(peersFixture()[0]!.name);
});
