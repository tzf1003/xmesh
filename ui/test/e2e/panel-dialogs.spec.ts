import { type Locator, expect, test } from "@playwright/test";
import type { Capability } from "../../src/shared/control";
import { launch, navigate, openModuleSettings, type Harness } from "./helpers";

const CAPS: Capability[] = ["status", "events", "peers", "pair", "sessions", "config.read", "config.write", "gateway", "ingress", "ci"];
let h: Harness;
test.afterEach(async () => { await h?.close(); });

async function closeDrawer(drawer: Locator): Promise<void> {
  await expect(drawer).toBeVisible();
  // CDP clicks bypass native window hit testing, so also assert the drag exclusion.
  await expect(drawer.locator("..")).toHaveCSS("-webkit-app-region", "no-drag");
  await drawer.getByRole("button", { name: "Close", exact: true }).click();
  await expect(drawer).toHaveCount(0);
}

for (const width of [390, 1320]) {
  test(`device drawer close buttons work over the titlebar at ${width}px`, async ({}, info) => {
    h = await launch({ kernel: { capabilities: CAPS } });
    const page = await h.openPanel();
    const errors: string[] = [];
    page.on("pageerror", error => errors.push(error.message));
    page.on("console", message => { if (["error", "warning"].includes(message.type())) errors.push(message.text()); });
    await page.setViewportSize({ width, height: 860 });
    await expect(page).toHaveTitle("xmesh");
    await expect(page).toHaveURL(new RegExp("^app://panel/"));
    await navigate(page, "devices");
    await page.getByTestId("peer-mac-dev-01").click();
    await page.screenshot({ path: info.outputPath(`device-drawer-${width}.png`) });
    const close = await page.getByTestId("device-details").getByRole("button", { name: "Close", exact: true }).boundingBox();
    const bar = await page.locator(".topbar").boundingBox();
    expect(close!.y).toBeLessThan(bar!.height);
    await closeDrawer(page.getByTestId("device-details"));
    await expect(page.getByTestId("peer-mac-dev-01")).toBeFocused();
    for (const [trigger, dialog] of [
      ["device-add", "device-panel-add"],
      ["device-password", "device-panel-password"],
      ["device-method-manual", "device-panel-manual"],
      ["device-method-documents", "device-panel-documents"],
      ["pair-open-win-lan", "dialog"],
    ]) {
      await page.getByTestId(trigger!).click();
      await closeDrawer(page.getByTestId(dialog!));
    }
    await expect(page.locator(".topbar")).toHaveCSS("-webkit-app-region", "drag");
    expect(errors).toEqual([]);
  });
}

test("module drawer close buttons preserve the unsaved changes confirmation", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  h.kernel.panel.setContent(JSON.stringify({ node: { name: "dialog-test", roles: ["agent", "gateway", "ingress"] }, mesh: { mode: "lan" }, gateway: { listen: "127.0.0.1:8080" } }));
  const page = await h.openPanel();
  for (const scope of ["node", "agent", "gateway", "ingress", "transfers"] as const) {
    await openModuleSettings(page, scope);
    await closeDrawer(page.getByTestId("module-settings-dialog"));
  }
  await openModuleSettings(page, "gateway");
  const drawer = page.getByTestId("module-settings-dialog");
  const listen = page.getByTestId("settings-gateway-listen");
  await listen.fill("127.0.0.1:9090");
  await drawer.getByRole("button", { name: "Close", exact: true }).click();
  await expect(page.getByTestId("confirm")).toBeVisible();
  await page.getByTestId("confirm-cancel").click();
  await expect(listen).toHaveValue("127.0.0.1:9090");
  await drawer.getByRole("button", { name: "Close", exact: true }).click();
  await page.getByTestId("confirm-ok").click();
  await expect(drawer).toHaveCount(0);
  expect(h.kernel.panel.writes).toEqual([]);
});

test("CI drawer close buttons dismiss only the active drawer", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  h.kernel.panel.ci.bindings = [{
    id: "example", scope: "repo", target: "owner/example", source: "token", concurrency: 1, labels: [], paused: false, removing: false, dir: "/tmp/example",
    instances: [{ index: 1, name: "example-1", state: "broken", restarts: 0, since: new Date().toISOString(), problem: { code: "runner_removed", message: "Runner removed" } }],
  }];
  const page = await h.openPanel();
  await navigate(page, "ci");
  await page.getByTestId("ci-add").click();
  await page.getByTestId("ci-way-account").click();
  await page.getByTestId("ci-add-account-inline").click();
  await closeDrawer(page.getByTestId("ci-account-dialog"));
  await expect(page.getByTestId("ci-add-dialog")).toBeVisible();
  await closeDrawer(page.getByTestId("ci-add-dialog"));
  await page.getByTestId("ci-manage-example").click();
  await page.getByTestId("ci-edit-example").click();
  await closeDrawer(page.getByTestId("ci-edit-dialog"));
  await page.getByTestId("ci-step-example-1").click();
  await closeDrawer(page.getByTestId("ci-repair-dialog"));
});

test("Tailscale drawer close buttons return to device management", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  const page = await h.openPanel();
  await navigate(page, "mesh");
  for (const [trigger, dialog] of [
    ["mesh-manage-credentials", "mesh-credentials-drawer"],
    ["mesh-tab-onboard", "mesh-onboard-drawer"],
    ["mesh-edit-tags-n100", "mesh-tags-dialog"],
  ]) {
    await page.getByTestId(trigger!).click();
    await closeDrawer(page.getByTestId(dialog!));
  }
  await expect(page.getByTestId("mesh-devices")).toBeVisible();
});

test("execution detail drawer closes on compact displays", async () => {
  h = await launch({ kernel: { capabilities: CAPS } });
  const now = new Date().toISOString();
  h.kernel.panel.sessions.executions = [{ id: "close-test", tool: "fs.read", device: "node-a", caller: "gateway/a", layer: "agent", state: "running", started_at: now, updated_at: now }];
  const page = await h.openPanel();
  await page.setViewportSize({ width: 640, height: 860 });
  await navigate(page, "sessions");
  await page.getByTestId("execution-row").click();
  await closeDrawer(page.getByTestId("execution-details-dialog"));
  await expect(page.getByTestId("execution-row")).toBeFocused();
});
