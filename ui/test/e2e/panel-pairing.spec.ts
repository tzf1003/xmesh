// Connection-password pairing on the Devices page, against the fake kernel: setting and clearing the
// password, connecting to a device found on the LAN, and adding one by hand, through the real
// Electron app with main-process IPC in between.

import { type Page, expect, test } from "@playwright/test";
import type { Capability } from "../../src/shared/control";
import { PEER_PASSWORD } from "../../testserver/panel-routes";
import { type Harness, launch } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit"];
const LAN_STATUS = { network: { mode: "lan" as const, node_id: "win-dev-01", tags: ["tag:device"] } };

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

async function openDevices(): Promise<Page> {
  h = await launch({ kernel: { capabilities: ALL, status: LAN_STATUS } });
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
  await page.getByTestId("nav-devices").click();
  await expect(page.getByTestId("page-devices")).toBeVisible();
  return page;
}

for (const lang of ["zh", "en"] as const) {
  test(`pairing: local mode explains device setup and keeps document pairing (${lang})`, async () => {
    h = await launch({ lang, kernel: { capabilities: ALL, status: { network: { mode: "local", node_id: "local", tags: ["tag:peer"] } } } });
    h.kernel.failNext("/v2/pair/password", 503, "unavailable", "设备配对服务尚未就绪");
    h.kernel.failNext("/v2/pair/discovered", 503, "unavailable", "设备配对服务尚未就绪");
    const page = await h.openPanel();
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => { if (["error", "warning"].includes(message.type())) errors.push(message.text()); });
    await expect(page).toHaveTitle("xmesh");
    await expect(page).toHaveURL(/^app:\/\/panel/);
    await page.getByTestId("nav-devices").click();
    await expect(page.getByTestId("pair-local-mode")).toContainText(lang === "zh" ? "本机模式" : "local mode");
    await expect(page.getByTestId("pair-password")).toHaveCount(0);
    await expect(page.getByTestId("pair-discovered")).toHaveCount(0);
    await expect(page.getByTestId("pair-manual-form")).toHaveCount(0);
    expect(h.kernel.requests.filter((request) => ["/v2/pair/password", "/v2/pair/discovered", "/v2/pair/connect"].includes(request.path))).toEqual([]);
    await page.getByTestId("pair-advanced").locator("summary").click();
    await page.getByTestId("pair-export").click();
    await expect(page.getByTestId("pair-my-fingerprint")).toBeVisible();
    await page.getByTestId("pair-advanced").locator("summary").click();
    await page.screenshot({ path: test.info().outputPath(`local-devices-${lang}.png`) });
    await page.getByTestId("pair-local-setup").click();
    await expect(page.getByTestId("page-setup")).toBeVisible();
    await page.getByTestId("purpose-multi-device").click();
    await expect(page.getByTestId("purpose-multi-device")).toHaveAttribute("aria-pressed", "true");
    expect(errors).toEqual([]);
  });
}

for (const mode of ["tailnet", "coexist"] as const) {
  test(`pairing: ${mode} offers connection passwords`, async () => {
    h = await launch({ kernel: { capabilities: ALL, status: { network: { ...LAN_STATUS.network, mode } } } });
    const page = await h.openPanel();
    await page.getByTestId("nav-devices").click();
    await expect(page.getByTestId("pair-password-state")).toContainText("尚未设置");
    await expect(page.getByTestId("pair-local-mode")).toHaveCount(0);
  });
}

test("pairing: service unavailability reports the pairing state", async () => {
  h = await launch({ kernel: { capabilities: ALL, status: LAN_STATUS } });
  h.kernel.failNext("/v2/pair/password", 503, "unavailable", "设备配对服务尚未就绪");
  const page = await h.openPanel();
  await page.getByTestId("nav-devices").click();
  await expect(page.getByTestId("pair-password-error")).toHaveText("设备配对暂不可用，请稍后重试。");
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
});

test("pairing: set the connection password, change it, then clear it after confirming", async () => {
  const page = await openDevices();
  await expect(page.getByTestId("pair-password")).toContainText("尚未设置");
  // The submit button stays off until both entries match and meet the rules.
  await page.getByTestId("pair-pw-new").fill("short");
  await expect(page.getByTestId("pair-pw-submit")).toBeDisabled();
  await page.getByTestId("pair-pw-new").fill("my local password");
  await page.getByTestId("pair-pw-again").fill("my local passwort");
  await expect(page.getByTestId("pair-pw-mismatch")).toBeVisible();
  await page.getByTestId("pair-pw-again").fill("my local password");
  await page.getByTestId("pair-pw-submit").click();
  await expect(page.getByTestId("pair-password-state")).toContainText("已设置");
  expect(h.kernel.panel.pairPassword.set).toBe(true);
  // The value itself is never shown again.
  await expect(page.getByTestId("pair-password")).not.toContainText("my local password");

  await page.getByTestId("pair-pw-clear").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("pair-password-state")).toContainText("尚未设置");
  expect(h.kernel.panel.pairPassword.set).toBe(false);
});

test("pairing: a device found on the LAN is offered for connection; a wrong password is refused, the right one pairs", async () => {
  const page = await openDevices();
  await expect(page.getByTestId("pair-discovered")).toContainText("发现 win-lan");
  await expect(page.getByTestId("pair-discovered")).toContainText("mac-lan");

  await page.getByTestId("pair-open-win-lan").click();
  await page.getByTestId("pair-connect-win-lan-password").fill("not the password at all");
  await page.getByTestId("pair-connect-win-lan-submit").click();
  await expect(page.getByTestId("pair-connect-win-lan-error")).toContainText("连接密码不正确");
  // The password field is cleared once the kernel has it, so a retry types it again.
  await expect(page.getByTestId("pair-connect-win-lan-password")).toHaveValue("");
  expect(h.kernel.panel.imported).toEqual([]);

  await page.getByTestId("pair-connect-win-lan-password").fill(PEER_PASSWORD);
  await page.getByTestId("pair-connect-win-lan-submit").click();
  await expect(page.getByTestId("pair-device-win-lan")).toHaveCount(0);
  expect(h.kernel.panel.imported.map((i) => i.name)).toEqual(["win-lan"]);
});

test("pairing: a device added by hand reports unreachable and rate-limited peers in words", async () => {
  const page = await openDevices();
  await page.getByTestId("pair-manual-form-address").fill("10.0.0.9.unreachable");
  await page.getByTestId("pair-manual-form-password").fill(PEER_PASSWORD);
  await page.getByTestId("pair-manual-form-submit").click();
  await expect(page.getByTestId("pair-manual-form-error")).toContainText("无法连接到对方设备");

  await page.getByTestId("pair-manual-form-address").fill("10.0.0.9.limited");
  await page.getByTestId("pair-manual-form-password").fill(PEER_PASSWORD);
  await page.getByTestId("pair-manual-form-submit").click();
  await expect(page.getByTestId("pair-manual-form-error")).toContainText("30 秒后再试");
  expect(h.kernel.panel.imported).toEqual([]);
});

test("pairing: the document flow sits under advanced and still works", async () => {
  const page = await openDevices();
  await expect(page.getByTestId("pair-advanced")).not.toHaveAttribute("open", /.*/);
  await page.getByTestId("pair-advanced").locator("summary").click();
  await page.getByTestId("pair-export").click();
  await expect(page.getByTestId("pair-my-fingerprint")).toBeVisible();
});
