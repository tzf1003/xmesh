// The panel pages against the fake kernel: each page's main path through the
// real Electron app, with main-process IPC in between.

import { type Page, expect, test } from "@playwright/test";
import * as fs from "node:fs";
import * as path from "node:path";
import { spawnSync } from "node:child_process";
import type { Capability } from "../../src/shared/control";
import { type Harness, launch, navigate } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit", "gateway"];
const LAN_STATUS = { network: { mode: "lan" as const, node_id: "win-dev-01", tags: ["tag:device"] } };
const FP = "ab".repeat(32);
const PEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n";

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

async function open(opts: Parameters<typeof launch>[0] = {}, nav?: string): Promise<Page> {
  h = await launch({ ...opts, kernel: { capabilities: ALL, ...opts.kernel } });
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
  if (nav) {
    await navigate(page, nav);
    await expect(page.getByTestId(`page-${nav}`)).toBeVisible();
    if (nav === "config") await page.getByTestId("config-source-tab").click();
  }
  return page;
}

/** Fails the test when the page tripped its own CSP. */
async function watchCsp(page: Page): Promise<() => Promise<string[]>> {
  await page.evaluate(() => {
    const w = window as unknown as { __csp: string[] };
    w.__csp = [];
    document.addEventListener("securitypolicyviolation", (e) => w.__csp.push(`${e.violatedDirective} ${e.blockedURI}`));
  });
  return () => page.evaluate(() => (window as unknown as { __csp: string[] }).__csp);
}

test("every page renders under the CSP without violations", async () => {
  const page = await open({ kernel: { status: LAN_STATUS } });
  const violations = await watchCsp(page);
  for (const id of ["overview", "runtime", "devices", "sessions", "authz", "ingress", "settings", "config", "audit", "mesh"]) {
    await navigate(page, id);
    await expect(page.getByTestId(`page-${id}`)).toBeVisible();
  }
  // Once a node has a config the wizard leaves the navigation; it is checked on its own below.
  await expect(page.getByTestId("nav-setup")).toHaveCount(0);
  expect(await violations()).toEqual([]);
});

test("the wizard renders under the CSP without violations when no node has a config", async () => {
  h = await launch({ publish: false });
  const page = await h.openPanel();
  const violations = await watchCsp(page);
  await expect(page.getByTestId("page-setup")).toBeVisible();
  expect(await violations()).toEqual([]);
});

test("runtime: pause from the page, kill needs a confirmation, resume", async () => {
  const page = await open({}, "runtime");
  await expect(page.getByTestId("page-runtime")).toBeVisible();

  await page.getByTestId("drain-kill").click();
  await expect(page.getByTestId("confirm")).toBeVisible();
  await page.getByTestId("confirm-cancel").click();
  expect(h.kernel.requests.some((r) => r.path === "/v2/drain")).toBe(false);

  await page.getByTestId("drain-kill").click();
  await page.getByTestId("confirm-ok").click();
  await expect.poll(() => h.kernel.requests.find((r) => r.path === "/v2/drain")?.body).toEqual({ mode: "kill" });
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", "draining");

  await page.getByTestId("resume").click();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", "ok");
});

test("overview: an unavailable Windows L2 shows its reason and degrades the state", async () => {
  const reason = "当前账户无法读取 NTFS 变更日志，Windows L2 需要以管理员（提权）或 LocalSystem 服务运行节点";
  const warning = { code: "sandbox_unavailable" as const, severity: "warning" as const, message: `Windows L2 不可用（sandbox.unavailable）：${reason}` };
  const page = await open({ kernel: { status: { warnings: [warning] } } });
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", "degraded");
  await expect(page.getByTestId("warnings")).toContainText(reason);
});

test("overview: recent calls arrive over the event stream", async () => {
  const page = await open();
  h.kernel.emit("call", { tool: "exec.run", device: "d1", caller: "claude", oauth_client: null, status: "ok", error: null, duration_ms: 842 });
  await expect(page.getByTestId("recent-calls")).toContainText("exec.run");
  await expect(page.getByTestId("recent-calls")).toContainText("842");
});

test("devices: peers are listed and a typed fingerprint is required to import a pair", async () => {
  const page = await open({ kernel: { status: LAN_STATUS } }, "devices");
  await expect(page.getByTestId("peers-table")).toBeVisible();

  // The document flow is the advanced fallback, collapsed by default.
  await page.getByTestId("pair-advanced").locator("summary").click();
  await page.getByTestId("pair-export").click();
  await expect(page.getByTestId("pair-my-fingerprint")).toContainText(h.kernel.panel.localPair.fingerprint.slice(0, 2));

  const doc = { pair: { name: "peer-2", version: 2 as const, device_id: `dev-${FP}`, fingerprint: FP, public_key: "MCowBQYDK2VwAyEAAAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=", tags: ["tag:peer"] } };
  await page.getByTestId("pair-paste").fill(JSON.stringify(doc));
  await expect(page.getByTestId("pair-peer-name")).toHaveText("peer-2");
  await expect(page.getByTestId("pair-import")).toBeDisabled();

  await page.getByTestId("pair-typed").fill("cd".repeat(32));
  await expect(page.getByTestId("pair-mismatch")).toBeVisible();
  await expect(page.getByTestId("pair-import")).toBeDisabled();


  const admin = page.getByRole("checkbox", { name: "tag:admin", exact: true });
  await expect(admin).not.toBeChecked();
  await admin.check();
  await page.getByTestId("pair-typed").fill(FP.toUpperCase());
  await expect(page.getByTestId("pair-import")).toBeEnabled();
  await page.getByTestId("pair-import").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("pair-done")).toBeVisible();
  // The shared trust store applies the import immediately.
  await expect(page.getByTestId("apply-restart-needed")).toHaveCount(0);
  expect(h.kernel.panel.imported.map((i) => i.name)).toEqual(["peer-2"]);
  expect(h.kernel.panel.imported[0]?.tags).toContain("tag:admin");
});

test("devices: pairing is available on a tailnet node", async () => {
  const page = await open({}, "devices");
  await expect(page.getByTestId("pair-manual")).toBeVisible();
});

test("sessions: the busy fixture shows ptys and executions", async () => {
  const page = await open({}, "sessions");
  await expect(page.getByTestId("pty-table")).toBeVisible();
  await expect(page.getByTestId("exec-table")).toBeVisible();
});

test("authz: create a PAT, see its secret once, rotate and delete it", async () => {
  const page = await open({}, "authz");
  await expect(page.getByTestId("pat-claude-code")).toBeVisible();

  await page.getByTestId("pat-create").click();
  await page.getByTestId("pat-name").fill("ci");
  await page.getByTestId("pat-scopes").fill("devices:read\nnot a scope");
  await expect(page.getByTestId("pat-submit")).toBeDisabled();
  await page.getByTestId("pat-scopes").fill("devices:read\nexec:*");
  await page.getByTestId("pat-submit").click();
  const secret = page.getByTestId("pat-secret");
  await expect(secret).toBeVisible();
  const token = (await secret.textContent())?.trim() ?? "";
  expect(token.length).toBeGreaterThan(8);
  await page.getByTestId("pat-secret-close").click();
  await expect(page.getByTestId("pat-secret")).toHaveCount(0);
  await expect(page.getByTestId("pat-ci")).toBeVisible();
  // The secret is not left anywhere in the page.
  expect(await page.content()).not.toContain(token);

  await page.getByTestId("pat-ci").getByRole("button").first().click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("pat-secret")).toBeVisible();
  await page.getByTestId("pat-secret-close").click();

  await page.getByTestId("pat-ci").getByRole("button").nth(1).click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("pat-ci")).toHaveCount(0);
  expect(h.kernel.requests.some((r) => r.method === "DELETE" && r.path === "/v2/pats/ci")).toBe(true);
});

test("ingress: revoke a client and rotate the password", async () => {
  const page = await open(
    { kernel: { status: { roles: ["agent", "gateway", "ingress"], ingress: { listen: "tunnel", issuer: "https://win-dev-01.tail1234.ts.net" } } } },
    "ingress",
  );
  await expect(page.getByTestId("issuer")).toContainText("tail1234");
  await expect(page.getByTestId("client-c_7f1a")).toBeVisible();

  await page.getByTestId("client-c_7f1a").getByRole("button").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("client-c_7f1a")).toHaveCount(0);

  await page.getByTestId("pw-new").fill("short");
  await expect(page.getByTestId("pw-submit")).toBeDisabled();
  await page.getByTestId("pw-new").fill(" a-long-enough-password ");
  await page.getByTestId("pw-again").fill(" a-long-enough-password ");
  await expect(page.getByTestId("pw-edge-space")).toBeVisible();
  await expect(page.getByTestId("pw-submit")).toBeDisabled();
  await page.getByTestId("pw-new").fill("a-long-enough-password");
  await expect(page.getByTestId("pw-edge-space")).toHaveCount(0);
  await page.getByTestId("pw-again").fill("something-else-entirely");
  await expect(page.getByTestId("pw-mismatch")).toBeVisible();
  await page.getByTestId("pw-again").fill("a-long-enough-password");
  await page.getByTestId("pw-submit").click();
  await expect.poll(() => h.kernel.panel.passwordRotations).toBe(1);
  // Write-only: the field is cleared afterwards.
  await expect(page.getByTestId("pw-new")).toHaveValue("");
});

test("config: review shows the diff and the weakening warning, then saves with the etag", async () => {
  const page = await open({}, "config");
  const editor = page.getByTestId("config-editor");
  await expect(editor).not.toHaveValue("");
  const original = await editor.inputValue();
  expect(original.length).toBeGreaterThan(0);

  await editor.fill(`${original}\nallow_dangerous: true\n`);
  await page.getByTestId("config-review").click();
  await expect(page.getByTestId("config-diff")).toBeVisible();
  await expect(page.getByTestId("config-weakens")).toBeVisible();
  expect(h.kernel.panel.writes).toEqual([]);

  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("apply-applied")).toBeVisible();
  expect(h.kernel.panel.writes.length).toBe(1);
  expect(h.kernel.panel.content).toContain("allow_dangerous: true");
});

test("config: a change to comments only can be saved", async () => {
  const page = await open({}, "config");
  const editor = page.getByTestId("config-editor");
  await expect(editor).not.toHaveValue("");
  const original = await editor.inputValue();

  await editor.fill(`# my note\n${original}`);
  await page.getByTestId("config-review").click();
  await expect(page.getByTestId("config-comments-only")).toBeVisible();
  await expect(page.getByTestId("config-weakens")).toHaveCount(0);
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("apply-applied")).toBeVisible();
  expect(h.kernel.panel.writes).toEqual([`# my note\n${original}`]);
});

test("config: a stale etag offers the latest version; an invalid config lists diagnostics", async () => {
  const page = await open({}, "config");
  const editor = page.getByTestId("config-editor");
  await expect(editor).not.toHaveValue("");
  const original = await editor.inputValue();

  h.kernel.panel.setContent(`${original}# changed elsewhere\n`);
  await editor.fill(`${original}# mine\n`);
  await page.getByTestId("config-review").click();
  await expect(page.getByTestId("config-reload-latest")).toBeVisible();
  await page.getByTestId("config-reload-latest").click();
  await expect(editor).toHaveValue(/changed elsewhere/);

  await editor.fill("BROKEN: [");
  await page.getByTestId("config-review").click();
  await expect(page.getByTestId("config-diagnostics")).toBeVisible();
  expect(h.kernel.panel.writes).toEqual([]);
});

test("audit: events are listed and filters narrow them", async () => {
  const page = await open({}, "audit");
  await expect(page.getByTestId("audit-table")).toContainText("control.config.put");
  await expect(page.getByTestId("audit-table")).toContainText("exec.run");

  await page.getByTestId("audit-tool").fill("exec.run");
  await page.getByTestId("audit-search").click();
  await expect(page.getByTestId("audit-table")).not.toContainText("control.config.put");
  await expect(page.getByTestId("audit-table")).toContainText("exec.run");
});

test("pages for capabilities the kernel lacks say so and send no request", async () => {
  const page = await open({ kernel: { capabilities: ["status", "events", "peers", "sessions", "drain"] } });
  await page.getByTestId("nav-audit").click();
  await expect(page.getByTestId("not-supported")).toBeVisible();
  expect(h.kernel.requests.some((r) => r.path.startsWith("/v2/audit"))).toBe(false);
});

test("wizard: with no kernel the first page is the wizard; it writes a 0600 config and runs install then start", async () => {
  h = await launch({ publish: false, kernel: { capabilities: ALL } });
  const page = await h.openPanel();
  await expect(page.getByTestId("page-setup")).toBeVisible();

  await page.getByTestId("wizard-name").fill("bad name");
  await expect(page.getByTestId("wizard-next")).toBeDisabled();
  await page.getByTestId("wizard-name").fill("lab-1");
  await page.getByTestId("wizard-next").click();

  await h.app.evaluate(() => {
    (globalThis as unknown as { __xmeshUiTest: { answers: { picked?: string } } }).__xmeshUiTest.answers.picked = "/home/me/work";
  });
  await page.getByTestId("wizard-pick-workspace").click();
  await expect(page.getByTestId("wizard-workspace")).toHaveValue("/home/me/work");
  await page.getByTestId("wizard-next").click();

  await expect(page.getByTestId("wizard-preview")).toContainText('name: "lab-1"');
  await page.getByTestId("wizard-run").click();
  await expect(page.getByTestId("wizard-step-write-config")).toBeVisible();
  await expect(page.getByTestId("wizard-step-start")).toBeVisible();

  const file = path.join(h.root, "lab-1", "xmesh.yaml");
  const text = fs.readFileSync(file, "utf8");
  expect(text).toContain('default_cwd: "/home/me/work"');
  // The default deny list is written beside the config, and the config names it.
  const denyFile = path.join(h.root, "lab-1", "cwd-deny.yaml");
  expect(text).toContain(`deny_file: ${JSON.stringify(denyFile)}`);
  expect(fs.readFileSync(denyFile, "utf8")).toContain('- "~/.ssh"');
  if (process.platform !== "win32") expect(fs.statSync(file).mode & 0o777).toBe(0o600);

  const calls = (await h.calls()).filter((c) => c.op === "setup-cli");
  expect(calls.map((c) => (c.arg as string[])[0])).toEqual(["install", "start"]);
  expect((calls[0]!.arg as string[]).slice(1)).toEqual(["--config", file]);
});

test("wizard: a failing install is shown and start is not attempted; an existing config is never overwritten", async () => {
  h = await launch({ publish: false });
  const page = await h.openPanel();
  await h.app.evaluate(() => {
    (globalThis as unknown as { __xmeshUiTest: { answers: { cliFails: boolean } } }).__xmeshUiTest.answers.cliFails = true;
  });
  await page.getByTestId("wizard-name").fill("lab-2");
  await page.getByTestId("wizard-gateway").uncheck();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("wizard-workspace").fill("/home/me/w");
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("wizard-run").click();
  await expect(page.getByTestId("wizard-failed")).toBeVisible();
  expect((await h.calls()).filter((c) => c.op === "setup-cli").map((c) => (c.arg as string[])[0])).toEqual(["install"]);

  // Same name again: the file exists now.
  const before = fs.readFileSync(path.join(h.root, "lab-2", "xmesh.yaml"), "utf8");
  const second = await page.evaluate(() =>
    (window as unknown as { xmesh: { runSetup(p: unknown): Promise<{ ok: boolean; error?: { code: string } }> } }).xmesh.runSetup({
      nodeName: "lab-2",
      mode: "lan",
      roles: ["gateway"],
      workspace: "/home/me/w",
    }),
  );
  expect(second.error?.code).toBe("conflict");
  expect(fs.readFileSync(path.join(h.root, "lab-2", "xmesh.yaml"), "utf8")).toBe(before);
});

test("panel IPC refuses malformed data operations", async () => {
  const page = await open();
  // The runtime page's gateway card lists the PATs on mount. The rotate button appears once that read has
  // finished, so the snapshot below counts only what the malformed calls sent.
  await navigate(page, "runtime");
  await expect(page.getByTestId("gateway-rotate")).toBeVisible();
  const before = h.kernel.requests.length;
  const codes = await page.evaluate(async () => {
    const api = (window as unknown as { xmesh: Record<string, (a?: unknown) => Promise<{ error?: { code: string } }>> }).xmesh;
    return [
      await api.putConfig!({ content: "x", etag: "nope", dryRun: false }),
      await api.createPat!({ name: "../x", scopes: ["devices:read"] }),
      await api.deletePat!("a/b"),
      await api.rotateIngressPassword!("short"),
      await api.queryAudit!({ event: "rm" }),
      await api.runSetup!({ nodeName: "x", mode: "lan", roles: ["root"], workspace: "/w" }),
      await api.pickPath!("/etc"),
      await api.cleanupStale!("../x"),
    ].map((r) => r.error?.code);
  });
  expect(codes).toEqual(Array(8).fill("invalid_args"));
  expect(h.kernel.requests.slice(before).some((r) => r.path.startsWith("/v2/pats") || r.path.startsWith("/v2/audit"))).toBe(false);
});

test("wizard: a pasted tailnet key lands in a 0600 file and never in the config", async () => {
  h = await launch({ publish: false, kernel: { capabilities: ALL } });
  const page = await h.openPanel();
  await expect(page.getByTestId("page-setup")).toBeVisible();
  await page.getByTestId("wizard-name").fill("tn-1");
  await page.getByTestId("wizard-tailnet").check();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("wizard-workspace").fill("/home/me/work");
  // Choose direct key input.
  await expect(page.getByTestId("wizard-authkey-browser")).toBeChecked();
  await page.getByTestId("wizard-authkey-paste").check();
  const key = "tskey-auth-E2E-PASTED-KEY";
  await page.getByTestId("wizard-authkey").fill(key);
  await page.getByTestId("wizard-next").click();

  await expect(page.getByTestId("wizard-preview")).not.toContainText(key);
  const keyFile = path.join(h.root, "tn-1", "authkey");
  await expect(page.getByTestId("wizard-authkey-path")).toContainText(keyFile);
  await page.getByTestId("wizard-run").click();
  await expect(page.getByTestId("wizard-step-start")).toBeVisible();

  expect(fs.readFileSync(keyFile, "utf8")).toBe(`${key}\n`);
  if (process.platform !== "win32") expect(fs.statSync(keyFile).mode & 0o777).toBe(0o600);
  const config = fs.readFileSync(path.join(h.root, "tn-1", "xmesh.yaml"), "utf8");
  expect(config).not.toContain(key);
  expect(config).toContain(`authkey_file: ${JSON.stringify(keyFile)}`);
  expect(await page.content()).not.toContain(key);
});

test("wizard: automatic admission asks the CLI for a tagged key", async () => {
  h = await launch({ publish: false, kernel: { capabilities: ALL } });
  const page = await h.openPanel();
  await page.getByTestId("wizard-name").fill("tn-2");
  await page.getByTestId("wizard-tailnet").check();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("wizard-workspace").fill("/home/me/work");
  await page.getByTestId("wizard-authkey-auto").check();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("wizard-run").click();
  await expect(page.getByTestId("wizard-step-start")).toBeVisible();
  const made = (await h.calls()).find((c) => c.op === "setup-authkey");
  expect(made?.arg).toEqual({ file: path.join(h.root, "tn-2", "authkey"), tags: ["tag:device"] });
});

test("wizard: the default browser login completes before installation", async () => {
  h = await launch({ publish: false, kernel: { capabilities: ALL } });
  const page = await h.openPanel();
  await page.getByTestId("wizard-name").fill("browser-node");
  await page.getByTestId("wizard-tailnet").check();
  await page.getByTestId("wizard-next").click();
  await page.getByTestId("wizard-workspace").fill("/home/me/work");
  await expect(page.getByTestId("wizard-authkey-browser")).toBeChecked();
  await expect(page.getByTestId("wizard-authkey")).toHaveCount(0);
  await page.getByTestId("wizard-next").click();
  await expect(page.getByTestId("wizard-preview")).toContainText("auth_method: browser");
  await page.getByTestId("wizard-run").click();
  await expect(page.getByTestId("wizard-step-start")).toBeVisible();
  const calls = (await h.calls()).filter((c) => c.op.startsWith("setup-"));
  expect(calls.map((c) => c.op)).toEqual(["setup-browser-login", "setup-cli", "setup-cli"]);
  const install = calls[1]!.arg as string[];
  expect(install.slice(-2)).toEqual(["--state-dir", path.join(h.root, "browser-node")]);
});

test("leftover kernel state: cleanup removes the manifest and keeps the node configuration", async () => {
  h = await launch({ publish: false });
  const child = spawnSync(process.execPath, ["-e", ""]);
  expect(child.status).toBe(0);
  const dir = path.join(h.root, "old-node");
  fs.mkdirSync(dir);
  const manifest = path.join(dir, "control.json");
  fs.writeFileSync(manifest, JSON.stringify({ ...h.kernel.manifest(), pid: child.pid, endpoint: `${h.kernel.endpoint}-closed` }));
  const config = path.join(dir, "xmesh.yaml");
  fs.writeFileSync(config, "node:\n  name: old-node\n");
  const page = await h.openPanel();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await expect(page.getByTestId("state")).toContainText("内核未运行");
  await expect(page.getByTestId("stale-state")).toContainText("内核进程已退出");
  await expect(page.getByTestId("start-kernel-old-node")).toBeVisible();
  await expect(page.getByTestId("restart-unresponsive-old-node")).toHaveCount(0);
  await page.screenshot({ path: test.info().outputPath("stale-state.png") });
  await page.getByTestId("cleanup-stale-old-node").click();
  await expect(page.getByTestId("stale-state")).toHaveCount(0);
  expect(fs.existsSync(manifest)).toBe(false);
  expect(fs.readFileSync(config, "utf8")).toBe("node:\n  name: old-node\n");
  await page.getByTestId("start-kernel-old-node").click();
  await expect.poll(async () => (await h.calls()).filter((c) => c.op === "cli").map((c) => c.arg)).toContain("start");
  expect(errors).toEqual([]);
});

test("leftover kernel state without a config still offers cleanup and setup", async () => {
  h = await launch({ publish: false });
  const child = spawnSync(process.execPath, ["-e", ""]);
  expect(child.status).toBe(0);
  const dir = path.join(h.root, "old-node");
  fs.mkdirSync(dir);
  fs.writeFileSync(path.join(dir, "control.json"), JSON.stringify({ ...h.kernel.manifest(), pid: child.pid, endpoint: `${h.kernel.endpoint}-closed` }));
  const page = await h.openPanel();
  await expect(page.getByTestId("cleanup-stale-old-node")).toBeVisible();
  await expect(page.getByTestId("goto-setup")).toBeVisible();
  await page.getByTestId("cleanup-stale-old-node").click();
  await expect(page.getByTestId("stale-state")).toHaveCount(0);
  await expect(page.getByTestId("page-setup")).toBeVisible();
});


test("stale state cleanup stays available alongside a connected node", async () => {
  h = await launch({ publish: false });
  const child = spawnSync(process.execPath, ["-e", ""]);
  expect(child.status).toBe(0);
  const dir = path.join(h.root, "aa-old");
  fs.mkdirSync(dir);
  const manifest = path.join(dir, "control.json");
  fs.writeFileSync(manifest, JSON.stringify({ ...h.kernel.manifest(), pid: child.pid, endpoint: `${h.kernel.endpoint}-closed` }));
  h.publish();
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
  await expect(page.getByTestId("cleanup-stale-aa-old")).toBeVisible();
  await h.clickMenu("action.cleanupStale.aa-old");
  await expect(page.getByTestId("stale-state")).toHaveCount(0);
  expect(fs.existsSync(manifest)).toBe(false);
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
});
