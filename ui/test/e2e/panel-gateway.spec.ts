// The local gateway on the overview against the fake kernel: the addresses and copy commands (the token
// is read in main and never reaches the page), the upgrade notice, the reach editor and token rotation.

import { type Page, expect, test } from "@playwright/test";
import * as fs from "node:fs";
import type { Capability, GatewayInfo } from "../../src/shared/control";
import { FAKE_LAN_IP, FAKE_TAILNET_IP, FAKE_TAILNET_NAME } from "../../testserver/panel-routes";
import { type Harness, launch } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit", "gateway"];
/** The PAT the copied commands carry: the first one by name (the fixture's "claude-code"). */
const COPIED_PAT = "claude-code";
/** The PAT rotation targets and the upgrade notice are about. */
const GATEWAY_PAT = "default";
const LOOPBACK: GatewayInfo = {
  listen: "127.0.0.1:8080",
  url: "http://127.0.0.1:8080/mcp",
  auth: "pat",
  access: ["localhost"],
  hosts: [],
  addresses: [{ kind: "localhost", host: "127.0.0.1", url: "http://127.0.0.1:8080/mcp" }],
};

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

async function open(opts: Parameters<typeof launch>[0] = {}): Promise<Page> {
  h = await launch({ ...opts, kernel: { capabilities: ALL, ...opts.kernel } });
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
  // The rotate button shows once the PAT list has loaded, so the card's data is in.
  await expect(page.getByTestId("gateway-rotate")).toBeVisible();
  return page;
}

async function clipboard(): Promise<string[]> {
  return (await h.calls()).filter((c) => c.op === "clipboard").map((c) => c.arg as string);
}

function putBodies(): unknown[] {
  return h.kernel.requests.filter((r) => r.method === "PUT" && r.path === "/v1/gateway").map((r) => r.body);
}

async function rotate(page: Page): Promise<void> {
  await page.getByTestId("gateway-rotate").click();
  await expect(page.getByTestId("confirm")).toBeVisible();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("confirm")).toHaveCount(0);
}

test("overview lists every reachable address with its kind and three copy buttons", async () => {
  const page = await open();
  const rows = page.getByTestId("gateway-address");
  await expect(rows).toHaveCount(4);
  expect(await rows.evaluateAll((els) => els.map((e) => e.getAttribute("data-kind")))).toEqual([
    "localhost",
    "lan",
    "tailnet_ip",
    "tailnet_name",
  ]);
  const lan = rows.nth(1);
  await expect(lan).toContainText("内网");
  await expect(lan).toContainText(`http://${FAKE_LAN_IP}:8080/mcp`);
  for (const id of ["copy-url", "copy-claude", "copy-codex"]) await expect(lan.getByTestId(id)).toBeEnabled();
  await expect(rows.nth(3)).toContainText(FAKE_TAILNET_NAME);
});

test("each copy carries the token, which is read in main; the page never holds it", async () => {
  const page = await open();
  const token = h.kernel.panel.tokenOf(COPIED_PAT)!;
  const rows = page.getByTestId("gateway-address");

  await rows.nth(0).getByTestId("copy-url").click();
  await expect.poll(clipboard).toEqual([`http://127.0.0.1:8080/mcp\nAuthorization: Bearer ${token}`]);

  await rows.nth(2).getByTestId("copy-codex").click();
  await expect.poll(clipboard).toHaveLength(2);
  expect((await clipboard())[1]).toBe(
    `[mcp_servers.xmesh]\nurl = "http://${FAKE_TAILNET_IP}:8080/mcp"\nhttp_headers = { Authorization = "Bearer ${token}" }\n`,
  );

  await rows.nth(1).getByTestId("copy-claude").click();
  await expect.poll(clipboard).toHaveLength(3);
  expect((await clipboard())[2]).toBe(
    `claude mcp add --transport http xmesh http://${FAKE_LAN_IP}:8080/mcp --header "Authorization: Bearer ${token}"`,
  );
  await expect(page.getByTestId("toast").first()).toContainText("已复制");

  expect(await page.content()).not.toContain(token);
});

test("when the token cannot be read the copy says so and copies nothing", async () => {
  const page = await open();
  const pat = h.kernel.panel.pats.find((p) => p.name === COPIED_PAT)!;
  fs.rmSync(pat.token_file, { force: true });

  await page.getByTestId("gateway-address").nth(0).getByTestId("copy-url").click();
  await expect(page.getByTestId("toast").first()).toHaveClass(/error/);
  await expect(page.getByTestId("toast").first()).toContainText("读不到本机 Gateway 的令牌");
  expect(await clipboard()).toEqual([]);
});

test("the upgrade notice stays dismissed across reloads and comes back when the token is rotated", async () => {
  const page = await open();
  const notice = page.getByTestId("token-notice");
  await expect(notice).toContainText("本机 Gateway 已启用令牌，本机客户端需要更新配置");
  await notice.getByTestId("token-notice-dismiss").click();
  await expect(notice).toHaveCount(0);

  await page.reload();
  await expect(page.getByTestId("gateway-rotate")).toBeVisible();
  await expect(page.getByTestId("token-notice")).toHaveCount(0);

  // Rotation changes the PAT's modified_at, which makes the notice due for this viewer again.
  await rotate(page);
  await expect(page.getByTestId("token-notice")).toBeVisible();
});

test("rotating asks first; cancel sends nothing, confirm rotates and neither token reaches the page", async () => {
  const page = await open();
  const before = h.kernel.panel.tokenOf(GATEWAY_PAT)!;

  await page.getByTestId("gateway-rotate").click();
  await page.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("confirm")).toHaveCount(0);
  expect(h.kernel.requests.some((r) => r.method === "POST" && r.path.endsWith("/rotate"))).toBe(false);

  await rotate(page);
  await expect.poll(() => h.kernel.requests.filter((r) => r.method === "POST" && r.path === "/v1/pats/default/rotate").length).toBe(1);
  const after = h.kernel.panel.tokenOf(GATEWAY_PAT)!;
  expect(after).not.toBe(before);
  await expect(page.getByTestId("toast").first()).toContainText("令牌已轮换");

  const html = await page.content();
  expect(html).not.toContain(before);
  expect(html).not.toContain(after);
});

test("the reach editor saves access at once, and a listen change asks for a restart", async () => {
  const page = await open();
  const reach = page.getByTestId("reach");
  await expect(reach.getByTestId("reach-localhost")).toBeChecked();
  await expect(reach.getByTestId("reach-localhost")).toBeDisabled();
  await expect(reach.getByTestId("reach-save")).toBeDisabled();

  await reach.getByTestId("reach-lan").uncheck();
  await reach.getByTestId("reach-save").click();
  await expect(page.getByTestId("toast").first()).toContainText("可连接范围已保存");
  expect(putBodies()).toEqual([{ listen: ":8080", access: ["localhost", "tailnet"], hosts: [] }]);
  await expect(page.getByTestId("gateway-restart")).toHaveCount(0);
  await expect(page.getByTestId("gateway-address")).toHaveCount(3);

  await reach.getByTestId("reach-listen-loopback").check();
  await reach.getByTestId("reach-save").click();
  await expect(page.getByTestId("gateway-restart")).toBeVisible();
  expect(putBodies()[1]).toEqual({ listen: "127.0.0.1:8080", access: ["localhost", "tailnet"], hosts: [] });
  await expect(page.getByTestId("gateway-address")).toHaveCount(1);
});

test("a loopback-only gateway says so and offers to listen on all interfaces, keeping the port", async () => {
  const page = await open({ kernel: { status: { gateway: LOOPBACK } } });
  await expect(page.getByTestId("gateway-loopback")).toContainText("只监听本机回环");
  await expect(page.getByTestId("reach").getByTestId("reach-listen-loopback")).toBeChecked();

  await page.getByTestId("gateway-listen-all").click();
  await expect(page.getByTestId("gateway-restart")).toBeVisible();
  expect(putBodies()).toEqual([{ listen: ":8080", access: ["localhost"], hosts: [] }]);
  await expect(page.getByTestId("gateway-loopback")).toHaveCount(0);
});

test("invalid custom addresses and domain names keep Add disabled; valid ones are saved", async () => {
  const page = await open();
  const reach = page.getByTestId("reach");

  await reach.getByTestId("reach-custom-input").fill("10.0.0.0/33");
  await expect(reach.getByTestId("reach-custom-add")).toBeDisabled();
  await expect(reach.getByTestId("reach-custom-error")).toBeVisible();
  await reach.getByTestId("reach-custom-input").fill("10.0.0.0/8");
  await expect(reach.getByTestId("reach-custom-add")).toBeEnabled();
  await reach.getByTestId("reach-custom-add").click();
  await expect(reach.getByTestId("reach-custom-entry")).toContainText("10.0.0.0/8");

  await reach.getByTestId("reach-host-input").fill("192.168.1.5");
  await expect(reach.getByTestId("reach-host-add")).toBeDisabled();
  await expect(reach.getByTestId("reach-host-error")).toBeVisible();
  await reach.getByTestId("reach-host-input").fill("xmesh.home.example");
  await reach.getByTestId("reach-host-add").click();
  await expect(reach.getByTestId("reach-host-entry")).toContainText("xmesh.home.example");

  await reach.getByTestId("reach-save").click();
  await expect.poll(putBodies).toHaveLength(1);
  expect(putBodies()[0]).toEqual({
    listen: ":8080",
    access: ["localhost", "lan", "tailnet", "10.0.0.0/8"],
    hosts: ["xmesh.home.example"],
  });
});

test("the overview and the authorization page read in English", async () => {
  const page = await open({ lang: "en" });
  await expect(page.getByTestId("token-notice")).toContainText("The local Gateway now requires a token");
  await expect(page.getByTestId("gateway-address").nth(0)).toContainText("This machine");
  await expect(page.getByTestId("gateway-address").nth(0).getByTestId("copy-claude")).toHaveText("Copy Claude Code connect command");
  await expect(page.getByTestId("reach")).toContainText("Reachable from");

  await page.getByTestId("nav-authz").click();
  await expect(page.getByTestId("pats-policy")).toContainText("Every request needs a token");
});
