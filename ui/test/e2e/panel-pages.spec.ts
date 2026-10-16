// The panel pages against the fake kernel: each page's main path through the
// real Electron app, with main-process IPC in between.

import { type Page, expect, test } from "@playwright/test";
import * as fs from "node:fs";
import * as path from "node:path";
import type { Capability } from "../../src/shared/control";
import { type Harness, launch } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit"];
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
    await page.getByTestId(`nav-${nav}`).click();
    await expect(page.getByTestId(`page-${nav}`)).toBeVisible();
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
  for (const id of ["overview", "devices", "sessions", "authz", "ingress", "config", "audit"]) {
    await page.getByTestId(`nav-${id}`).click();
    await expect(page.getByTestId(`page-${id}`)).toBeVisible();
  }
  await page.getByTestId("nav-setup").click();
  await expect(page.getByTestId("page-setup")).toBeVisible();
  expect(await violations()).toEqual([]);
});

test("overview: pause from the page, kill needs a confirmation, resume", async () => {
  const page = await open();
  await expect(page.getByTestId("page-overview")).toBeVisible();

  await page.getByTestId("drain-kill").click();
  await expect(page.getByTestId("confirm")).toBeVisible();
  await page.getByTestId("confirm-cancel").click();
  expect(h.kernel.requests.some((r) => r.path === "/v1/drain")).toBe(false);

  await page.getByTestId("drain-kill").click();
  await page.getByTestId("confirm-ok").click();
  await expect.poll(() => h.kernel.requests.find((r) => r.path === "/v1/drain")?.body).toEqual({ mode: "kill" });
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", "draining");
  await expect(page.getByTestId("draining")).toBeVisible();

  await page.getByTestId("resume").click();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", "ok");
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

  await page.getByTestId("pair-export").click();
  await expect(page.getByTestId("pair-my-fingerprint")).toContainText(h.kernel.panel.localPair.fingerprint.slice(0, 2));

  const doc = { pair: { name: "peer-2", fingerprint: FP, certificate_pem: PEM, tags: ["tag:peer"] } };
  await page.getByTestId("pair-paste").fill(JSON.stringify(doc));
  await expect(page.getByTestId("pair-peer-name")).toHaveText("peer-2");
  await expect(page.getByTestId("pair-import")).toBeDisabled();

  await page.getByTestId("pair-typed").fill("cd".repeat(32));
  await expect(page.getByTestId("pair-mismatch")).toBeVisible();
  await expect(page.getByTestId("pair-import")).toBeDisabled();

  await page.getByTestId("pair-typed").fill(FP.toUpperCase());
  await expect(page.getByTestId("pair-import")).toBeEnabled();
  await page.getByTestId("pair-import").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("pair-done")).toBeVisible();
  expect(h.kernel.panel.imported.map((i) => i.name)).toEqual(["peer-2"]);
});

test("devices: pairing says it is lan-only on a tailnet node", async () => {
  const page = await open({}, "devices");
  await expect(page.getByTestId("pair-export")).toHaveCount(0);
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
  expect(h.kernel.requests.some((r) => r.method === "DELETE" && r.path === "/v1/pats/ci")).toBe(true);
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
  await page.getByTestId("pw-new").fill("a-long-enough-password");
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
  await expect(page.getByTestId("config-saved")).toBeVisible();
  expect(h.kernel.panel.writes.length).toBe(1);
  expect(h.kernel.panel.content).toContain("allow_dangerous: true");
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
  expect(h.kernel.requests.some((r) => r.path.startsWith("/v1/audit"))).toBe(false);
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
      workspace: "",
    }),
  );
  expect(second.error?.code).toBe("conflict");
  expect(fs.readFileSync(path.join(h.root, "lab-2", "xmesh.yaml"), "utf8")).toBe(before);
});

test("panel IPC refuses malformed data operations", async () => {
  const page = await open();
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
    ].map((r) => r.error?.code);
  });
  expect(codes).toEqual(Array(7).fill("invalid_args"));
  expect(h.kernel.requests.some((r) => r.path.startsWith("/v1/pats") || r.path.startsWith("/v1/audit"))).toBe(false);
});
