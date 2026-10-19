import { expect, test, type Page } from "@playwright/test";
import { launch, navigate, type Harness } from "./helpers";
import * as os from "node:os";
import * as path from "node:path";
let h: Harness;
test.afterEach(async () => { await h?.close(); });
async function basics(page: Page, purpose = "local"): Promise<void> {
  await page.getByTestId(`purpose-${purpose}`).click();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("wizard-name").fill("my-device");
  await page.getByTestId("wizard-workspace").fill("/home/me/work");
}
async function review(page: Page): Promise<void> {
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("setup-plan")).toBeVisible();
  await expect(page.getByTestId("wizard-run")).toBeEnabled();
}
test("first launch opens setup automatically when no device is configured", async () => {
  h = await launch({ publish: false, lang: "en" });
  await expect.poll(() => h.app.windows().length).toBe(1);
  const page = h.app.windows()[0]!;
  await expect(page.getByTestId("wizard-purpose")).toBeVisible();
  await expect.poll(() => h.app.evaluate(() => (globalThis as unknown as { __xmeshUiTest: { panelState(): { visible: boolean } } }).__xmeshUiTest.panelState().visible)).toBe(true);
});
for (const purpose of ["local", "multi-device", "cloud"]) test(`purpose ${purpose}: review shows intended setup and collapsed YAML`, async () => {
  h = await launch({ publish: false, lang: "en" });
  const page = await h.openPanel();
  const errors: string[] = []; page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
  await expect(page.locator("vite-error-overlay")).toHaveCount(0);
  await expect(page).toHaveTitle("xmesh");
  expect(page.url()).toMatch(/^app:\/\/panel\//);
  if (purpose === "local") await page.screenshot({ path: path.join(os.tmpdir(), "xmesh-onboarding-purpose.png") });
  await basics(page, purpose);
  await expect(page.getByTestId("permission-development")).toBeChecked();
  if (purpose !== "local") await expect(page.getByTestId("wizard-control-url")).toHaveValue("https://controlplane.tailscale.com");
  await review(page);
  await expect(page.getByTestId("client-codex")).toBeChecked();
  await expect(page.getByTestId("wizard-preview")).toBeHidden();
  await page.locator(".quick-yaml summary").click();
  await expect(page.getByTestId("wizard-preview")).toContainText(`mode: ${purpose === "local" ? "lan" : "tailnet"}`);
  await page.screenshot({ path: path.join(os.tmpdir(), `xmesh-onboarding-${purpose}.png`) });
  expect(errors).toEqual([]);
});
test("device readiness and actual client verification are separate, and feature links work", async () => {
  h = await launch({ lang: "en" });
  const page = await h.openPanel();
  await navigate(page, "setup");
  await basics(page); await review(page); await page.getByTestId("wizard-run").click();
  await expect(page.getByTestId("setup-client-pending")).toBeVisible();
  await expect(page.getByTestId("setup-readiness")).toContainText("Device ready");
  await expect(page.getByTestId("setup-readiness")).toContainText("Client verification pending");
  await expect(page.getByTestId("setup-verification-prompt")).toHaveValue(/local\/fs.stat/);
  await page.getByTestId("setup-copy-prompt").click();
  await expect(page.getByTestId("toast")).toContainText("Verification prompt copied");
  await page.getByTestId("setup-verify").click();
  await expect(page.getByTestId("setup-readiness")).toContainText("Client tool call verified");
  await page.getByTestId("wizard-finish").click();
  await expect(page.getByTestId("page-features")).toBeVisible();
  await expect(page.locator(".feature-card")).toHaveCount(6);
  await page.screenshot({ path: path.join(os.tmpdir(), "xmesh-feature-center.png") });
  await page.getByTestId("feature-devices").click();
  await expect(page.getByTestId("page-devices")).toBeVisible();
});
test("browser wait persists through page navigation and authorization needs explicit action", async () => {
  h = await launch({ lang: "en" });
  const page = await h.openPanel(); await navigate(page, "setup");
  await h.app.evaluate(() => { (globalThis as unknown as { __xmeshUiTest: { answers: { setupWaiting: boolean } } }).__xmeshUiTest.answers.setupWaiting = true; });
  await basics(page, "cloud"); await review(page); await page.getByTestId("wizard-run").click();
  await expect(page.getByTestId("setup-open-auth")).toBeVisible();
  expect((await h.calls()).filter((call) => call.op === "openExternal")).toHaveLength(0);
  await page.getByTestId("setup-open-auth").click();
  await expect.poll(async () => (await h.calls()).filter((call) => call.op === "openExternal").length).toBe(1);
  await page.reload();
  await expect(page.getByTestId("page-overview")).toBeVisible();
  await expect(page.getByTestId("setup-reminder")).toBeVisible();
  await navigate(page, "setup");
  await expect(page.getByTestId("setup-existing")).toBeVisible();
  await page.getByTestId(/^resume-job-/).click();
  await expect(page.getByTestId("setup-open-auth")).toBeVisible();
  await page.screenshot({ path: path.join(os.tmpdir(), "xmesh-onboarding-waiting.png") });
});
test("failed task can retry and deferred clients remain pending", async () => {
  h = await launch({ publish: false, lang: "en" }); const page = await h.openPanel();
  await basics(page); await review(page);
  await page.getByTestId("clients-defer").check(); await expect(page.getByTestId("wizard-run")).toBeEnabled();
  await h.app.evaluate(() => { (globalThis as unknown as { __xmeshUiTest: { answers: { cliFails: boolean } } }).__xmeshUiTest.answers.cliFails = true; });
  await page.getByTestId("wizard-run").click(); await expect(page.getByTestId("setup-retry")).toBeVisible();
  await h.app.evaluate(() => { (globalThis as unknown as { __xmeshUiTest: { answers: { cliFails: boolean } } }).__xmeshUiTest.answers.cliFails = false; });
  await page.getByTestId("setup-retry").click();
  await expect(page.getByTestId("setup-readiness")).toContainText("Device ready");
  await expect(page.getByTestId("setup-readiness")).toContainText("Client verification pending");
  await page.setViewportSize({ width: 740, height: 800 });
  await page.screenshot({ path: path.join(os.tmpdir(), "xmesh-onboarding-compact.png") });
});

test("cloud login password clears after save and reaches client evidence step", async () => {
  h = await launch({ publish: false, lang: "en" }); const page = await h.openPanel();
  await basics(page, "cloud"); await review(page); await page.getByTestId("wizard-run").click();
  await expect(page.getByTestId("setup-ingress-password")).toBeVisible();
  await page.getByTestId("setup-password").fill("sample-long-password-123");
  await page.getByTestId("setup-password-confirm").fill("sample-long-password-123");
  await page.getByTestId("setup-save-password").click();
  await expect(page.getByTestId("setup-ingress-password")).toHaveCount(0);
  await expect(page.getByTestId("setup-client-pending")).toBeVisible();
  const call = (await h.calls()).find((entry) => entry.op === "onboarding" && (entry.arg as { op?: string }).op === "set_password");
  expect((call?.arg as { password: string }).password).toBe("[redacted]");
  expect(await page.content()).not.toContain("sample-long-password-123");
});
test("existing client entry requires replacement acknowledgement for this preview", async () => {
  h = await launch({ publish: false, lang: "en" }); const page = await h.openPanel();
  await h.app.evaluate(() => { (globalThis as unknown as { __xmeshUiTest: { answers: { setupConflict: boolean } } }).__xmeshUiTest.answers.setupConflict = true; });
  await basics(page); await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("setup-plan")).toBeVisible();
  await expect(page.getByTestId("wizard-run")).toBeDisabled();
  await page.getByTestId("setup-replace-client").check();
  await expect(page.getByTestId("wizard-run")).toBeEnabled();
  await page.getByTestId("client-claude-code").check();
  await expect(page.getByTestId("setup-replace-client")).not.toBeChecked();
  await expect(page.getByTestId("wizard-run")).toBeDisabled();
});

test("multi-device readiness links to device pairing while client verification stays pending", async () => {
  h = await launch({ lang: "en" }); const page = await h.openPanel(); await navigate(page, "setup");
  await basics(page, "multi-device"); await review(page); await page.getByTestId("wizard-run").click();
  await expect(page.getByTestId("setup-pair-device")).toBeVisible();
  await expect(page.getByTestId("setup-readiness")).toContainText("Client verification pending");
  await page.getByTestId("setup-pair-device").click();
  await expect(page.getByTestId("page-devices")).toBeVisible();
  await expect(page.getByTestId("setup-reminder")).toBeVisible();
});

test("feature statuses follow live peers, backend health and runtime warnings", async () => {
  h = await launch({ lang: "en", kernel: { peers: [], status: { backends: [], network: { mode: "lan", node_id: "self", tags: [] } } } });
  const page = await h.openPanel(); await navigate(page, "features");
  await expect(page.getByTestId("feature-devices")).toHaveAttribute("data-state", "unconfigured");
  await expect(page.getByTestId("feature-clients")).toHaveAttribute("data-state", "unconfigured");
  await expect(page.getByTestId("feature-mesh")).toHaveAttribute("data-state", "unconfigured");
  h.kernel.panel.setContent(JSON.stringify({ node: { name: "win-dev-01", roles: ["agent", "gateway"] }, agent: { backends: { mcp: { transport: "stdio", command: ["test-mcp"], policy: "shared" } } } }));
  h.kernel.setStatus({ network: { mode: "tailnet", node_id: "self", tags: [] } });
  await expect(page.getByTestId("feature-clients")).toHaveAttribute("data-state", "unconfigured");
  await expect(page.getByTestId("feature-mesh")).toHaveAttribute("data-state", "ready");
  h.kernel.setStatus({ warnings: [{ code: "tailnet_not_ready", severity: "warning", message: "network waiting" }, { code: "policy_reload_failed", severity: "error", message: "policy waiting" }] });
  await expect(page.getByTestId("feature-permissions")).toHaveAttribute("data-state", "attention");
  await expect(page.getByTestId("feature-mesh")).toContainText("Needs attention");
  await page.screenshot({ path: path.join(os.tmpdir(), "xmesh-feature-states.png") });
});
