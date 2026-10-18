// Version check and one-click update on the About page, with the update source and the hand-over replaced by
// the app's test doubles (XMESH_UI_TEST=1): no network, no installer.

import { type Page, expect, test } from "@playwright/test";
import { menuHas, type Harness, launch } from "./helpers";

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

async function openAbout(opts: { publish?: boolean } = {}): Promise<Page> {
  h = await launch({ ...opts });
  const page = await h.openPanel();
  await page.getByTestId("nav-about").click();
  await expect(page.getByTestId("page-about")).toBeVisible();
  return page;
}

const fake = (patch: { offered?: string[]; offline?: boolean; corrupt?: boolean }): Promise<void> =>
  h.app.evaluate((_e, p) => {
    Object.assign((globalThis as unknown as { __xmeshUiTest: { update: object } }).__xmeshUiTest.update, p);
  }, patch);

test("the page shows both versions, finds a newer release and offers it in the tray and on every page", async () => {
  const page = await openAbout();
  await expect(page.getByTestId("version-panel")).toHaveText("0.1.0-beta.2");
  await expect(page.getByTestId("version-kernel")).not.toHaveText("");
  await expect(page.getByTestId("update-uptodate")).toHaveCount(0);

  await page.getByTestId("update-check").click();
  const card = page.getByTestId("update-available");
  await expect(card).toContainText("发现新版本 0.1.0-beta.3");
  await expect(card).toContainText("测试版");
  await expect(card).toContainText("Fake release 0.1.0-beta.3");
  await expect(page.getByTestId("update-install")).toBeVisible();

  const item = menuHas((await h.tray()).menu, "action.update");
  expect(item?.label).toBe("发现新版本 0.1.0-beta.3，打开面板更新");
  expect(item?.enabled).toBe(true);

  // The other pages carry a strip that leads back here.
  await page.getByTestId("nav-overview").click();
  await expect(page.getByTestId("update-strip")).toContainText("0.1.0-beta.3");
  await page.getByTestId("update-strip-view").click();
  await expect(page.getByTestId("page-about")).toBeVisible();
  await expect(page.getByTestId("update-strip")).toHaveCount(0);
});

test("with nothing newer the page says it is current", async () => {
  const page = await openAbout();
  await fake({ offered: ["0.1.0-beta.2", "0.1.0-beta.1"] });
  await page.getByTestId("update-check").click();
  await expect(page.getByTestId("update-uptodate")).toContainText("0.1.0-beta.2");
  await expect(page.getByTestId("update-install")).toHaveCount(0);
  expect(menuHas((await h.tray()).menu, "action.update")).toBeUndefined();
});

test("a check that cannot reach the release server says so", async () => {
  const page = await openAbout();
  await fake({ offline: true });
  await page.getByTestId("update-check").click();
  await expect(page.getByTestId("update-error")).toContainText("连不上 GitHub");
});

test("updating stops the kernel, hands the verified installer over and quits; cancelling the confirmation does nothing", async () => {
  const page = await openAbout();
  await page.getByTestId("update-check").click();
  await page.getByTestId("update-install").click();
  await expect(page.getByTestId("confirm")).toContainText("更新到 0.1.0-beta.3？");
  await page.getByTestId("confirm-cancel").click();
  expect((await h.calls()).filter((c) => c.op.startsWith("update"))).toEqual([]);

  await page.getByTestId("update-install").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("update-installing")).toContainText("0.1.0-beta.3");
  const calls = (await h.calls()).filter((c) => c.op.startsWith("update") || c.op === "quit");
  expect(calls).toEqual([
    { op: "update-stop", arg: "node-a" },
    { op: "update-handover", arg: expect.stringMatching(/^xmesh-ui-0\.1\.0-beta\.3-(win|mac)-(x64|arm64)\.(exe|dmg)$/) },
    { op: "quit" },
  ]);
});

test("a download that fails verification is refused before anything is stopped", async () => {
  const page = await openAbout();
  await fake({ corrupt: true });
  await page.getByTestId("update-check").click();
  await page.getByTestId("update-install").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("update-error")).toContainText("没有通过校验");
  expect((await h.calls()).filter((c) => c.op.startsWith("update"))).toEqual([]);
  await expect(page.getByTestId("update-install")).toBeVisible();
});

test("the switches are remembered and the beta switch re-checks", async () => {
  const page = await openAbout();
  await expect(page.getByTestId("pref-auto")).toBeChecked();
  await expect(page.getByTestId("pref-prerelease")).toBeChecked();
  await page.getByTestId("update-check").click();
  await expect(page.getByTestId("update-available")).toBeVisible();
  await page.getByTestId("pref-prerelease").uncheck();
  await expect(page.getByTestId("update-available")).toHaveCount(0);
  await expect(page.getByTestId("pref-prerelease")).not.toBeChecked();
  await page.getByTestId("pref-auto").uncheck();
  await expect(page.getByTestId("pref-auto")).not.toBeChecked();
});

test("the page works while the kernel is not running", async () => {
  const page = await openAbout({ publish: false });
  await expect(page.getByTestId("version-kernel")).toHaveText("未运行");
  await page.getByTestId("update-check").click();
  await expect(page.getByTestId("update-available")).toBeVisible();
});
