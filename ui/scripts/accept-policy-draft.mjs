// Real Electron/IPC/CLI and read-only cloud validation.
// Run after npm run build with XMESH_ACCEPT_BIN and XMESH_ACCEPT_CREDENTIAL_FILE.
import { _electron as electron, expect } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import os from "node:os";
import cp from "node:child_process";
import { fileURLToPath } from "node:url";
const ui = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const binary = process.env.XMESH_ACCEPT_BIN;
const credentialFile = process.env.XMESH_ACCEPT_CREDENTIAL_FILE;
if (!binary || !credentialFile) throw Error("Set XMESH_ACCEPT_BIN and XMESH_ACCEPT_CREDENTIAL_FILE");
const evidence = process.env.XMESH_ACCEPT_OUT || fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-policy-evidence-"));
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const state = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-policy-state-"));
const userData = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-policy-userdata-"));
const report = { checks: [], console: [] };
let app;
(async () => {
  try {
    fs.mkdirSync(path.join(state, "mesh"), { mode: 0o700 });
    const cred = JSON.parse(fs.readFileSync(credentialFile, "utf8"));
    cred.access = "read";
    fs.writeFileSync(path.join(state, "mesh", "mesh-credential.json"), JSON.stringify(cred), { mode: 0o600 });
    const read = cp.spawnSync(binary, ["mesh", "policy", "--show", "--json", "--credential-file=" + path.join(state, "mesh", "mesh-credential.json")], { encoding: "utf8" });
    if (read.status !== 0) throw Error("Cannot read cloud policy: " + read.stderr);
    const before = JSON.parse(read.stdout);
    const inspect = cp.spawnSync(binary, ["mesh", "policy", "document", "--json"], { input: JSON.stringify({ text: before.policy.text }), encoding: "utf8" });
    if (inspect.status !== 0) throw Error("Cannot inspect cloud policy: " + inspect.stderr);
    const document = JSON.parse(inspect.stdout);
    if (document.regions.length < 2) throw Error("This acceptance flow requires at least two existing cloud regions");
    const [first, second] = document.regions;
    let editedName = first.name + " local draft";
    app = await electron.launch({ args: ["--user-data-dir=" + userData, ui], env: { ...process.env, XMESH_UI_TEST: "0", XMESH_UI_STATE_ROOT: state, XMESH_UI_LANG: "zh", XMESH_BIN: binary } });
    await expect.poll(() => app.evaluate(({ app: app2 }) => app2.listenerCount("second-instance"))).toBeGreaterThan(0);
    await app.evaluate(({ app: app2 }) => app2.emit("second-instance"));
    const page = await app.firstWindow();
    page.on("pageerror", (e) => report.console.push(String(e)));
    page.on("console", (m) => {
      if (m.type() === "error" || m.type() === "warning") report.console.push(m.text());
    });
    await page.getByTestId("nav-advanced").click();
    await page.getByTestId("nav-mesh").click();
    await expect(page.getByTestId("mesh-tab-derp")).toBeVisible({ timeout: 40000 });
    await page.getByTestId("mesh-tab-derp").click();
    await expect(page.getByTestId(`mesh-cloud-region-${first.id}`)).toContainText(first.code, { timeout: 40000 });
    await expect(page.getByTestId(`mesh-cloud-region-${second.id}`)).toContainText(second.code);
    await expect(page.getByTestId(`mesh-cloud-region-${second.id}`)).toContainText(second.hosts[0]);
    report.url = page.url();
    report.title = await page.title();
    expect(report.url).toBe("app://panel/index.html");
    expect(report.title).toBe("xmesh");
    report.checks.push(`Cloud regions ${first.id} and ${second.id} loaded through the real CLI and Electron IPC`);
    await page.screenshot({ path: path.join(evidence, "derp-cloud-loaded.png") });
    await page.getByTestId(`mesh-cloud-region-${first.id}-edit`).click();
    const original = await page.getByTestId("mesh-cloud-region-text").inputValue();
    await page.getByTestId("mesh-cloud-region-text").fill(original.replace(JSON.stringify(first.name), JSON.stringify(editedName)));
    await page.getByTestId("mesh-cloud-region-save").click();
    await expect(page.getByTestId("mesh-cloud-region-editor")).toHaveCount(0, { timeout: 10000 });
    await expect(page.getByTestId(`mesh-cloud-region-${first.id}`)).toContainText(editedName);
    await page.getByTestId("mesh-tab-policy").click();
    expect((await page.getByTestId("mesh-policy-text").inputValue()).includes(editedName)).toBe(true);
    await page.getByTestId("mesh-draft-preview").click();
    await expect(page.getByTestId("mesh-policy-plan")).toBeVisible({ timeout: 40000 });
    await expect(page.getByTestId("mesh-policy-plan")).toContainText(editedName);
    await expect(page.getByTestId("mesh-plan-apply")).toBeDisabled();
    report.checks.push("Editing existing cloud relay updates the shared policy draft; cloud API validates full-document preview; read-only credential cannot apply");
    const raw = await page.getByTestId("mesh-policy-text").inputValue();
    const previousName = editedName;
    editedName += " source";
    await page.getByTestId("mesh-policy-text").fill(raw.replace(previousName, editedName));
    await page.getByTestId("mesh-draft-check").click();
    await expect(page.getByTestId("mesh-draft-check")).toBeEnabled();
    await page.getByTestId("mesh-template-preview").click();
    await expect(page.getByTestId("mesh-template-preview")).toBeEnabled();
    expect((await page.getByTestId("mesh-policy-text").inputValue()).includes(editedName)).toBe(true);
    report.checks.push("Source editing and template merging compose in the same draft while preserving existing relays");
    await page.getByTestId("mesh-tab-derp").click();
    await expect(page.getByTestId(`mesh-cloud-region-${first.id}`)).toContainText(editedName);
    await page.getByTestId("nav-about").click();
    await page.getByTestId("nav-mesh").click();
    await page.getByTestId("mesh-tab-derp").click();
    await expect(page.getByTestId(`mesh-cloud-region-${first.id}`)).toContainText(editedName);
    report.checks.push("Draft survives switching tabs and navigating to another page");
    await page.getByTestId("mesh-policy-cloud-refresh").click();
    await expect(page.getByTestId("confirm")).toBeVisible();
    await page.getByTestId("confirm-cancel").click();
    await expect(page.getByTestId(`mesh-cloud-region-${first.id}`)).toContainText(editedName);
    report.checks.push("Reload requires explicit confirmation when a draft has changes; cancel preserves draft");
    const run = cp.spawnSync(binary, ["mesh", "policy", "--show", "--json", "--credential-file=" + path.join(state, "mesh", "mesh-credential.json")], { encoding: "utf8" });
    if (run.status !== 0) throw Error("Cloud readback failed");
    const after = JSON.parse(run.stdout);
    expect(after.policy.version).toEqual(before.policy.version);
    expect(after.policy.text).toEqual(before.policy.text);
    report.checks.push("Cloud policy and version unchanged after local editing and preview");
    await page.screenshot({ path: path.join(evidence, "derp-shared-draft.png") });
    await page.getByTestId("mesh-policy-cloud-refresh").click();
    await page.getByTestId("confirm-ok").click();
    await expect(page.getByTestId(`mesh-cloud-region-${first.id}`).locator("strong")).toHaveText(`${first.id} \xB7 ${first.name} (${first.code})`);
    report.checks.push("Explicit reload rereads the actual cloud policy");
    expect(report.console).toEqual([]);
    report.passed = true;
    console.log(JSON.stringify({ passed: true, checks: report.checks.length, evidence }));
  } catch (e) {
    report.passed = false;
    report.error = String(e);
    throw e;
  } finally {
    fs.writeFileSync(path.join(evidence, "ui-report.json"), JSON.stringify(report, null, 2));
    if (app) {
      const child = app.process();
      const exited = child.exitCode !== null ? Promise.resolve() : new Promise((resolve) => child.once("close", resolve));
      try {
        await app.evaluate(({ app: app2 }) => app2.exit(0));
      } catch (e) {
        if (!String(e).includes("closed")) throw e;
      }
      await exited;
    }
    fs.rmSync(state, { recursive: true });
    fs.rmSync(userData, { recursive: true });
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
