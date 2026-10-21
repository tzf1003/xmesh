// The tailnet admin pages against an in-process stand-in for `xmesh mesh --json`
// (src/main/mesh-fake.ts), through the real Electron app and IPC.

import { type Page, expect, test } from "@playwright/test";
import type { Capability } from "../../src/shared/control";
import type { FakeMeshState } from "../../src/main/mesh-fake";
import * as path from "node:path";
import { type Harness, launch, navigate } from "./helpers";
import { FakeKernel } from "../../testserver/server";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit"];
const cli = process.platform === "win32" ? "& 'xmesh'" : "xmesh";

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

async function open(opts: { publish?: boolean } = {}): Promise<Page> {
  h = await launch({ kernel: { capabilities: ALL }, ...opts });
  const page = await h.openPanel();
  await navigate(page, "mesh");
  await expect(page.getByTestId("page-mesh")).toBeVisible();
  return page;
}

type Patch = Partial<Pick<FakeMeshState, "credential" | "writeCredential" | "acceptKeys" | "provider" | "version" | "delayMs" | "policyText" | "devices">>;
const patch = (p: Patch): Promise<void> =>
  h.app.evaluate((_e, v) => {
    Object.assign((globalThis as unknown as { __xmeshUiTest: { mesh: object } }).__xmeshUiTest.mesh, v);
  }, p);
const fake = <K extends keyof FakeMeshState>(k: K): Promise<FakeMeshState[K]> =>
  h.app.evaluate((_e, key) => (globalThis as unknown as { __xmeshUiTest: { mesh: Record<string, unknown> } }).__xmeshUiTest.mesh[key], k) as Promise<FakeMeshState[K]>;
const picked = (path: string): Promise<void> =>
  h.app.evaluate((_e, p) => {
    (globalThis as unknown as { __xmeshUiTest: { answers: { picked?: string } } }).__xmeshUiTest.answers.picked = p;
  }, path);

test("devices: roles, issues and tags show, and the page works without a running kernel", async () => {
  const page = await open({ publish: false });
  await expect(page.getByTestId("mesh-devices").getByRole("columnheader", { name: "网络标签", exact: true })).toBeVisible();
  await expect(page.getByTestId("mesh-network-tags-scope")).toContainText("连接和配对无需分配网络标签");
  await expect(page.getByTestId("mesh-provider")).toContainText("tailscale");
  await expect(page.getByTestId("mesh-devices")).toContainText("laptop");
  await expect(page.getByTestId("mesh-device-n100")).toContainText("agent, gateway");
  await expect(page.getByTestId("mesh-device-n200")).toContainText("tag:peer");
  await expect(page.getByTestId("mesh-device-n300")).toContainText("设备没有 tag");
  await expect(page.getByTestId("mesh-device-n300")).toContainText("离线");
  // Without a connected node no node config is passed to the CLI; the
  // credential file is always named.
  const calls = await fake("calls");
  expect(calls[0]!.slice(0, 3)).toEqual(["mesh", "status", "--json"]);
  expect(calls[0]!.at(-1)!.startsWith("--credential-file=")).toBe(true);
  expect(calls[0]!.some((a: string) => a.startsWith("--node-config="))).toBe(false);
});

test("devices: optional network tags do not change pairing permission groups", async () => {
  const page = await open();
  const requests = h.kernel.requests.length;
  await patch({ devices: [{ id: "n100", name: "untagged-v2", tags: [], online: true, roles: ["agent", "gateway"], issues: [] }] });
  await page.getByTestId("mesh-refresh").click();
  const row = page.getByTestId("mesh-device-n100");
  await expect(row).toContainText("未设置（可选）");
  await expect(row).toContainText("agent, gateway");
  await expect(page.getByTestId("mesh-attention")).toHaveCount(0);
  await page.getByTestId("mesh-edit-tags-n100").click();
  await expect(page.getByRole("dialog")).toContainText("网络标签");
  await page.getByTestId("mesh-tags-input").fill("tag:device");
  await page.getByTestId("mesh-tags-preview").click();
  await page.getByTestId("mesh-tags-apply").click();
  await expect(row).toContainText("tag:device");
  expect(h.kernel.requests.slice(requests).some(r => r.path.startsWith("/v2/pair"))).toBe(false);
});

test("devices: a connected node's config is handed to the CLI so roles can be matched", async () => {
  const page = await open();
  await expect(page.getByTestId("mesh-devices")).toBeVisible();
  const first = (await fake("calls"))[0]!;
  expect(first.slice(0, 3)).toEqual(["mesh", "status", "--json"]);
  expect(first.some((a: string) => a.startsWith("--node-config="))).toBe(true);
  expect(first.some((a: string) => a.startsWith("--credential-file="))).toBe(true);
});

test("devices: search and status filters combine and onboarding opens a drawer", async () => {
  const page = await open();
  await page.getByTestId("mesh-search").fill("tag:peer");
  await expect(page.getByTestId("mesh-device-n200")).toBeVisible();
  await expect(page.getByTestId("mesh-device-n100")).toHaveCount(0);
  await page.getByTestId("mesh-device-filter").selectOption("offline");
  await expect(page.getByTestId("mesh-device-n200")).toHaveCount(0);
  await page.getByTestId("mesh-search").fill("");
  await expect(page.getByTestId("mesh-device-n300")).toBeVisible();
  await page.getByTestId("mesh-tab-onboard").click();
  await expect(page.getByTestId("mesh-onboard-drawer")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("mesh-onboard-drawer")).toHaveCount(0);
  await expect(page.getByTestId("mesh-device-filter")).toHaveValue("offline");
});

test("devices: warning review toggles filters and long diagnostics fit the table", async () => {
  const page = await open();
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
  await patch({ devices: [
    { id: "n100", name: "healthy", tags: ["tag:device"], online: true, roles: ["agent"], issues: [] },
    { id: "n200", name: "xiaosong-mac.tailb10244.ts.net", tags: ["tag:mobile"], online: true, roles: [], issues: [
      { severity: "warning", code: "no-tags", message: "Review device tags" },
      { severity: "error", code: "missing-role-tag", message: 'xmesh.yaml declares role "gateway" but the node lacks tag:peer or tag:cloud (it will not appear in discovery or will be denied)' },
      { severity: "info", code: "future", message: "long-diagnostic-".repeat(40) },
    ] },
    { id: "n300", name: "warning-only", tags: [], online: false, roles: [], issues: [{ severity: "warning", code: "no-tags", message: "Review tags" }] },
  ] });
  await page.getByTestId("mesh-refresh").click();
  const banner = page.getByTestId("mesh-attention");
  await expect(banner).toContainText("2 台设备");
  await page.getByTestId("mesh-search").fill("healthy");
  await banner.getByRole("button", { name: "查看待处理设备" }).click();
  await expect(page.getByTestId("mesh-search")).toHaveValue("");
  await expect(page.getByTestId("mesh-device-filter")).toHaveValue("attention");
  await expect(page.getByTestId("mesh-device-n100")).toHaveCount(0);
  await expect(page.getByTestId("mesh-device-n300")).toBeVisible();
  const report = page.getByTestId("mesh-device-n200");
  await expect(report).toContainText("配置已启用 gateway 角色");
  await expect(report.locator(".badge.warn")).toHaveText("警告");
  await expect(report.locator(".module-device-roles")).toHaveText("—");
  await expect(report.locator(".module-state")).toHaveText("在线");
  expect(page.url()).toMatch(/^app:\/\/panel\//);
  expect(await page.title()).toBeTruthy();
  await expect(page.getByTestId("page-mesh")).toBeVisible();
  for (const width of [1440, 1100, 900, 700]) {
    await page.setViewportSize({ width, height: 1000 });
    const bounds = await page.evaluate(() => {
      const alert = document.querySelector('[data-testid="mesh-attention"]')!;
      const button = alert.querySelector("button")!.getBoundingClientRect();
      const box = alert.getBoundingClientRect();
      const table = document.querySelector('[data-testid="mesh-devices"]') as HTMLElement;
      const status = table.querySelector(".module-state")!.getBoundingClientRect();
      return { buttonFits: button.left >= box.left && button.right <= box.right && button.bottom <= box.bottom, tableFits: table.scrollWidth <= table.clientWidth + 1, statusHeight: status.height, detailsOverflow: [...table.querySelectorAll(".module-device-issue > span:last-child")].some((el) => el.scrollWidth > el.clientWidth + 1) };
    });
    expect(bounds.buttonFits, `banner button at ${width}px`).toBe(true);
    expect(bounds.tableFits, `table at ${width}px`).toBe(true);
    expect(bounds.statusHeight).toBeLessThan(25);
    expect(bounds.detailsOverflow).toBe(false);
    if (process.env.XMESH_UI_QA_DIR) await page.screenshot({ path: path.join(process.env.XMESH_UI_QA_DIR, `mesh-review-${width}.png`), fullPage: true });
  }
  await banner.getByRole("button", { name: "全部设备" }).click();
  await expect(page.getByTestId("mesh-device-filter")).toHaveValue("all");
  await expect(page.getByTestId("mesh-device-n100")).toBeVisible();
  expect(errors).toEqual([]);
});

test("no credential: the page shows the login commands, copies them, and recovers after login", async () => {
  await (async () => {
    h = await launch({ kernel: { capabilities: ALL } });
  })();
  await patch({ credential: false });
  const page = await h.openPanel();
  await navigate(page, "mesh");
  await expect(page.getByTestId("mesh-no-credential")).toBeVisible();
  await expect(page.getByTestId("mesh-no-credential")).toContainText(`${cli} mesh login`);
  await page.getByTestId("mesh-copy-write").click();
  await expect.poll(async () => (await h.calls()).filter((c) => c.op === "clipboard").map((c) => c.arg)).toEqual([`${cli} mesh login --kind tailscale --access write --panel`]);
  await page.getByTestId("mesh-copy-read-headscale").click();
  await expect.poll(async () => (await h.calls()).filter((c) => c.op === "clipboard").map((c) => c.arg)).toEqual([
    `${cli} mesh login --kind tailscale --access write --panel`,
    `${cli} mesh login --kind headscale --url https://headscale.example.com --access read --panel`,
  ]);
  await expect(page.getByTestId("toast").first()).toContainText("已复制");

  await patch({ credential: true });
  await page.getByTestId("mesh-refresh").click();
  await expect(page.getByTestId("mesh-devices")).toBeVisible();
  await expect(page.getByTestId("mesh-no-credential")).toHaveCount(0);
});

test("login form: the key goes to the CLI on stdin, the field is cleared, and the devices load", async () => {
  const page = await open();
  await patch({ credential: false });
  await page.getByTestId("mesh-refresh").click();
  await page.getByTestId("mesh-connect-credentials").or(page.getByTestId("mesh-manage-credentials")).click();
  await expect(page.getByTestId("mesh-credentials-drawer")).toBeVisible();
  await expect(page.getByTestId("mesh-login-submit")).toBeDisabled();
  const key = "tskey-api-e2e-SECRET-42";
  await page.getByTestId("mesh-login-secret").fill(key);
  await page.getByTestId("mesh-login-submit").click();
  await expect(page.getByTestId("mesh-devices")).toContainText("laptop");
  await expect(page.getByTestId("mesh-no-credential")).toHaveCount(0);
  await expect(page.getByTestId("mesh-credentials-drawer")).toHaveCount(0);
  await expect(page.getByTestId("mesh-login-secret")).toHaveCount(0);
  expect(await page.content()).not.toContain(key);
  const calls = await fake("calls");
  const login = calls.find((c) => c[1] === "login")!;
  expect(login.slice(0, 5)).toEqual(["mesh", "login", "--json", "--kind=tailscale", "--access=write"]);
  expect(login).toContain("--secret-file=-");
  expect(login.some((a: string) => a.startsWith("--credential-file="))).toBe(true);
  expect(JSON.stringify(calls)).not.toContain(key);
});

test("login form: a rejected key is reported and cleared; a Headscale address must be https", async () => {
  const page = await open();
  await patch({ credential: false, acceptKeys: false });
  await page.getByTestId("mesh-refresh").click();
  await page.getByTestId("mesh-connect-credentials").or(page.getByTestId("mesh-manage-credentials")).click();
  await page.getByTestId("mesh-login-kind").selectOption("headscale");
  await page.getByTestId("mesh-login-url").fill("http://headscale.example.com");
  await page.getByTestId("mesh-login-secret").fill("hskey-e2e");
  await expect(page.getByTestId("mesh-login-submit")).toBeDisabled();
  await page.getByTestId("mesh-login-url").fill("https://headscale.example.com");
  await expect(page.getByTestId("mesh-login-submit")).toBeEnabled();
  await page.getByTestId("mesh-login-submit").click();
  await expect(page.getByTestId("mesh-error")).toContainText("凭据验证失败");
  await expect(page.getByTestId("mesh-login-secret")).toHaveValue("");
  await expect(page.getByTestId("mesh-login-submit")).toBeDisabled();
  expect(await page.content()).not.toContain("hskey-e2e");
  expect(await fake("credential")).toBe(false);
});

test("read-only credential: reading works and writing explains the missing write login", async () => {
  const page = await open();
  await expect(page.getByTestId("mesh-devices")).toBeVisible();
  await patch({ writeCredential: false });
  await page.getByTestId("mesh-edit-tags-n300").click();
  await page.getByTestId("mesh-tags-input").fill("tag:device");
  await page.getByTestId("mesh-tags-preview").click();
  await expect(page.getByTestId("mesh-tags-plan")).toBeVisible();
  await page.getByTestId("mesh-tags-apply").click();
  await expect(page.getByTestId("mesh-error")).toContainText("权限");
  expect(await fake("devices")).toMatchObject([{}, {}, { tags: [] }]);
});

test("tags: preview first, tags without an owner are blocked, apply writes", async () => {
  const page = await open();
  await page.getByTestId("mesh-edit-tags-n300").click();
  const input = page.getByTestId("mesh-tags-input");
  await expect(page.getByTestId("mesh-tags-apply")).toBeDisabled();

  await input.fill("not a tag");
  await expect(page.getByTestId("mesh-tags-invalid")).toBeVisible();
  await expect(page.getByTestId("mesh-tags-preview")).toBeDisabled();

  await input.fill("tag:ghost");
  await page.getByTestId("mesh-tags-preview").click();
  await expect(page.getByTestId("mesh-tags-missing")).toContainText("tag:ghost");
  await expect(page.getByTestId("mesh-tags-apply")).toBeDisabled();

  await input.fill("tag:device");
  // A changed text invalidates the previous preview.
  await expect(page.getByTestId("mesh-tags-plan")).toHaveCount(0);
  await expect(page.getByTestId("mesh-tags-apply")).toBeDisabled();
  await page.getByTestId("mesh-tags-preview").click();
  await expect(page.getByTestId("mesh-tags-plan")).toContainText("tag:device");
  expect((await fake("devices"))[2]!.tags).toEqual([]);
  await page.getByTestId("mesh-tags-apply").click();
  await expect(page.getByTestId("mesh-tags-dialog")).toHaveCount(0);
  await expect(page.getByTestId("mesh-device-n300")).toContainText("tag:device");
  expect((await fake("devices"))[2]!.tags).toEqual(["tag:device"]);
  const calls = await fake("calls");
  expect(calls.some((c) => c.includes("--apply") && c.includes("--device=n300") && c.includes("--set=tag:device"))).toBe(true);
});

test("onboard: the key goes to the chosen file; the page only ever learns the path", async () => {
  const page = await open();
  await page.getByTestId("mesh-tab-onboard").click();
  await expect(page.getByTestId("mesh-onboard-tags")).toHaveValue("");
  await expect(page.getByTestId("mesh-onboard-create")).toBeDisabled();
  await picked("/home/me/keys/new-device.key");
  await page.getByTestId("mesh-onboard-pick").click();
  await expect(page.getByTestId("mesh-onboard-file")).toHaveValue("/home/me/keys/new-device.key");
  await page.getByTestId("mesh-onboard-ttl").selectOption("6h");
  await page.getByTestId("mesh-onboard-ephemeral").check();
  await page.getByTestId("mesh-onboard-create").click();
  await expect(page.getByTestId("mesh-onboard-done")).toBeVisible();
  await expect(page.getByTestId("mesh-onboard-path")).toHaveText("/home/me/keys/new-device.key");
  expect(await fake("keyFiles")).toEqual(["/home/me/keys/new-device.key"]);
  const call = (await fake("calls")).find((c) => c[1] === "authkey")!;
  expect(call.slice(0, 4)).toEqual(["mesh", "authkey", "--json", "--credential-file=" + call[3]!.slice("--credential-file=".length)]);
  expect(call.slice(4)).toEqual(["--tags=", "--ttl=6h", "--out=/home/me/keys/new-device.key", "--ephemeral", "--overwrite"]);
  expect(await page.getByTestId("page-mesh").innerText()).not.toMatch(/tskey|secret/i);
  // A cancelled chooser changes nothing.
  await picked("");
  await page.getByTestId("mesh-onboard-pick").click();
  await expect(page.getByTestId("mesh-onboard-file")).toHaveValue("/home/me/keys/new-device.key");
});

test("policy: template preview, confirmed write with the previewed version, snapshot and rollback", async () => {
  const page = await open();
  await page.getByTestId("mesh-tab-policy").click();
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/tagOwners/);

  await page.getByTestId("mesh-template").selectOption("team");
  await page.getByTestId("mesh-template-preview").click();
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/xmesh:managed/);
  await expect(page.getByTestId("mesh-policy-plan")).toHaveCount(0);
  await page.getByTestId("mesh-draft-preview").click();
  await expect(page.getByTestId("mesh-policy-plan")).toBeVisible();
  await expect(page.getByTestId("mesh-diff")).toContainText("tagOwners");
  expect(await fake("version")).toBe(1);

  await page.getByTestId("mesh-plan-cancel").click();
  expect(await fake("version")).toBe(1);
  await page.getByTestId("mesh-draft-preview").click();
  await page.getByTestId("mesh-plan-apply").click();
  await expect(page.getByTestId("mesh-snapshot-snap-1")).toBeVisible();
  await expect(page.getByTestId("mesh-policy-plan")).toHaveCount(0);
  expect(await fake("version")).toBe(2);
  const applied = (await fake("calls")).find((c) => c.includes("--apply"))!;
  expect(applied).toContain("--expect-version=v1");
  expect(applied.slice(0, 3)).toEqual(["mesh", "policy", "publish"]);

  await page.getByTestId("mesh-rollback-snap-1").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/"acls": \[\]/);
  expect(await fake("version")).toBe(2);
  await page.getByTestId("mesh-draft-preview").click();
  await expect(page.getByTestId("mesh-policy-plan")).toBeVisible();
  await page.getByTestId("mesh-plan-apply").click();
  await expect(page.getByTestId("mesh-policy-plan")).toHaveCount(0);
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/"acls": \[\]/);
  expect(await fake("version")).toBe(3);
});

test("policy: applying previews the draft, and saving writes the reviewed policy", async () => {
  const page = await open();
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
  await page.getByTestId("mesh-tab-policy").click();
  const workspace = page.getByTestId("mesh-policy-workspace");
  const applyDraft = workspace.getByRole("button", { name: "应用到云端", exact: true });
  const refresh = workspace.getByTestId("mesh-policy-cloud-refresh");
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/tagOwners/);
  await expect(applyDraft).toBeDisabled();
  const original = await page.getByTestId("mesh-policy-text").inputValue();
  await page.getByTestId("mesh-policy-text").fill(original.replace('"acls": []', '"acls": [], "note": "save-draft"'));
  await expect(workspace).toContainText("草稿有待应用的修改");
  await expect(applyDraft).toBeEnabled();
  for (const width of [1440, 900, 700]) {
    await page.setViewportSize({ width, height: 900 });
    await workspace.scrollIntoViewIfNeeded();
    await expect(applyDraft).toBeInViewport({ ratio: 1 });
    await expect(refresh).toBeInViewport({ ratio: 1 });
    const refreshBounds = (await refresh.boundingBox())!;
    const applyBounds = (await applyDraft.boundingBox())!;
    expect(Math.abs(refreshBounds.y - applyBounds.y)).toBeLessThan(1);
    expect(applyBounds.x - refreshBounds.x - refreshBounds.width).toBeLessThanOrEqual(9);
    expect(applyBounds.x).toBeGreaterThan(refreshBounds.x);
    expect(await workspace.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    if (process.env.XMESH_UI_QA_DIR) await page.screenshot({ path: path.join(process.env.XMESH_UI_QA_DIR, `mesh-draft-save-${width}.png`) });
  }
  expect(page.url()).toMatch(/^app:\/\/panel\//);
  expect(await page.title()).toBeTruthy();
  await expect(page.getByTestId("page-mesh")).toBeVisible();
  await refresh.click();
  await page.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/save-draft/);
  await applyDraft.click();
  await expect(page.getByTestId("mesh-policy-plan").getByRole("button", { name: "保存", exact: true })).toBeEnabled();
  await expect(page.getByTestId("mesh-diff")).toContainText("save-draft");
  const preview = page.getByRole("dialog", { name: "预览完整策略差异" });
  await expect(preview).toHaveAttribute("aria-modal", "true");
  await expect(preview.getByTestId("mesh-plan-cancel")).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(preview.getByTestId("mesh-plan-apply")).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(preview.getByTestId("mesh-plan-cancel")).toBeFocused();
  for (const width of [1440, 900, 700]) {
    await page.setViewportSize({ width, height: 800 });
    await expect(preview).toBeInViewport({ ratio: 1 });
    await expect(preview.getByTestId("mesh-plan-apply")).toBeInViewport({ ratio: 1 });
    expect(await preview.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    if (process.env.XMESH_UI_QA_DIR) await page.screenshot({ path: path.join(process.env.XMESH_UI_QA_DIR, `mesh-draft-preview-${width}.png`) });
  }
  expect(await fake("version")).toBe(1);
  await page.keyboard.press("Escape");
  await expect(preview).toHaveCount(0);
  await expect(applyDraft).toBeFocused();
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/save-draft/);
  const reviewLines = JSON.stringify(Array.from({ length: 100 }, (_, i) => `Review line ${i + 1}`), null, 2);
  await page.getByTestId("mesh-policy-text").fill(original.replace('"acls": []', `"acls": [], "note": "revised-draft", "reviewLines": ${reviewLines}`));
  await expect(page.getByTestId("mesh-policy-plan")).toHaveCount(0);
  await applyDraft.click();
  await expect(page.getByTestId("mesh-diff")).toContainText("revised-draft");
  expect(await preview.locator(".module-policy-preview-body").evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true);
  await expect(preview.getByTestId("mesh-plan-apply")).toBeInViewport({ ratio: 1 });
  await preview.locator(".module-policy-preview-body").evaluate((el) => { el.scrollTop = el.scrollHeight; });
  await expect(preview.getByTestId("mesh-plan-apply")).toBeInViewport({ ratio: 1 });
  if (process.env.XMESH_UI_QA_DIR) await page.screenshot({ path: path.join(process.env.XMESH_UI_QA_DIR, "mesh-draft-preview-long.png") });
  await page.getByTestId("mesh-plan-apply").click();
  await expect(workspace).toContainText("已加载云端策略");
  await expect(applyDraft).toBeDisabled();
  expect(await fake("policyText")).toContain("revised-draft");
  expect(errors).toEqual([]);
});

test("policy: a policy that changed after the preview is not overwritten", async () => {
  const page = await open();
  await page.getByTestId("mesh-tab-policy").click();
  await expect(page.getByTestId("mesh-policy-text")).toBeVisible();
  await page.getByTestId("mesh-template-preview").click();
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/xmesh:managed/);
  await page.getByTestId("mesh-draft-preview").click();
  await expect(page.getByTestId("mesh-policy-plan")).toBeVisible();
  await patch({ version: 7 });
  await page.getByTestId("mesh-plan-apply").click();
  await expect(page.getByTestId("mesh-error")).toContainText("云端策略版本已变化");
  await expect(page.getByTestId("mesh-policy-plan")).toHaveCount(0);
  expect(await fake("version")).toBe(7);
});

test("policy: a file-mode control plane shows the whole new policy to install by hand", async () => {
  const page = await open();
  await patch({ provider: "headscale" });
  await page.getByTestId("mesh-refresh").click();
  await expect(page.getByTestId("mesh-provider")).toContainText("headscale");
  await expect(page.getByTestId("mesh-provider")).toContainText("文件模式");
  await page.getByTestId("mesh-tab-policy").click();
  await expect(page.getByTestId("mesh-policy-readonly")).toBeVisible();
  await page.getByTestId("mesh-owner").fill("alice@");
  await page.getByTestId("mesh-template-preview").click();
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/alice@/);
  await page.getByTestId("mesh-draft-preview").click();
  await expect(page.getByTestId("mesh-plan-manual")).toBeVisible();
  await expect(page.getByTestId("mesh-plan-after")).toHaveValue(/alice@/);
  await expect(page.getByTestId("mesh-plan-apply")).toBeDisabled();
  expect(await fake("version")).toBe(1);
});

test("policy: local node selection preserves the draft and credential replacement asks before discarding", async () => {
  const page = await open();
  await page.getByTestId("mesh-tab-policy").click();
  await expect(page.getByTestId("mesh-policy-text")).toBeVisible();
  const original = await page.getByTestId("mesh-policy-text").inputValue();
  const draft = original.replace('"acls": []', '"acls": [], "note": "unsaved-node-switch"');
  await page.getByTestId("mesh-policy-text").fill(draft);
  const second = await FakeKernel.start({ pingMs: 300, status: { hostname: "server" } });
  try {
    second.writeManifest(h.root, "node-b");
    await expect.poll(async () => JSON.stringify((await h.tray()).menu)).toContain("instance.node-b");
    await h.clickMenu("instance.node-b");
    await expect(page.getByTestId("node")).toHaveText("server");
    await expect(page.getByTestId("mesh-policy-text")).toHaveValue(draft);
    await h.clickMenu("instance.node-a");
    await expect(page.getByTestId("mesh-policy-text")).toHaveValue(draft);
    await page.getByTestId("mesh-manage-credentials").click();
    await page.getByTestId("mesh-login-secret").fill("tskey-api-replacement");
    await page.getByTestId("mesh-login-submit").click();
    await expect(page.getByTestId("confirm")).toBeVisible();
    await page.getByTestId("confirm-cancel").click();
    expect((await fake("calls")).some((c) => c[1] === "login")).toBe(false);
    await expect(page.getByTestId("mesh-policy-text")).toHaveValue(draft);
  } finally {
    await second.stop();
  }
});

test("matrix: both layers are shown and mismatches can be isolated", async () => {
  const page = await open();
  await page.getByTestId("mesh-tab-matrix").click();
  await expect(page.getByTestId("mesh-matrix-table")).toBeVisible();
  const cell = page.getByTestId("mesh-cell-laptop-nas");
  await expect(cell).toContainText("放行");
  await expect(cell).toContainText("exec.run, fs.read");
  await expect(page.getByTestId("mesh-cell-stray-laptop")).toContainText("工具层放行但网络层拒绝");
  await expect(page.getByTestId("mesh-cell-stray-laptop")).toHaveAttribute("data-mismatch", "true");
  await page.getByTestId("mesh-matrix-only").check();
  await expect(page.getByTestId("mesh-cell-laptop-nas")).toHaveCount(0);
  await expect(page.getByTestId("mesh-cell-stray-laptop")).toBeVisible();
});

test("derp: probe, add to shared draft, preview and apply the complete policy", async () => {
  const page = await open();
  await page.getByTestId("mesh-tab-derp").click();
  await expect(page.getByTestId("mesh-derp-preview")).toBeDisabled();
  const fill = async (host: string): Promise<void> => {
    await page.getByTestId("mesh-region-0-code").fill("lab");
    await page.getByTestId("mesh-region-0-name").fill("Lab");
    await page.getByTestId("mesh-region-0-host").fill(host);
  };

  await page.getByTestId("mesh-region-0-code").fill("Bad Code");
  await page.getByTestId("mesh-region-0-name").fill("Lab");
  await page.getByTestId("mesh-region-0-host").fill("derp.example.com");
  await expect(page.getByTestId("mesh-derp-invalid")).toBeVisible();
  await expect(page.getByTestId("mesh-derp-probe")).toBeDisabled();

  await fill("bad.example.com");
  await expect(page.getByTestId("mesh-derp-invalid")).toHaveCount(0);
  await page.getByTestId("mesh-derp-compare").check();
  await page.getByTestId("mesh-derp-probe").click();
  await expect(page.getByTestId("mesh-probe-bad.example.com")).toContainText("失败");
  await expect(page.getByTestId("mesh-derp-official")).toContainText("nyc");

  await fill("derp.example.com");
  await page.getByTestId("mesh-derp-probe").click();
  await expect(page.getByTestId("mesh-probe-derp.example.com")).toContainText("通过");
  await page.getByTestId("mesh-derp-preview").click();
  await expect(page.getByTestId("mesh-cloud-region-900")).toContainText("derp.example.com");
  expect(await fake("version")).toBe(1);
  expect(await fake("policyText")).not.toContain("derpMap");
  await page.getByTestId("mesh-policy-workspace").getByRole("button", { name: "应用到云端", exact: true }).click();
  await expect(page.getByTestId("mesh-tab-derp")).toHaveAttribute("aria-selected", "true");
  await expect(page.getByTestId("mesh-diff")).toContainText("derp.example.com");
  await page.getByTestId("mesh-plan-apply").click();
  await expect(page.getByTestId("mesh-policy-workspace")).toContainText("已加载云端策略");
  await page.getByTestId("mesh-derp-open-policy").click();
  await expect(page.getByTestId("mesh-policy-text")).toHaveValue(/derp.example.com/);
  await expect(page.getByTestId("mesh-snapshot-snap-1")).toBeVisible();
  expect(await fake("version")).toBe(2);
  const apply = (await fake("calls")).find((c) => c[2] === "publish" && c.includes("--apply"))!;
  expect(apply).toContain("--expect-version=v1");
  expect(await fake("policyText")).toContain("derp.example.com");
  await page.getByTestId("mesh-tab-derp").click();
  await expect(page.getByTestId("mesh-cloud-region-900")).toContainText("derp.example.com");
});

test("derp: Headscale gets a generated file and snippet instead of an API write", async () => {
  const page = await open();
  await patch({ provider: "headscale" });
  await page.getByTestId("mesh-refresh").click();
  await page.getByTestId("mesh-tab-derp").click();
  await expect(page.getByTestId("mesh-derp-file-mode")).toBeVisible();
  await page.getByTestId("mesh-region-0-code").fill("lab");
  await page.getByTestId("mesh-region-0-name").fill("Lab");
  await page.getByTestId("mesh-region-0-host").fill("derp.example.com");
  await page.getByTestId("mesh-derp-preview").click();
  await expect(page.getByTestId("mesh-derp-hs-file")).toHaveValue(/regioncode: lab/);
  await expect(page.getByTestId("mesh-derp-hs-snippet")).toHaveValue(/derp:/);
  await expect(page.getByTestId("mesh-derp-apply")).toHaveCount(0);
  expect((await fake("calls")).some((c) => c[2] === "apply" || c[2] === "plan")).toBe(false);
});

test("derp admission: a local allow-list and a remote service, both through files", async () => {
  const page = await open();
  await page.getByTestId("mesh-tab-derp").click();
  await expect(page.getByTestId("mesh-admission-sync")).toBeDisabled();
  await picked("/etc/derp/allow.json");
  await page.getByTestId("mesh-admission-pick-allowlist").click();
  await page.getByTestId("mesh-admission-sync").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("mesh-admission-result")).toContainText("放行 3 台设备（新增 2，移除 1）");

  await page.getByTestId("mesh-admission-remote").check();
  await expect(page.getByTestId("mesh-admission-sync")).toBeDisabled();
  await page.getByTestId("mesh-admission-url").fill("https://derp.example.com:8480");
  await picked("/etc/derp/token");
  await page.getByTestId("mesh-admission-pick-token").click();
  await page.getByTestId("mesh-admission-all").check();
  await page.getByTestId("mesh-admission-sync").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("mesh-admission-result")).toBeVisible();
  const sync = (await fake("calls")).filter((c) => c[2] === "sync").at(-1)!;
  expect(sync.slice(0, 3)).toEqual(["mesh", "derp", "sync"]);
  expect(sync).toContain("--admission-url=https://derp.example.com:8480/");
  expect(sync).toContain("--admission-token-file=/etc/derp/token");
  expect(sync).toContain("--all-devices");
  expect(sync.some((a: string) => a.startsWith("--credential-file="))).toBe(true);
});

test("derp admission: a pasted token goes to a 0600 file, not argv or the page", async () => {
  const page = await open();
  await page.getByTestId("mesh-tab-derp").click();
  await page.getByTestId("mesh-admission-remote").check();
  await page.getByTestId("mesh-admission-url").fill("https://derp.example.com:8480");
  const token = "e2e-ADMIN-TOKEN-value";
  await page.getByTestId("mesh-admission-token").fill(token);
  await expect(page.getByTestId("mesh-admission-sync")).toBeEnabled();
  await page.getByTestId("mesh-admission-sync").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("mesh-admission-result")).toBeVisible();
  expect(await page.getByTestId("mesh-admission-token").inputValue()).toBe("");

  const sync = (await fake("calls")).filter((c) => c[2] === "sync").at(-1)!;
  const fileArg = sync.find((a: string) => a.startsWith("--admission-token-file="))!;
  expect(sync.join(" ")).not.toContain(token);
  expect(fileArg).not.toBe("--admission-token-file=/etc/derp/token");
  expect(await page.content()).not.toContain(token);
});

test("verified mesh status belongs to the selected node",async()=>{
 const page=await open();
 await patch({credential:false});
 await page.getByTestId("mesh-refresh").click();
 await page.getByTestId("mesh-connect-credentials").or(page.getByTestId("mesh-manage-credentials")).click();
 await expect(page.getByTestId("mesh-credentials-drawer")).toBeVisible();
 await expect(page.getByTestId("mesh-login-submit")).toBeDisabled();
 await page.getByTestId("mesh-login-secret").fill("tskey-api-e2e-node-scope");
 await page.getByTestId("mesh-login-submit").click();
 await expect(page.getByTestId("mesh-provider")).toContainText("tailscale");
 const second=await FakeKernel.start({pingMs:300,status:{hostname:"other-node"}});
 try {
  await patch({provider:"headscale"});
  second.writeManifest(h.root,"node-b");
  await expect.poll(async()=>JSON.stringify((await h.tray()).menu)).toContain("instance.node-b");
  await h.clickMenu("instance.node-b");
  await expect(page.getByTestId("mesh-provider")).toContainText("headscale");
 }finally {await second.stop();}
});
