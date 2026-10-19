import { expect, test } from "@playwright/test";
import { type Harness, expectIcon, launch, menuHas } from "./helpers";
import { peerNamed, peersFixture, warningOf } from "../../testserver/fixtures";
import { FakeKernel } from "../../testserver/server";

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

test("no manifest: gray icon and an offline menu; the icon turns green when a kernel appears", async () => {
  h = await launch({ publish: false });
  await expectIcon(h, "offline");
  const t = await h.tray();
  expect(menuHas(t.menu, "hint.offline")).toBeTruthy();
  expect(menuHas(t.menu, "action.drain")).toBeUndefined();
  expect(menuHas(t.menu, "action.quit")?.enabled).toBe(true);

  h.publish();
  await expectIcon(h, "ok");
  const ok = await h.tray();
  expect(menuHas(ok.menu, "header.node")?.label).toBe("节点：win-dev-01");
  expect(menuHas(ok.menu, "header.peers")?.label).toBe("在线对端：2");
  expect(menuHas(ok.menu, "activity.pty")?.label).toBe("终端 1/20");
  expect(ok.tooltip).toBe("xmesh：正常");
});

test("warnings turn the icon yellow and clearing them turns it green", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  h.kernel.setWarnings([warningOf("policy_reload_failed", "error", "reload failed")]);
  await expectIcon(h, "degraded");
  const t = await h.tray();
  expect(menuHas(t.menu, "warning.0.policy_reload_failed")?.label).toBe("reload failed");
  await expect.poll(async () => (await h.notices()).join("|")).toContain("降级");
  h.kernel.setWarnings([]);
  await expectIcon(h, "ok");
});

test("info-level warnings do not turn the icon yellow", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  h.kernel.setWarnings([warningOf("allow_dangerous", "info", "allow_dangerous 已启用")]);
  await expect.poll(async () => menuHas((await h.tray()).menu, "warning.0.allow_dangerous")?.label).toBe("allow_dangerous 已启用");
  expect((await h.tray()).icon).toBe("ok");
});

test("drain and resume from the menu reach the kernel and flip the menu entry", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  expect((await h.tray()).menu.some((m) => m.id === "action.drain")).toBe(true);

  await h.clickMenu("action.drain");
  await expectIcon(h, "draining");
  expect(h.kernel.requests.find((r) => r.method === "POST" && r.path === "/v2/drain")?.body).toEqual({ mode: "keep" });
  const draining = await h.tray();
  expect(menuHas(draining.menu, "action.drain")).toBeUndefined();
  expect(menuHas(draining.menu, "action.resume")?.enabled).toBe(true);

  await h.clickMenu("action.resume");
  await expectIcon(h, "ok");
  expect(h.kernel.requests.some((r) => r.method === "POST" && r.path === "/v2/resume")).toBe(true);
});

test("restart_required shows the restart icon and a notification", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  h.kernel.setStatus({ restart_required: true });
  await expectIcon(h, "restart");
  await expect.poll(async () => (await h.notices()).join("|")).toContain("重启");
});

test("an incompatible control version shows the mismatch state and recovers when the kernel is upgraded", async () => {
  h = await launch({ kernel: { controlVersion: "3.0", kernelVersion: "9.9.9" } });
  await expectIcon(h, "mismatch");
  const t = await h.tray();
  expect(menuHas(t.menu, "hint.mismatch")?.label).toContain("9.9.9");
  expect(menuHas(t.menu, "action.openPanel")).toBeUndefined();
  expect(menuHas(t.menu, "action.drain")).toBeUndefined();
  expect(menuHas(t.menu, "action.upgrade")?.enabled).toBe(true);
  expect(h.kernel.requests).toHaveLength(0);

  // The kernel is replaced by one that speaks v1 and rewrites control.json.
  h.kernel.setControlVersion("2.0");
  h.publish();
  await expectIcon(h, "ok");
});

test("a kernel whose response header disagrees with control.json is also reported as a mismatch", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  h.kernel.setControlVersion("3.0");
  h.kernel.dropStreams();
  await expectIcon(h, "mismatch");
  h.kernel.setControlVersion("2.0");
  await expectIcon(h, "ok");
});

test("an event stream that breaks reconnects with Last-Event-ID and keeps following changes", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  await expect.poll(() => h.kernel.connectedStreams).toBe(1);

  // The client only has a Last-Event-ID once it has seen an event.
  h.kernel.setWarnings([warningOf()]);
  await expectIcon(h, "degraded");

  h.kernel.dropStreams();
  await expect.poll(() => h.kernel.connectedStreams).toBe(1);
  const reopen = h.kernel.requests.filter((r) => r.path === "/v2/events").at(-1);
  expect(reopen?.lastEventId).toBeDefined();

  h.kernel.setWarnings([]);
  await expectIcon(h, "ok");
});

test("a kernel that goes away is reported offline and found again when it returns", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  const endpoint = h.kernel.endpoint;
  await h.kernel.stop();
  await expectIcon(h, "offline");

  h.kernel = await FakeKernel.start({ endpoint, pingMs: 300 });
  await expectIcon(h, "ok");
});

test("peers going offline and online raise notices with the device name only", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  const peers = peersFixture();
  h.kernel.setPeers(peers.map((p) => (p.name === "cloud-vm" ? peerNamed("cloud-vm", { online: false, connection: "unknown" }) : p)));
  await expect.poll(async () => (await h.notices()).join("|")).toContain("设备 cloud-vm 已离线");
  h.kernel.setPeers(peers);
  await expect.poll(async () => (await h.notices()).join("|")).toContain("设备 cloud-vm 已上线");
});

test("stopping the kernel asks first and runs nothing when declined", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  await h.app.evaluate(() => {
    (globalThis as unknown as { __xmeshUiTest: { answers: { confirm: boolean } } }).__xmeshUiTest.answers.confirm = false;
  });
  await h.clickMenu("action.stopKernel");
  let calls = await h.calls();
  expect(calls.some((c) => c.op === "confirm")).toBe(true);
  expect(calls.some((c) => c.op === "cli")).toBe(false);

  await h.app.evaluate(() => {
    (globalThis as unknown as { __xmeshUiTest: { answers: { confirm: boolean } } }).__xmeshUiTest.answers.confirm = true;
  });
  await h.clickMenu("action.stopKernel");
  calls = await h.calls();
  expect(calls.filter((c) => c.op === "cli").map((c) => c.arg)).toEqual(["stop"]);
});

test("two instances: the menu lists both and switching moves the tray to the chosen node", async () => {
  h = await launch();
  const second = await FakeKernel.start({ pingMs: 300, status: { hostname: "server" } });
  try {
    second.writeManifest(h.root, "node-b");
    await expect.poll(async () => menuHas((await h.tray()).menu, "instances") !== undefined).toBe(true);
    await h.clickMenu("instance.node-b");
    await expect.poll(async () => menuHas((await h.tray()).menu, "header.node")?.label).toBe("节点：server");
  } finally {
    await second.stop();
  }
});

test("an unpackaged run never registers a login item; the menu explains why", async () => {
  h = await launch();
  await expectIcon(h, "ok");
  await h.clickMenu("settings.loginItem");
  await expect.poll(async () => (await h.notices()).join("|")).toContain("只有安装后的 xmesh");
  expect(menuHas((await h.tray()).menu, "settings.loginItem")?.checked).toBe(false);
});
