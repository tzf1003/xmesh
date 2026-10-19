import { expect, test, type Locator, type Page } from "@playwright/test";
import * as fs from "node:fs";
import * as path from "node:path";
import type { Capability } from "../../src/shared/control";
import { launch, navigate, type Harness } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit", "gateway", "ci"];
let h: Harness;
test.afterEach(async () => { await h?.close(); });

async function capture(page: Page, name: string): Promise<void> {
  if (!process.env.XMESH_UI_QA_DIR) return;
  fs.mkdirSync(process.env.XMESH_UI_QA_DIR, { recursive: true });
  await page.screenshot({ path: path.join(process.env.XMESH_UI_QA_DIR, name), animations: "disabled" });
}

/** Compare rendered module edges, including cards separated by component wrappers. */
async function expectModuleGaps(root: Locator): Promise<void> {
  const gaps = await root.locator(".card").evaluateAll((elements) => {
    const cards = elements.filter((el) => el.getClientRects().length > 0 && !el.parentElement?.closest(".card"));
    return cards.slice(1).flatMap((el, i) => {
      const before = cards[i]!.getBoundingClientRect(), after = el.getBoundingClientRect();
      if (Math.abs(before.x - after.x) > 1 || Math.abs(before.width - after.width) > 1) return [];
      return [{ before: cards[i]!.textContent?.trim().slice(0, 30), after: el.textContent?.trim().slice(0, 30), gap: after.top - before.bottom }];
    });
  });
  for (const pair of gaps) expect(pair.gap, JSON.stringify(pair)).toBeGreaterThanOrEqual(14);
}

test("nested Tailnet modules have visible separation", async () => {
  h = await launch({ kernel: { capabilities: ALL } });
  const page = await h.openPanel();
  await navigate(page, "mesh");
  await page.getByTestId("mesh-tab-policy").click();
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/tagOwners/);
  await page.setViewportSize({ width: 1440, height: 1180 });
  await page.emulateMedia({ colorScheme: "light" });
  if (process.env.XMESH_UI_QA_DIR) {
    fs.mkdirSync(process.env.XMESH_UI_QA_DIR, { recursive: true });
    await page.screenshot({ path: path.join(process.env.XMESH_UI_QA_DIR, "policy.png") });
  }
  await expectModuleGaps(page.getByTestId("page-mesh"));
  await page.getByTestId("mesh-template-preview").click();
  await page.getByTestId("mesh-draft-preview").click();
  await expect(page.getByTestId("mesh-policy-plan")).toBeVisible();
  await expectModuleGaps(page.getByTestId("page-mesh"));
  await page.getByTestId("mesh-tab-derp").click();
  await expect(page.getByTestId("mesh-admission")).toBeVisible();
  await expect(page.getByTestId("mesh-policy")).not.toBeVisible();
  await expectModuleGaps(page.getByTestId("page-mesh"));
  await capture(page, "derp.png");
});

test("module separation, runtime navigation and role capsules work across themes and sizes", async () => {
  h = await launch({ kernel: { capabilities: ALL, status: { roles: ["agent", "gateway", "ingress"], network: { mode: "lan", node_id: "layout-node", tags: [] } } } });
  h.kernel.panel.setContent(JSON.stringify({ node: { name: "layout-node", roles: ["agent", "gateway", "ingress"] }, mesh: { mode: "lan" }, gateway: { listen: ":8080" }, agent: { default_cwd: "/tmp", cwd_policy: { allow: ["/tmp"] } } }));
  const page = await h.openPanel();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  await expect(page).toHaveTitle("xmesh");
  expect(page.url()).toMatch(/^app:\/\/panel/);
  await expect(page.getByTestId("nav-runtime")).toBeVisible();
  await page.getByTestId("topology-inspector").getByRole("button", { name: "管理接入地址与令牌" }).click();
  await expect(page.getByTestId("page-runtime")).toBeVisible();
  await expect(page.getByTestId("nav-runtime")).toHaveAttribute("aria-current", "page");
  await expect(page.getByTestId("reach-lan")).toBeVisible();
  await expect(page.locator("header").getByRole("switch")).toHaveCount(3);
  for (const [width, height, scheme] of [[1440, 1180, "light"], [1440, 1180, "dark"], [640, 800, "light"]] as const) {
    await page.setViewportSize({ width, height });
    await page.emulateMedia({ colorScheme: scheme });
    for (const id of ["runtime", "devices", "sessions", "authz", "ingress", "settings", "config", "audit", "ci", "about"]) {
      await navigate(page, id);
      const root = page.getByTestId(`page-${id}`);
      await expect(root).toBeVisible();
      await expect(root.locator(".card").first()).toBeVisible();
      await expectModuleGaps(root);
      await expect(page.locator("vite-error-overlay")).toHaveCount(0);
    }
    await navigate(page, "runtime");
    await capture(page, `runtime-${width}-${scheme}.png`);
    expect(await page.locator("header").evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(false);
    for (const role of ["agent", "gateway", "ingress"]) await expect(page.getByTestId(`role-${role}`)).toBeInViewport();
  }
  await page.getByTestId("open-display").click();
  await page.getByTestId("zoom-125").click();
  await page.keyboard.press("Escape");
  for (const role of ["agent", "gateway", "ingress"]) await expect(page.getByTestId(`role-${role}`)).toBeInViewport();
  expect(await page.locator("header").evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(false);
  await capture(page, "runtime-compact-125.png");
  expect(errors).toEqual([]);
});
