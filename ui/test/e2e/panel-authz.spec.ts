// The tool authorization page against the fake kernel: MCP servers, the built-in
// tool switches and the tag grants, through the real Electron app with main-process
// IPC in between. Each action is one PATCH /v2/config carrying the etag it was read at.

import { type Page, expect, test } from "@playwright/test";
import type { Capability } from "../../src/shared/control";
import { type Harness, launch } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit"];

const SEED = {
  node: { name: "win-dev-01", roles: ["agent"] },
  agent: {
    backends: {
      fetch: { transport: "stdio", command: ["uvx", "mcp-server-fetch"], policy: "shared" },
      docs: { transport: "http", url: "http://localhost:9000/mcp", policy: "shared" },
    },
    authz: {
      "tag:dev": { tools: ["exec.run"], backends: ["fetch"] },
      "tag:ops": { tools: [], backends: ["fetch*"] },
    },
  },
};

interface Doc {
  agent?: {
    backends?: Record<string, Record<string, unknown>>;
    disabled_tools?: string[];
    authz?: Record<string, { tools?: string[]; backends?: string[] }>;
  };
}

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

/** The config as the fake kernel holds it now (what the last successful write left). */
const doc = (): Doc => JSON.parse(h.kernel.panel.content) as Doc;

/** Every PATCH the page sent, with the edit paths of each batch. */
const patchPaths = (): string[][] =>
  h.kernel.requests
    .filter((r) => r.method === "PATCH")
    .map((r) => (r.body as { edits: { path: string }[] }).edits.map((e) => e.path));

async function openAuthz(caps: Capability[] = ALL, content: string = JSON.stringify(SEED, null, 2)): Promise<Page> {
  h = await launch({ kernel: { capabilities: caps } });
  h.kernel.panel.setContent(content);
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
  await page.getByTestId("nav-authz").click();
  await expect(page.getByTestId("authz-view")).toBeVisible();
  return page;
}

test("tool access: switching a built-in tool off writes disabled_tools, and on again removes the key", async () => {
  const page = await openAuthz();
  const sw = page.getByTestId("tool-exec.run");
  await expect(sw).toBeChecked();

  await sw.click();
  await expect.poll(() => doc().agent?.disabled_tools).toEqual(["exec.run"]);
  await expect(sw).not.toBeChecked();
  // The kernel took the write at once; the page says so in the same place as every other page.
  await expect(page.getByTestId("apply-applied")).toContainText("已应用");

  await sw.click();
  await expect.poll(() => doc().agent?.disabled_tools).toBeUndefined();
  await expect(sw).toBeChecked();
  expect(patchPaths()).toEqual([["/agent/disabled_tools"], ["/agent/disabled_tools"]]);
});

test("MCP servers: a backend added from the form is one PATCH, with its grant in the same batch", async () => {
  const page = await openAuthz();
  await page.getByTestId("mcp-add").click();
  await expect(page.getByTestId("mcp-dialog")).toBeVisible();
  await expect(page.getByTestId("mcp-save")).toBeDisabled();

  await page.getByTestId("mcp-name").fill("Bad_Name");
  await expect(page.getByTestId("mcp-problems")).toBeVisible();
  await expect(page.getByTestId("mcp-save")).toBeDisabled();
  await page.getByTestId("mcp-name").fill("git-tools");
  await page.getByTestId("mcp-command").fill("git\nmcp-server");
  await page.getByTestId("grant-tag:dev").check();
  await expect(page.getByTestId("mcp-save")).toBeEnabled();
  await page.getByTestId("mcp-save").click();

  await expect(page.getByTestId("mcp-dialog")).toHaveCount(0);
  await expect(page.getByTestId("backend-git-tools")).toBeVisible();
  expect(doc().agent?.backends?.["git-tools"]).toEqual({ transport: "stdio", command: ["git", "mcp-server"], policy: "shared" });
  expect(doc().agent?.authz?.["tag:dev"]?.backends).toEqual(["fetch", "git-tools"]);
  expect(patchPaths()).toEqual([["/agent/backends/git-tools", "/agent/authz/tag:dev/backends"]]);
  expect(h.kernel.requests.find((r) => r.method === "PATCH")?.ifMatch).toBeTruthy();
});

test("import: the preview lists what will be written; a public URL is refused and an env value reaches only the kernel's env route", async () => {
  const page = await openAuthz();
  const secret = "sk-test-secret-value-9f3a";
  const text = JSON.stringify({
    mcpServers: {
      github: { command: "npx", args: ["-y", "github-mcp"], env: { GITHUB_TOKEN: secret } },
      remote: { url: "https://example.com/mcp" },
    },
  });

  await page.getByTestId("mcp-import").click();
  await page.getByTestId("mcp-import-text").fill(text);
  await page.getByTestId("mcp-import-parse").click();

  await expect(page.getByTestId("mcp-import-problems")).toContainText("remote");
  await expect(page.getByTestId("import-github")).toBeVisible();
  await expect(page.getByTestId("import-remote")).toHaveCount(0);
  await expect(page.getByTestId("mcp-import-preview")).toContainText("GITHUB_TOKEN");
  await expect(page.getByTestId("mcp-import-preview")).not.toContainText(secret);
  await expect(page.getByTestId("mcp-import-confirm")).toBeEnabled();

  await page.getByTestId("mcp-import-confirm").click();
  await expect(page.getByTestId("backend-github")).toBeVisible();
  expect(doc().agent?.backends?.github).toEqual({ transport: "stdio", command: ["npx", "-y", "github-mcp"], policy: "shared" });
  expect(doc().agent?.backends?.remote).toBeUndefined();
  expect(h.kernel.panel.content).not.toContain(secret);
  expect(h.kernel.panel.envValues("github")).toEqual({ GITHUB_TOKEN: secret });
  // The value travels in one request, the PATCH to the env route; no config request carries it.
  const carrying = h.kernel.requests.filter((r) => JSON.stringify(r.body).includes(secret));
  expect(carrying.map((r) => `${r.method} ${r.path}`)).toEqual(["PATCH /v2/backends/github/env"]);
  await expect(page.getByTestId("backend-env-github")).toContainText("GITHUB_TOKEN");
  await expect(page.getByTestId("backend-github")).not.toContainText(secret);
});

test("env form: values are written to the kernel's private store before the config, never shown again, and keep/replace/remove work per variable", async () => {
  const page = await openAuthz();
  const secret = "sk-form-secret-1";
  await page.getByTestId("mcp-add").click();
  await page.getByTestId("mcp-name").fill("git-tools");
  await page.getByTestId("mcp-command").fill("git\nmcp-server");

  // A new variable needs a name and a value; the value box hides what is typed.
  await page.getByTestId("mcp-env-add").click();
  await expect(page.getByTestId("mcp-env-value-0")).toHaveAttribute("type", "password");
  await page.getByTestId("mcp-env-name-0").fill("API_KEY");
  await expect(page.getByTestId("mcp-problems")).toBeVisible();
  await expect(page.getByTestId("mcp-save")).toBeDisabled();
  await page.getByTestId("mcp-env-value-0").fill(secret);
  await page.getByTestId("mcp-env-add").click();
  await page.getByTestId("mcp-env-name-1").fill("REGION");
  await page.getByTestId("mcp-env-value-1").fill("eu");
  await expect(page.getByTestId("mcp-save")).toBeEnabled();
  await page.getByTestId("mcp-save").click();

  await expect(page.getByTestId("mcp-dialog")).toHaveCount(0);
  expect(h.kernel.panel.envValues("git-tools")).toEqual({ API_KEY: secret, REGION: "eu" });
  expect(doc().agent?.backends?.["git-tools"]).toEqual({ transport: "stdio", command: ["git", "mcp-server"], policy: "shared" });
  expect(h.kernel.panel.content).not.toContain(secret);
  const order = h.kernel.requests.filter((r) => r.method === "PATCH").map((r) => r.path);
  expect(order).toEqual(["/v2/backends/git-tools/env", "/v2/config"]);
  await expect(page.getByTestId("backend-env-git-tools")).toContainText("API_KEY, REGION");
  await expect(page.getByTestId("backend-git-tools")).not.toContainText(secret);

  // Editing shows the stored names with an empty value box.
  await page.getByTestId("backend-edit-git-tools").click();
  await expect(page.getByTestId("mcp-env-name-0")).toHaveValue("API_KEY");
  await expect(page.getByTestId("mcp-env-name-0")).toBeDisabled();
  await expect(page.getByTestId("mcp-env-value-0")).toHaveValue("");
  await expect(page.getByTestId("mcp-env-value-1")).toHaveValue("");

  // Saving untouched keeps both values and sends no env request.
  const before = h.kernel.requests.length;
  await page.getByTestId("mcp-save").click();
  await expect(page.getByTestId("mcp-dialog")).toHaveCount(0);
  expect(h.kernel.requests.slice(before).some((r) => r.path.startsWith("/v2/backends"))).toBe(false);
  expect(h.kernel.panel.envValues("git-tools")).toEqual({ API_KEY: secret, REGION: "eu" });

  // Replace one, remove the other, add a third.
  await page.getByTestId("backend-edit-git-tools").click();
  await page.getByTestId("mcp-env-value-0").fill("sk-form-secret-2");
  await page.getByTestId("mcp-env-remove-1").click();
  await expect(page.getByTestId("mcp-env-remove-1")).toContainText("撤销");
  await page.getByTestId("mcp-env-add").click();
  await page.getByTestId("mcp-env-name-2").fill("LOG_LEVEL");
  await page.getByTestId("mcp-env-value-2").fill("debug");
  await page.getByTestId("mcp-save").click();
  await expect(page.getByTestId("mcp-dialog")).toHaveCount(0);
  expect(h.kernel.panel.envValues("git-tools")).toEqual({ API_KEY: "sk-form-secret-2", LOG_LEVEL: "debug" });
  expect(h.kernel.requests.filter((r) => r.method === "PATCH" && r.path.endsWith("/env")).at(-1)?.body).toEqual({ set: { API_KEY: "sk-form-secret-2", LOG_LEVEL: "debug" }, unset: ["REGION"] });
  await expect(page.getByTestId("backend-env-git-tools")).toContainText("API_KEY, LOG_LEVEL");
  await expect(page.getByTestId("backend-env-git-tools")).not.toContainText("REGION");

  // The page never held a value after a save: neither the table nor a reopened form shows one.
  expect(await page.content()).not.toContain("sk-form-secret");

  // Deleting the backend drops its variables with it.
  await page.getByTestId("backend-delete-git-tools").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("backend-git-tools")).toHaveCount(0);
  expect(h.kernel.panel.envValues("git-tools")).toEqual({});
});

test("env form: a name the node's policy refuses is reported by name and nothing is saved", async () => {
  const page = await openAuthz();
  await page.getByTestId("mcp-add").click();
  await page.getByTestId("mcp-name").fill("evil");
  await page.getByTestId("mcp-command").fill("x");
  await page.getByTestId("mcp-env-add").click();
  await page.getByTestId("mcp-env-name-0").fill("LD_PRELOAD");
  await page.getByTestId("mcp-env-value-0").fill("/tmp/x.so");
  await page.getByTestId("mcp-save").click();

  await expect(page.getByTestId("mcp-dialog")).toBeVisible();
  await expect(page.getByTestId("mcp-dialog").getByTestId("error")).toContainText("LD_PRELOAD");
  expect(h.kernel.panel.envValues("evil")).toEqual({});
  expect(h.kernel.panel.writes).toEqual([]);
});

test("env form: a refused config save takes back the variables written for a new backend", async () => {
  const page = await openAuthz();
  await page.getByTestId("mcp-add").click();
  await page.getByTestId("mcp-name").fill("git-tools");
  await page.getByTestId("mcp-command").fill("git");
  await page.getByTestId("mcp-env-add").click();
  await page.getByTestId("mcp-env-name-0").fill("API_KEY");
  await page.getByTestId("mcp-env-value-0").fill("sk-rollback");

  // The file changes under the page, so the config write is refused with a stale etag.
  h.kernel.panel.setContent(JSON.stringify({ ...SEED, agent: { ...SEED.agent, disabled_tools: ["exec.run"] } }, null, 2));
  await page.getByTestId("mcp-save").click();

  await expect.poll(() => h.kernel.requests.some((r) => r.method === "DELETE" && r.path === "/v2/backends/git-tools/env")).toBe(true);
  expect(h.kernel.panel.envValues("git-tools")).toEqual({});
  expect(doc().agent?.backends?.["git-tools"]).toBeUndefined();
});

test("tag grants: editing a tag's built-in tools writes that tag's list and no other tag", async () => {
  const page = await openAuthz();
  await expect(page.getByTestId("authz-view").getByRole("button", { name: "在配置中编辑" })).toBeVisible();

  await page.getByTestId("authz-edit-tag:dev").click();
  const dialog = page.getByTestId("authz-dialog");
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId("authz-tool-exec.run")).toBeChecked();
  await expect(page.getByTestId("authz-backend-fetch")).toBeChecked();

  await page.getByTestId("authz-tool-fs.read").check();
  await page.getByTestId("authz-tool-exec.run").uncheck();
  await page.getByTestId("authz-save").click();

  await expect(dialog).toHaveCount(0);
  await expect(page.getByTestId("authz-tag:dev")).toContainText("fs.read");
  expect(doc().agent?.authz?.["tag:dev"]).toEqual({ tools: ["fs.read"], backends: ["fetch"] });
  expect(doc().agent?.authz?.["tag:ops"]).toEqual({ tools: [], backends: ["fetch*"] });
});

test("deleting a backend removes its exact grants in the same write and leaves patterns alone", async () => {
  const page = await openAuthz();
  await page.getByTestId("backend-delete-fetch").click();
  await expect(page.getByTestId("confirm")).toBeVisible();
  await page.getByTestId("confirm-ok").click();

  await expect(page.getByTestId("backend-fetch")).toHaveCount(0);
  expect(doc().agent?.backends?.fetch).toBeUndefined();
  expect(doc().agent?.backends?.docs).toEqual({ transport: "http", url: "http://localhost:9000/mcp", policy: "shared" });
  expect(doc().agent?.authz?.["tag:dev"]).toEqual({ tools: ["exec.run"], backends: [] });
  expect(doc().agent?.authz?.["tag:ops"]).toEqual({ tools: [], backends: ["fetch*"] });
  expect(patchPaths()).toEqual([["/agent/backends/fetch", "/agent/authz/tag:dev/backends"]]);
});

test("a write against a config changed on disk is refused, and the page reloads the file on disk", async () => {
  const page = await openAuthz();
  const fsRead = page.getByTestId("tool-fs.read");
  await expect(fsRead).toBeChecked();

  // Someone edits the file while the page is open.
  h.kernel.panel.setContent(JSON.stringify({ ...SEED, agent: { ...SEED.agent, disabled_tools: ["exec.run"] } }, null, 2));
  await fsRead.click();

  await expect(page.getByTestId("tool-exec.run")).not.toBeChecked();
  await expect(fsRead).toBeChecked();
  expect(h.kernel.panel.writes).toEqual([]);
});

test("without config.write the lists show, and no change is offered", async () => {
  const page = await openAuthz(ALL.filter((c) => c !== "config.write"));
  await expect(page.getByTestId("config-readonly")).toBeVisible();
  await expect(page.getByTestId("backend-fetch")).toBeVisible();
  await expect(page.getByTestId("mcp-add")).toHaveCount(0);
  await expect(page.getByTestId("mcp-import")).toHaveCount(0);
  await expect(page.getByTestId("tool-exec.run")).toBeDisabled();
  await expect(page.getByTestId("authz-add")).toHaveCount(0);
  await expect(page.getByTestId("authz-edit-tag:dev")).toHaveCount(0);
  await expect(page.getByTestId("authz-delete-tag:dev")).toHaveCount(0);
});

test("an invalid config says so and offers no change", async () => {
  const page = await openAuthz(ALL, "BROKEN: [\n");
  await expect(page.getByTestId("authz-invalid")).toBeVisible();
  await expect(page.getByTestId("mcp-add")).toHaveCount(0);
  await expect(page.getByTestId("tool-exec.run")).toBeDisabled();
  expect(h.kernel.requests.some((r) => r.method === "PATCH")).toBe(false);
});
