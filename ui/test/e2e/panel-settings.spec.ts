// The settings page against the fake kernel: the ingress entry lands on it with the ingress role
// preselected, and a change to the working directories is reviewed before it is written.

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { type Page, expect, test } from "@playwright/test";
import { parse } from "yaml";
import type { Capability } from "../../src/shared/control";
import { statusFixture } from "../../testserver/fixtures";
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
  await expect(page.getByTestId("settings-role-summary")).toContainText("ingress");
  await expect(page.getByTestId("settings-ingress")).toBeVisible();
  // A public listener or reverse tunnel can use LAN. Funnel requires Tailnet.
  await expect(page.getByTestId("settings-needs-tailnet")).toHaveCount(0);
  await page.getByTestId("settings-listen").selectOption("funnel");
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
  await expect(page.getByTestId("apply-applied")).toContainText("已应用");
  await expect(page.getByTestId("apply-restart")).toHaveCount(0);
  expect(h.kernel.panel.writes.length).toBe(1);
  expect(h.kernel.panel.content).toContain("allow: []");
  expect(h.kernel.panel.content).toContain('deny: ["~/.ssh"]');
});

test("settings: a setting the running kernel could not apply is shown with its reason, not as live", async () => {
  const page = await open();
  h.kernel.panel.setContent(FULL_CONFIG);
  h.kernel.panel.applyErrors = [{ section: "gateway", message: "网关没能改到监听地址 :8080，仍在 127.0.0.1:8080 上：address already in use" }];
  await page.getByTestId("nav-settings").click();

  await page.getByTestId("settings-allow-remove-0").click();
  await page.getByTestId("settings-review").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("apply-failed")).toBeVisible();
  await expect(page.getByTestId("apply-error")).toContainText("address already in use");
  await expect(page.getByTestId("apply-applied")).toHaveCount(0);
  await expect(page.getByTestId("apply-restart")).toHaveCount(0);
});

test("settings: a deny list still inline moves to its own file on save, and the config points at it", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-deny-e2e-"));
  try {
    const page = await open({ status: { paths: { ...statusFixture().paths, config_file: path.join(dir, "xmesh.yaml") } } });
    h.kernel.panel.setContent(FULL_CONFIG);
    await page.getByTestId("nav-settings").click();
    await expect(page.getByTestId("settings-deny-unset")).toBeVisible();
    await expect(page.getByTestId("deny-entry-0")).toHaveValue("~/.ssh");

    await page.getByTestId("deny-review").click();
    await page.getByTestId("confirm-ok").click();
    await expect(page.getByTestId("settings-deny-unset")).toHaveCount(0);
    await expect(page.getByTestId("deny-entry-0")).toHaveValue("~/.ssh");

    const file = path.join(dir, "cwd-deny.yaml");
    expect(fs.readFileSync(file, "utf8")).toContain('- "~/.ssh"');
    expect(h.kernel.panel.content).toMatch(/deny_file: .*cwd-deny\.yaml/);
    expect(h.kernel.panel.content).toContain(file);
    expect(h.kernel.panel.content).not.toContain('deny: ["~/.ssh"]');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("settings: an unsaved change to the form holds the move until it is saved or dropped", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-deny-e2e-"));
  try {
    const page = await open({ status: { paths: { ...statusFixture().paths, config_file: path.join(dir, "xmesh.yaml") } } });
    h.kernel.panel.setContent(FULL_CONFIG);
    await page.getByTestId("nav-settings").click();
    await page.getByTestId("settings-allow-remove-0").click();
    await expect(page.getByTestId("deny-finish-first")).toBeVisible();
    await expect(page.getByTestId("deny-review")).toBeDisabled();
    expect(fs.existsSync(path.join(dir, "cwd-deny.yaml"))).toBe(false);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("settings: a change that only takes effect after a restart says so, names it, and restarts from the same place", async () => {
  const page = await open();
  h.kernel.panel.setContent(FULL_CONFIG);
  h.kernel.panel.restartPaths = ["/mesh/mode"];
  await page.getByTestId("nav-settings").click();

  await page.getByTestId("settings-allow-remove-0").click();
  await page.getByTestId("settings-review").click();
  await page.getByTestId("confirm-ok").click();

  await expect(page.getByTestId("apply-restart-needed")).toContainText("需要重启内核");
  await expect(page.getByTestId("apply-waiting")).toContainText("组网方式");
  await expect(page.getByTestId("apply-applied")).toHaveCount(0);

  await page.getByTestId("apply-restart").click();
  await expect(page.getByTestId("confirm")).toBeVisible();
  expect((await h.calls()).filter((c) => c.op === "cli")).toEqual([]);
  await page.getByTestId("confirm-ok").click();
  await expect.poll(async () => (await h.calls()).filter((c) => c.op === "cli").map((c) => c.arg)).toEqual(["restart"]);
  await expect(page.getByTestId("apply-waiting")).toHaveCount(0);
});

test("settings: editing the form again withdraws the 'applied' of the last save", async () => {
  const page = await open();
  h.kernel.panel.setContent(FULL_CONFIG);
  await page.getByTestId("nav-settings").click();

  await page.getByTestId("settings-allow-remove-0").click();
  await page.getByTestId("settings-review").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("apply-applied")).toBeVisible();

  await page.getByTestId("settings-name").fill("dev-2");
  await expect(page.getByTestId("apply-applied")).toHaveCount(0);
});

test("a restart the kernel is waiting on shows on every page, with the restart button, and the 'applied' note does not follow the user around", async () => {
  const page = await open();
  h.kernel.panel.setContent(FULL_CONFIG);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-allow-remove-0").click();
  await page.getByTestId("settings-review").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("apply-applied")).toBeVisible();

  await page.getByTestId("nav-overview").click();
  await expect(page.getByTestId("apply-applied")).toHaveCount(0);
  await expect(page.getByTestId("apply-status")).toHaveCount(0);

  // The kernel says a restart is due (a file edited by hand, an imported pairing): any page shows it.
  h.kernel.setStatus({ restart_required: true });
  await expect(page.getByTestId("apply-restart-needed")).toBeVisible();
  await page.getByTestId("nav-audit").click();
  await expect(page.getByTestId("apply-restart")).toBeEnabled();
});


test("settings: file transfer limit defaults to 1GiB, saves custom values and unlimited", async () => {
  const page = await open();
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
  await expect(page).toHaveTitle(/xmesh/i);
  h.kernel.panel.setContent(FULL_CONFIG);
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-max-file-size")).toHaveValue("1GiB");
  await page.getByTestId("settings-max-file-size").fill("2GiB");
  await page.getByTestId("settings-review").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("settings-max-file-size")).toHaveValue("2GiB");
  expect(h.kernel.panel.content).toContain("max_file_size: 2GiB");
  await page.getByTestId("settings-file-unlimited").check();
  await expect(page.getByTestId("settings-max-file-size")).toHaveCount(0);
  await page.getByTestId("settings-review").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("settings-file-unlimited")).toBeChecked();
  expect(h.kernel.panel.content).toContain("max_file_size: unlimited");
  await page.getByTestId("settings-file-unlimited").uncheck();
  await page.getByTestId("settings-max-file-size").fill("0");
  await expect(page.getByTestId("settings-review")).toBeDisabled();
  await page.getByTestId("settings-max-file-size").fill("1GiB");
  await page.getByTestId("settings-transfers").scrollIntoViewIfNeeded();
  await expect(page.locator("vite-error-overlay")).toHaveCount(0);
  expect(errors).toEqual([]);
  await page.screenshot({ path: path.join(os.tmpdir(), "xmesh-file-transfer-settings.png") });
});

for (const mode of ["tailnet", "coexist"] as const) {
  test(`settings: enter an auth key and save ${mode} authorization`, async ({}, testInfo) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-authkey-e2e-"));
    try {
      const page = await open({ status: { paths: { ...statusFixture().paths, config_file: path.join(dir, "xmesh.yaml") } } });
      const errors: string[] = [];
      page.on("pageerror", error => errors.push(error.message));
      page.on("console", message => { if (message.type() === "error") errors.push(message.text()); });
      await expect(page).toHaveTitle(/xmesh/i);
      expect(page.url()).toContain("app://panel");
      h.kernel.panel.setContent(FULL_CONFIG);
      await page.getByTestId("nav-settings").click();
      await page.getByTestId(`settings-mode-${mode}`).check();
      const input = page.getByTestId("settings-authkey");
      await expect(input).toHaveAttribute("type", "password");
      await expect(page.getByTestId("settings-node").getByRole("button")).toHaveCount(0);
      await expect(page.getByTestId("settings-review")).toBeDisabled();
      await input.fill("short");
      await expect(input).toHaveAttribute("aria-invalid", "true");
      await expect(page.getByTestId("settings-review")).toBeDisabled();

      const authkey = "tskey-auth-e2e-settings";
      await input.fill(` ${authkey} `);
      await expect(page.getByTestId("settings-review")).toBeEnabled();
      await page.getByTestId("settings-review").click();
      await expect(page.getByTestId("settings-authkey-change")).toBeVisible();
      await expect(page.getByTestId("settings-diff")).not.toContainText(authkey);
      expect(fs.readdirSync(dir)).toEqual([]);
      expect(h.kernel.panel.writes).toEqual([]);
      await page.getByTestId("confirm-cancel").click();
      expect(fs.readdirSync(dir)).toEqual([]);
      await page.getByTestId("settings-review").click();
      await page.getByTestId("confirm-ok").click();
      await expect(input).toHaveValue("");
      await expect(input).toHaveAttribute("placeholder", /已配置授权/);

      const saved = parse(h.kernel.panel.content);
      expect(saved.mesh.lan.enabled).toBe(mode === "coexist");
      expect(saved.mesh.tailnet.enabled).toBe(true);
      const file = saved.mesh.tailnet.authkey_file as string;
      expect(path.dirname(file)).toBe(dir);
      expect(fs.readFileSync(file, "utf8")).toBe(`${authkey}\n`);
      expect(JSON.stringify(h.kernel.requests)).not.toContain(authkey);
      await expect(page.locator("body")).not.toContainText(authkey);
      await expect(page.locator("vite-error-overlay")).toHaveCount(0);
      await page.getByTestId("settings-node").scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath(`auth-key-${mode}.png`) });
      expect(errors).toEqual([]);

      await page.getByTestId("settings-name").fill("renamed-node");
      await page.getByTestId("settings-review").click();
      await expect(page.getByTestId("settings-authkey-change")).toHaveCount(0);
      await page.getByTestId("confirm-ok").click();
      await expect(page.getByTestId("settings-name")).toHaveValue("renamed-node");
      expect(parse(h.kernel.panel.content).mesh.tailnet.authkey_file).toBe(file);
      expect(fs.readdirSync(dir)).toEqual([path.basename(file)]);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });
}

test("settings: a pasted key replaces browser authorization and can be rotated", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-authkey-e2e-"));
  try {
    const page = await open({ status: { paths: { ...statusFixture().paths, config_file: path.join(dir, "xmesh.yaml") } } });
    h.kernel.panel.setContent(FULL_CONFIG.replace("mesh:\n  mode: lan", "schema_version: 2\nmesh:\n  tailnet:\n    enabled: true\n    auth_method: browser"));
    await page.getByTestId("nav-settings").click();
    const input = page.getByTestId("settings-authkey");
    await expect(input).toHaveValue("");
    await expect(input).toHaveAttribute("placeholder", /已配置授权/);

    const files: string[] = [];
    for (const authkey of ["hskey-e2e-initial", "tskey-auth-e2e-rotated"]) {
      await input.fill(authkey);
      await page.getByTestId("settings-review").click();
      await page.getByTestId("confirm-ok").click();
      await expect(input).toHaveValue("");
      const saved = parse(h.kernel.panel.content);
      expect(saved.mesh.tailnet.auth_method).toBeUndefined();
      const file = saved.mesh.tailnet.authkey_file as string;
      expect(fs.readFileSync(file, "utf8")).toBe(`${authkey}\n`);
      files.push(file);
    }
    expect(files[0]).not.toBe(files[1]);
    expect(fs.readFileSync(files[0]!, "utf8")).toBe("hskey-e2e-initial\n");
    expect(JSON.stringify(h.kernel.requests)).not.toContain("tskey-auth-e2e-rotated");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
