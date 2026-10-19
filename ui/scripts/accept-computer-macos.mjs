// Opt-in native macOS preview acceptance. Uses the real kernel, broker, native
// capture and panel MCP client; leaves installed nodes and services untouched.
// Build the UI and test-profile Electron fuses first (npm run test:e2e).
import { _electron as electron, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { ControlClient } = require("../dist/src/main/client.js");
const { Client, StreamableHTTPClientTransport } = require("../dist/src/main/mcp-sdk.js");
const ui = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const binary = process.env.XMESH_TEST_COMPUTER_SERVICE_BINARY;
const helper = process.env.XMESH_TEST_COMPUTER_HELPER;
if (process.platform !== "darwin" || !binary || !helper || !path.isAbsolute(binary) || !path.isAbsolute(helper)) {
  throw new Error("Requires macOS and absolute XMESH_TEST_COMPUTER_SERVICE_BINARY / XMESH_TEST_COMPUTER_HELPER paths");
}
if (process.env.XMESH_TEST_ALLOW_DESKTOP_UI !== "1") {
  throw new Error("Live preview acceptance opens a desktop window. Obtain the user's approval, then set XMESH_TEST_ALLOW_DESKTOP_UI=1.");
}
if ((process.env.XMESH_TEST_COMPUTER_FIXTURE || process.env.XMESH_TEST_COMPUTER_FIXTURE_TOKEN) && process.env.XMESH_TEST_ALLOW_DESKTOP_INPUT !== "1") {
  throw new Error("Native input acceptance shares the system mouse and keyboard; explicit XMESH_TEST_ALLOW_DESKTOP_INPUT=1 authorization is required.");
}

const root = mkdtempSync("/tmp/xmesh-mac-preview-");
const state = path.join(root, "nodes", "mac-preview");
mkdirSync(state, { recursive: true, mode: 0o700 });
const config = path.join(state, "xmesh.yaml");
const tokens = ["default", "preview", "input"].map((name) => {
  const file = path.join(root, name + ".token");
  writeFileSync(file, randomBytes(32).toString("base64url"), { mode: 0o600 });
  return file;
});
const children = [];
let app;
let completed = false;
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
function start(args) {
  const child = spawn(binary, args, { stdio: ["ignore", "pipe", "pipe"] });
  let log = "";
  for (const stream of [child.stdout, child.stderr]) stream.on("data", (data) => { log = (log + data).slice(-16384); });
  const item = { child, log: () => log };
  children.push(item);
  return item;
}
async function address() {
  const server = net.createServer();
  await new Promise((resolve, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", resolve); });
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return `127.0.0.1:${port}`;
}
async function stop(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = new Promise((resolve) => child.once("close", resolve));
  child.kill("SIGTERM");
  await Promise.race([exited, pause(5000)]);
  if (child.exitCode === null && child.signalCode === null) { child.kill("SIGKILL"); await exited; }
}
async function verifyInput(gateway) {
  const fixture = process.env.XMESH_TEST_COMPUTER_FIXTURE;
  const token = process.env.XMESH_TEST_COMPUTER_FIXTURE_TOKEN;
  if (!fixture && !token) return false;
  const url = new URL(fixture);
  if (url.protocol !== "http:" || url.hostname !== "127.0.0.1" || url.username || url.password || !token) throw new Error("An authenticated isolated loopback input fixture is required");
  const read = async () => {
    const response = await fetch(url, { headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(2000), redirect: "error" });
    expect(response.status).toBe(200);
    return response.json();
  };
  const client = new Client({ name: "xmesh-macos-acceptance", version: "1" }, { capabilities: {} });
  const transport = new StreamableHTTPClientTransport(new URL(`http://${gateway}/mcp`), { requestInit: { headers: { Authorization: `Bearer ${readFileSync(tokens[2], "utf8")}` }, redirect: "error" } });
  const call = async (name, args = {}) => {
    const result = await client.callTool({ name: `local/computer.${name}`, arguments: args });
    expect(result.isError, JSON.stringify(result.structuredContent)).not.toBe(true);
    return result;
  };
  let identity;
  try {
    await client.connect(transport);
    const target = await read();
    const info = (await call("info")).structuredContent;
    expect(info.capabilities).toContain("keyboard");
    const display = info.displays.find((d) => d.x === target.display.x && d.y === target.display.y);
    expect(display).toBeTruthy();
    const session = (await call("open", { mode: "control", interaction: "foreground", ttl_seconds: 30 })).structuredContent;
    identity = { session_id: session.session_id, desktop_epoch: session.desktop_epoch, lease_token: session.lease_token };
    const observed = await call("observe", { ...identity, display_id: display.id, max_width: 1280 });
    const frame = observed.structuredContent;
    const image = observed.content.find((content) => content.type === "image");
    expect(image.mimeType).toMatch(/^image\/(png|jpeg)$/);
    expect(Buffer.from(image.data, "base64").length).toBeGreaterThan(100);
    const point = (p) => ({
      x: Math.floor((target.content.x + p.x - target.display.x) * frame.image_width / target.display.width),
      y: Math.floor((target.content.y + p.y - target.display.y) * frame.image_height / target.display.height),
    });
    const act = async (id, actions) => {
      const result = (await call("act", { ...identity, action_id: id, frame_id: frame.frame_id, actions })).structuredContent;
      expect(result.state, JSON.stringify(result)).toBe("executed");
      expect(result.executed).toBe(actions.length);
      return result;
    };
    const click = [{ type: "click", ...point(target.click) }];
    const executed = await act("click", click);
    expect(await act("click", click)).toEqual(executed);
    expect((await call("action_status", { ...identity, action_id: "click" })).structuredContent).toEqual(executed);
    await act("text", [{ type: "click", ...point(target.text) }, { type: "text", text: "XmeSh 中文桌面" }]);
    await act("shortcut", [{ type: "key", keys: ["Meta", "a"] }, { type: "text", text: "验证完成" }]);
    const to = point(target.dragTo);
    await act("drag", [{ type: "drag", ...point(target.dragPoint), to_x: to.x, to_y: to.y, duration_ms: 300 }]);
    await act("scroll", [{ type: "scroll", ...point(target.scroll), delta_y: 3 }]);
    await expect.poll(async () => {
      const state = await read();
      return { clicks: state.clicks, value: state.value, drag: state.dragComplete, scrolled: state.scrollTop > 0 };
    }, { timeout: 5000 }).toEqual({ clicks: 1, value: "验证完成", drag: true, scrolled: true });
    expect((await call("renew", { ...identity, ttl_seconds: 30 })).structuredContent.lease_token).toBe(identity.lease_token);
    return true;
  } finally {
    try { if (identity) await call("close", identity); }
    finally {
      await transport.terminateSession().catch(() => undefined);
      await client.close();
    }
  }
}
try {
  const host = start(["computer", "host", "--state-dir", path.join(root, "host"), "--helper", helper]);
  let endpoint;
  await expect.poll(() => {
    for (const line of host.log().split("\n")) {
      try { endpoint = JSON.parse(line).endpoint; if (endpoint) return true; } catch { /* Wait for the broker announcement. */ }
    }
    if (host.child.exitCode !== null) throw new Error("Native host exited: " + host.log());
    return false;
  }, { timeout: 15000 }).toBe(true);
  const gateway = await address();
  const direct = await address();
  writeFileSync(config, JSON.stringify({
    node: { name: "mac-preview", roles: ["agent", "gateway"] },
    mesh: { mode: "lan", lan: { mdns: true } },
    gateway: { listen: gateway, pats: {
      default: { token_file: tokens[0], scopes: ["devices:read", "sys:*"] },
      preview: { token_file: tokens[1], scopes: ["devices:read", "computer:observe:mac-preview"] },
      input: { token_file: tokens[2], scopes: ["devices:read", "computer:control:mac-preview"] },
    } },
    agent: { default_cwd: root, cwd_policy: { allow: [root] }, policy: { isolation: "L1" },
      computer: { enabled: true, endpoint, allow_foreground: process.env.XMESH_TEST_ALLOW_DESKTOP_INPUT === "1" }, authz: { "tag:peer": { tools: ["*"], backends: ["*"] } } },
  }), { mode: 0o600 });
  const kernel = start(["serve", "--local", "--config", config, "--state-dir", state, "--listen", direct]);
  let manifest;
  await expect.poll(() => {
    if (kernel.child.exitCode !== null) throw new Error("Acceptance kernel exited: " + kernel.log());
    try { manifest = JSON.parse(readFileSync(path.join(state, "control.json"), "utf8")); return true; } catch { return false; }
  }, { timeout: 15000 }).toBe(true);
  const control = new ControlClient({ endpoint: manifest.endpoint });
  await expect.poll(async () => (await control.call("GET", "/v1/computer")).info.available, { timeout: 15000 }).toBe(true);
  const input = await verifyInput(gateway);
  const args = [`--user-data-dir=${path.join(root, "ui")}`, ui];
  app = await electron.launch({ args, env: { ...process.env, XMESH_UI_TEST: "1", XMESH_UI_STATE_ROOT: path.join(root, "nodes"), XMESH_UI_LANG: "zh", XMESH_BIN: binary } });
  await expect.poll(() => app.evaluate(() => globalThis.__xmeshUiTest?.snapshot().connection.kind), { timeout: 15000 }).toBe("connected");
  const opened = app.waitForEvent("window");
  await app.evaluate(() => globalThis.__xmeshUiTest.showPanel());
  const page = await opened;
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
  await page.waitForLoadState("domcontentloaded");
  expect(await page.title()).not.toBe("");
  expect(page.url().startsWith("app://")).toBe(true);
  if (!await page.getByTestId("nav-computer").isVisible()) await page.getByTestId("nav-advanced").click();
  await page.getByTestId("nav-computer").click();
  await expect(page.getByTestId("computer-state")).toHaveText("桌面已就绪");
  await expect(page.getByTestId("computer-preview-refresh")).toBeDisabled();
  const credentials = page.getByTestId("computer-preview-pat");
  await expect(credentials.locator("option[value=default]")).toHaveCount(0);
  await credentials.selectOption("preview");
  const image = page.getByTestId("computer-preview-image");
  const dimensions = [];
  for (let n = 0; n < 2; n++) {
    await page.getByTestId("computer-preview-refresh").click();
    await expect(page.getByTestId("computer-preview-refresh")).toBeEnabled({ timeout: 15000 });
    await expect(image).toBeVisible();
    await expect.poll(() => image.evaluate((img) => img.complete && img.naturalWidth > 0), { timeout: 15000 }).toBe(true);
    dimensions.push(await image.evaluate((img) => ({ width: img.naturalWidth, height: img.naturalHeight })));
    await expect.poll(async () => (await control.call("GET", "/v1/computer")).sessions.length).toBe(0);
  }
  await page.getByTestId("computer-preview-clear").click();
  await expect(image).toHaveCount(0);
  await page.getByTestId("computer-takeover").click();
  await expect.poll(async () => (await control.call("GET", "/v1/computer")).paused).toBe(true);
  await page.getByTestId("computer-pause").click();
  await expect.poll(async () => (await control.call("GET", "/v1/computer")).paused).toBe(false);
  await page.setViewportSize({ width: 900, height: 700 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  // The captured desktop stays in memory. This optional UI evidence is taken
  // only after clearing it, so the artifact contains no desktop image.
  const evidence = process.env.XMESH_TEST_COMPUTER_UI_EVIDENCE;
  if (evidence) { if (!path.isAbsolute(evidence)) throw new Error("UI evidence path must be absolute"); await page.screenshot({ path: evidence, fullPage: true }); }
  expect(errors).toEqual([]);
  console.log(JSON.stringify({ result: "PASS", platform: "darwin", images: dimensions, checks: ["real-native-preview", "repeat-capture", "explicit-observation-pat", "observation-lease-cleanup", "native-manager-takeover-resume", "900x700-layout", "renderer-console", ...(input ? ["native-mcp-click-chinese-shortcut-drag-scroll", "mcp-input-dedup-status-renew-close"] : [])] }));
  completed = true;
} finally {
  if (app) {
    const child = app.process();
    const exited = child.exitCode === null && child.signalCode === null ? new Promise((resolve) => child.once("close", resolve)) : Promise.resolve();
    await app.evaluate(({ app }) => app.exit(0)).catch(() => undefined);
    await Promise.race([exited, pause(5000)]);
    await stop(child);
  }
  for (const { child } of children.reverse()) await stop(child);
  rmSync(root, { recursive: true, force: true });
  if (!completed) console.error("macOS live preview acceptance did not complete");
}
