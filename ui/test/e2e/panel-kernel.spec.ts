import { test, expect } from "@playwright/test";
import * as fs from "node:fs";
import * as path from "node:path";
import { launch, navigate, type Harness } from "./helpers";

let h: Harness;
test.afterEach(async () => { await h?.close(); });

test("kernel management exposes all six operations and confirms interruptions", async () => {
  h = await launch();
  const page = await h.openPanel();
  await navigate(page, "about");
  const card = page.getByTestId("kernel-manager");
  await expect(card).toBeVisible();
  for (const op of ["install", "uninstall", "reinstall", "start", "stop", "restart"]) await expect(card.getByTestId(`kernel-op-${op}`)).toBeVisible();
  await expect(card.getByTestId("kernel-op-install")).toBeDisabled();
  for (const op of ["uninstall", "reinstall", "stop", "restart"]) {
    await card.getByTestId(`kernel-op-${op}`).click();
    await expect(page.getByTestId("confirm")).toContainText("节点配置、令牌");
    await page.getByTestId("confirm-cancel").click();
    expect((await h.calls()).filter((c) => c.op === "kernel")).toHaveLength(["uninstall", "reinstall", "stop", "restart"].indexOf(op));
    await card.getByTestId(`kernel-op-${op}`).click();
    await page.getByTestId("confirm-ok").click();
    await expect.poll(async () => (await h.calls()).filter((c) => c.op === "kernel").at(-1)?.arg).toEqual({ op, node: "node-a" });
    await expect(card).toHaveAttribute("aria-busy", "false");
  }
  await expect(card.getByTestId("kernel-operation-error")).toHaveCount(0);
});

test("offline configuration can be installed and started, and failures stay visible", async () => {
  h = await launch({ publish: false });
  const dir = path.join(h.root, "offline-node");
  fs.mkdirSync(dir);
  fs.writeFileSync(path.join(dir, "xmesh.yaml"), "node:\n  name: offline-node\n");
  const page = await h.openPanel();
  await navigate(page, "about");
  const card = page.getByTestId("kernel-manager");
  await expect(card.getByTestId("kernel-node")).toHaveValue("offline-node");
  for (const op of ["install", "start"]) {
    await card.getByTestId(`kernel-op-${op}`).click();
    await expect.poll(async () => (await h.calls()).filter((c) => c.op === "kernel").at(-1)?.arg).toEqual({ op, node: "offline-node" });
    await expect(card).toHaveAttribute("aria-busy", "false");
  }
  await h.app.evaluate(() => {
    (globalThis as unknown as { __xmeshUiTest: { answers: { cliFails: boolean } } }).__xmeshUiTest.answers.cliFails = true;
  });
  await card.getByTestId("kernel-op-install").click();
  await expect(card.getByTestId("kernel-operation-error")).toContainText("simulated kernel failure");
});
