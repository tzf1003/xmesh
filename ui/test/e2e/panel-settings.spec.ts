// The settings page against the fake kernel: the ingress entry lands on it with the ingress role
// preselected, and a change to the working directories is reviewed before it is written.

import { type Page, expect, test } from "@playwright/test";
import type { Capability } from "../../src/shared/control";
import type { FakeKernelOptions } from "../../testserver/server";
import { type Harness, launch } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit"];
const FULL_CONFIG = [
  "# 测试配置",
  "node:",
  "  name: dev-1",
  "  roles: [agent, gateway]",
  "mesh:",
  "  mode: lan",
  "  lan:",
  "    mdns: true",
  "gateway:",
  '  listen: "127.0.0.1:8080"',
  "agent:",
  '  default_cwd: "/home/me/work"',
  "  cwd_policy:",
  '    allow: ["/home/me/work"]',
  '    deny: ["~/.ssh"]',
  "  authz:",
  '    "tag:peer":',
  '      tools: ["*"]',
  '      backends: ["*"]',
  "",
].join("\n");

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

async function open(kernel: FakeKernelOptions = {}): Promise<Page> {
  h = await launch({ kernel: { capabilities: ALL, ...kernel } });
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
  return page;
}

test("ingress: the disabled page's button opens settings with the ingress role preselected", async () => {
  const page = await open({ status: { roles: ["agent", "gateway"] } });
  await page.getByTestId("nav-ingress").click();
  await expect(page.getByTestId("ingress-disabled")).toBeVisible();
  await page.getByTestId("ingress-to-settings").click();

  await expect(page.getByTestId("page-settings")).toBeVisible();
  await expect(page.getByTestId("settings-role-ingress")).toBeChecked();
  await expect(page.getByTestId("settings-ingress")).toBeVisible();
  // Nothing is saved by the jump; the lan mode shows why ingress is not ready yet.
  await expect(page.getByTestId("settings-needs-tailnet")).toBeVisible();
  expect(h.kernel.panel.writes).toEqual([]);
});

test("settings: removing the last allowed directory is reviewed and saved as an empty list", async () => {
  const page = await open();
  h.kernel.panel.setContent(FULL_CONFIG);
  await page.getByTestId("nav-settings").click();

  await expect(page.getByTestId("settings-allow-0")).toHaveValue("/home/me/work");
  await page.getByTestId("settings-allow-remove-0").click();
  await expect(page.getByTestId("settings-allow-empty")).toBeVisible();
  await page.getByTestId("settings-review").click();
  await expect(page.getByTestId("settings-diff")).toBeVisible();
  expect(h.kernel.panel.writes).toEqual([]);

  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("settings-saved")).toBeVisible();
  expect(h.kernel.panel.writes.length).toBe(1);
  expect(h.kernel.panel.content).toContain("allow: []");
  expect(h.kernel.panel.content).toContain('deny: ["~/.ssh"]');
});

test("settings: without a deny list file the page says so and offers no save", async () => {
  const page = await open();
  h.kernel.panel.setContent(FULL_CONFIG);
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-deny-unset")).toBeVisible();
  await expect(page.getByTestId("deny-review")).toHaveCount(0);
});
