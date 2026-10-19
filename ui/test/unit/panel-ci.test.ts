// The CI runners page's request validation, its pure helpers, and the main-process routes (CTR-CTL-15).

import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, test } from "node:test";
import { ShellController } from "../../src/main/controller";
import { discoverInstances } from "../../src/main/discovery";
import { PanelService } from "../../src/main/panel-service";
import { CI_OPS, ciBusy, ciCounts, ciNextStep, parseCiRequest } from "../../src/shared/ci";
import type { Capability, CiBinding, CiInstance, CiProblemCode } from "../../src/shared/control";
import { ciFailure, needsPublicConfirm, needsRegistrationToken, parseLabelInput, parseRepo, registrationTokenValid, githubTokenValid, stepOf } from "../../src/renderer/lib/ci";
import Ajv2020 from "ajv/dist/2020";
import { CiFake } from "../../testserver/ci-routes";
import { fixture, schemaDir } from "../../testserver/fixtures";
import { FakeKernel } from "../../testserver/server";

const GOOD = "github_pat_11AAAAAAA0abcdefghij_klmnopqrstuvwxyz0123456789";
const REG = "AABBCCDDEEFF1122334455667788";

test("every request the page makes parses, and nothing else does", () => {
  const valid: unknown[] = [
    { op: "get" },
    { op: "account.create", token: GOOD },
    { op: "account.create", token: GOOD, host: "ghe.example.com" },
    { op: "account.delete", id: "gh-tzf1003" },
    { op: "repos", id: "gh-tzf1003", q: "xm", page: "2" },
    { op: "binding.create", args: { target: "tzf1003/xmesh", account: "gh-tzf1003", concurrency: 2, labels: ["gpu"], confirm_public: true } },
    { op: "binding.create", args: { target: "tzf1003/xmesh", registration_token: REG } },
    { op: "binding.create", args: { import_dir: "/Users/me/actions-runner" } },
    { op: "binding.create", args: { import_dir: "C:\\actions-runner" } },
    { op: "binding.patch", id: "xmesh", patch: { paused: true } },
    { op: "binding.patch", id: "xmesh", patch: { concurrency: 3, labels: [], account: "gh-other" } },
    { op: "binding.delete", id: "xmesh" },
    { op: "binding.delete", id: "xmesh", force: true },
    { op: "binding.restart", id: "xmesh" },
    { op: "binding.repair", id: "xmesh" },
    { op: "binding.repair", id: "xmesh", registration_token: REG },
    { op: "import.inspect", dir: "/runners/a" },
  ];
  for (const v of valid) assert.deepEqual(parseCiRequest(v), v, JSON.stringify(v));
  assert.equal(new Set(valid.map((v) => (v as { op: string }).op)).size, CI_OPS.length, "the cases cover every op");

  const invalid: unknown[] = [
    null,
    "get",
    {},
    { op: "nope" },
    { op: "get", extra: 1 },
    { op: "account.create", token: "short" },
    { op: "account.create", token: `${GOOD} ` },
    { op: "account.create", token: GOOD, host: "Not A Host" },
    { op: "account.create", token: GOOD, path: "/v1/other" },
    { op: "account.delete", id: "../etc" },
    { op: "repos", id: "gh-tzf1003", page: "x" },
    { op: "repos", id: "gh-tzf1003", q: "x".repeat(101) },
    { op: "binding.create", args: {} },
    { op: "binding.create", args: { target: "tzf1003/xmesh" } },
    { op: "binding.create", args: { target: "tzf1003/xmesh", account: "a", registration_token: REG } },
    { op: "binding.create", args: { target: "tzf1003/xmesh", account: "a", import_dir: "/x" } },
    { op: "binding.create", args: { import_dir: "relative/dir" } },
    { op: "binding.create", args: { import_dir: "/a/../b" } },
    { op: "binding.create", args: { target: "no-slash", account: "a" } },
    { op: "binding.create", args: { target: "a/b", account: "a", concurrency: 0 } },
    { op: "binding.create", args: { target: "a/b", account: "a", concurrency: 17 } },
    { op: "binding.create", args: { target: "a/b", account: "a", concurrency: 1.5 } },
    { op: "binding.create", args: { target: "a/b", account: "a", labels: ["ok", "ok"] } },
    { op: "binding.create", args: { target: "a/b", account: "a", labels: ["bad label"] } },
    { op: "binding.create", args: { target: "a/b", account: "a", confirm_public: "yes" } },
    { op: "binding.create", args: { target: "a/b", account: "a", dir: "/etc" } },
    { op: "binding.patch", id: "xmesh", patch: {} },
    { op: "binding.patch", id: "xmesh", patch: { dir: "/etc" } },
    { op: "binding.patch", id: "xmesh", patch: { paused: "true" } },
    { op: "binding.delete", id: "xmesh", force: "true" },
    { op: "binding.repair", id: "xmesh", registration_token: "short" },
    { op: "import.inspect", dir: "runner" },
  ];
  for (const v of invalid) assert.equal(parseCiRequest(v), undefined, JSON.stringify(v));
});

test("counts and next steps follow the instances' states and problems", () => {
  const busy = fixture<CiBinding>("CiBinding.busy");
  const broken = fixture<CiBinding>("CiBinding.broken");
  const counts = ciCounts([busy, broken]);
  assert.equal(counts.broken, 1);
  assert.equal(counts.busy, busy.instances.filter((i) => i.state === "busy").length);
  assert.equal(ciBusy([busy]), counts.busy > 0);
  assert.deepEqual(ciCounts(undefined), { busy: 0, idle: 0, broken: 0 });

  const codes: CiProblemCode[] = ["session_conflict", "runner_removed", "registration_failed", "auth_failed", "download_failed", "missing_dependency", "start_failed", "version_deprecated", "unclassified"];
  const steps = codes.map((code) => ciNextStep({ problem: { code, message: "" } } as CiInstance));
  assert.deepEqual(steps, ["restart", "repair", "repair", "account", "restart", "restart", "restart", "restart", "restart"]);
  assert.equal(stepOf({} as CiInstance), undefined);
  assert.equal(stepOf(broken.instances[0]!), "restart");
});

test("typed text becomes a repository, labels and tokens, or is refused", () => {
  assert.equal(parseRepo("tzf1003/xmesh"), "tzf1003/xmesh");
  assert.equal(parseRepo(" https://github.com/tzf1003/xmesh.git "), "tzf1003/xmesh");
  assert.equal(parseRepo("https://github.com/tzf1003/xmesh/settings/actions/runners"), "tzf1003/xmesh");
  assert.equal(parseRepo("git@github.com:tzf1003/xmesh.git"), "tzf1003/xmesh");
  assert.equal(parseRepo("xmesh"), undefined);
  assert.equal(parseRepo("a b/c"), undefined);

  assert.deepEqual(parseLabelInput("linux, gpu  gpu，fast"), ["linux", "gpu", "fast"]);
  assert.deepEqual(parseLabelInput(""), []);
  assert.equal(parseLabelInput("bad!"), undefined);
  assert.equal(parseLabelInput(Array.from({ length: 17 }, (_, i) => `l${i}`).join(",")), undefined);

  assert.ok(githubTokenValid(` ${GOOD} `));
  assert.ok(!githubTokenValid("short"));
  assert.ok(registrationTokenValid(REG));
  assert.ok(!registrationTokenValid("has space in it"));
});

test("failures the kernel names get the panel's wording; others fall back to the kernel's message", () => {
  for (const reason of ["github_rejected", "no_admin_permission", "github_unreachable", "account_in_use", "account_not_found", "account_without_token", "accounts_unavailable", "too_many_accounts", "not_a_runner", "ephemeral_runner", "service_installed", "runner_running", "import_single_instance"]) {
    assert.equal(ciFailure({ code: "invalid_args", reason }), `ci.err.${reason}`);
  }
  assert.equal(ciFailure({ code: "conflict" }), "ci.err.conflict");
  assert.equal(ciFailure({ code: "invalid_args" }), undefined);
  assert.equal(ciFailure({ code: "internal", reason: "something_new" }), undefined);
  assert.ok(needsPublicConfirm({ reason: "public_repo_requires_confirm" }));
  assert.ok(!needsPublicConfirm({}));

  const token = { source: "token" } as CiBinding;
  const account = { source: "account", account: "gh" } as CiBinding;
  assert.ok(needsRegistrationToken(token, "runner_removed"));
  assert.ok(!needsRegistrationToken(account, "runner_removed"));
  assert.ok(!needsRegistrationToken(token, "session_conflict"));
  assert.ok(!needsRegistrationToken(token, undefined));
});

// ---- main-process routes ----

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "ci"];
let root: string;
let kernels: FakeKernel[] = [];
let controllers: ShellController[] = [];

beforeEach(() => {
  root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-ci-"));
});
afterEach(async () => {
  for (const c of controllers) await c.stop();
  for (const k of kernels) await k.stop();
  kernels = [];
  controllers = [];
  fs.rmSync(root, { recursive: true, force: true });
});

async function setup() {
  const k = await FakeKernel.start({ pingMs: 100, capabilities: ALL });
  kernels.push(k);
  k.writeManifest(root, "n1");
  const c = new ShellController({
    discover: () => discoverInstances(root),
    getLoginItem: () => false,
    setLoginItem: () => {},
    pollMs: 30,
    reconnectMs: 20,
    maxBackoffMs: 100,
    statusPollMs: 30,
    refreshDelayMs: 5,
    streamIdleMs: 1000,
  });
  controllers.push(c);
  c.start();
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("not connected")), 5000);
    const check = (): void => {
      if (c.snapshot.connection.kind === "connected" && k.connectedStreams > 0) {
        clearTimeout(timer);
        resolve();
      } else setTimeout(check, 10);
    };
    check();
  });
  return { k, service: new PanelService(c) };
}

const last = (k: FakeKernel, method: string, p: string) => k.requests.filter((r) => r.method === method && r.path.startsWith(p)).at(-1);

test("each request goes to its own kernel route with the body the contract names, and no result carries a token", async () => {
  const { k, service } = await setup();
  const overview = await service.ci({ op: "get" });
  assert.ok(overview.ok);
  assert.deepEqual(overview.data.bindings, []);

  const account = await service.ci({ op: "account.create", token: GOOD });
  assert.ok(account.ok);
  assert.deepEqual(last(k, "POST", "/v2/ci/accounts")?.body, { token: GOOD });
  assert.ok(!JSON.stringify(account.data).includes(GOOD), "only the mask comes back");

  const repos = await service.ci({ op: "repos", id: account.data.id, q: "xm", page: "2" });
  assert.ok(repos.ok);
  assert.equal(last(k, "GET", "/v2/ci/accounts")?.path, `/v2/ci/accounts/${account.data.id}/repos?q=xm&page=2`);

  const created = await service.ci({ op: "binding.create", args: { target: "tzf1003/xmesh", account: account.data.id, concurrency: 2, labels: ["gpu"] } });
  assert.ok(created.ok);
  assert.deepEqual(last(k, "POST", "/v2/ci/bindings")?.body, { target: "tzf1003/xmesh", account: account.data.id, concurrency: 2, labels: ["gpu"] });
  assert.equal(created.data.instances.length, 2);

  const publicTry = await service.ci({ op: "binding.create", args: { target: "tzf1003/oss-lib", account: account.data.id } });
  assert.ok(!publicTry.ok);
  assert.equal(publicTry.error.reason, "public_repo_requires_confirm");

  assert.ok((await service.ci({ op: "binding.patch", id: "xmesh", patch: { paused: true } })).ok);
  assert.deepEqual(last(k, "PATCH", "/v2/ci/bindings/xmesh")?.body, { paused: true });
  const restarted = await service.ci({ op: "binding.restart", id: "xmesh" });
  assert.deepEqual(restarted, { ok: true, data: null });
  assert.equal(last(k, "POST", "/v2/ci/bindings/xmesh/restart")?.path, "/v2/ci/bindings/xmesh/restart");

  assert.ok((await service.ci({ op: "binding.repair", id: "xmesh" })).ok);
  assert.equal(last(k, "POST", "/v2/ci/bindings/xmesh/repair")?.body, null, "no token, no body");
  assert.ok((await service.ci({ op: "binding.repair", id: "xmesh", registration_token: REG })).ok);
  assert.deepEqual(last(k, "POST", "/v2/ci/bindings/xmesh/repair")?.body, { registration_token: REG });

  const inspected = await service.ci({ op: "import.inspect", dir: "/runners/clean" });
  assert.ok(inspected.ok);
  assert.deepEqual(last(k, "POST", "/v2/ci/import/inspect")?.body, { dir: "/runners/clean" });

  const removed = await service.ci({ op: "binding.delete", id: "xmesh", force: true });
  assert.ok(removed.ok);
  assert.equal(removed.data.removing, true);
  assert.equal(last(k, "DELETE", "/v2/ci/bindings/xmesh")?.path, "/v2/ci/bindings/xmesh?force=true");
  const busyAccount = await service.ci({ op: "account.delete", id: account.data.id });
  assert.ok(!busyAccount.ok && busyAccount.error.reason === "account_in_use", "the binding is still draining");
  assert.ok((await service.ci({ op: "get" })).ok, "the fake drops a removed binding on the next read");
  assert.deepEqual(await service.ci({ op: "account.delete", id: account.data.id }), { ok: true, data: null });

  for (const secret of [GOOD, REG]) assert.ok(!JSON.stringify(await service.ci({ op: "get" })).includes(secret));
});

test("a kernel without the ci route answers with an error the page can show", async () => {
  const { k, service } = await setup();
  k.failNext("/v2/ci", 501, "not_implemented", "此内核尚未实现 ci");
  const r = await service.ci({ op: "get" });
  assert.ok(!r.ok);
  assert.equal(r.error.code, "not_implemented");
});

// ---- the fake speaks the contract ----

test("everything the fake kernel answers on the ci routes validates against the contract's schemas", () => {
  const ajv = new Ajv2020({ strict: false, allErrors: true });
  for (const f of fs.readdirSync(schemaDir()).filter((n) => n.endsWith(".schema.json"))) {
    ajv.addSchema(JSON.parse(fs.readFileSync(path.join(schemaDir(), f), "utf8")));
  }
  const check = (def: string, value: unknown): void => {
    const v = ajv.getSchema(`urn:xmesh:schema:control-ci:2#/$defs/${def}`);
    assert.ok(v, def);
    assert.ok(v(value), `${def} invalid: ${ajv.errorsText(v.errors)}\n${JSON.stringify(value).slice(0, 300)}`);
  };
  const fake = new CiFake();
  const call = (method: string, p: string, body?: unknown, query = "") => {
    const r = fake.handle(method, p, new URLSearchParams(query), body ?? null);
    assert.ok(r, `${method} ${p}`);
    return r;
  };

  check("CiOverview", call("GET", "/v2/ci").body);
  const account = call("POST", "/v2/ci/accounts", { token: GOOD });
  check("CiAccount", account.body);
  const id = (account.body as { id: string }).id;
  check("CiReposResponse", call("GET", `/v2/ci/accounts/${id}/repos`).body);
  check("CiReposResponse", call("GET", `/v2/ci/accounts/${id}/repos`, undefined, "page=2").body);
  for (const dir of ["clean", "service", "running", "ephemeral"]) check("CiImportInspectResponse", call("POST", "/v2/ci/import/inspect", { dir: `/runners/${dir}` }).body);

  const created = call("POST", "/v2/ci/bindings", { target: "tzf1003/xmesh", account: id, concurrency: 2, labels: ["gpu"] });
  assert.equal(created.status, 201);
  check("CiBinding", created.body);
  const taken = call("POST", "/v2/ci/bindings", { import_dir: "/runners/clean" });
  check("CiBinding", taken.body);
  check("CiBinding", call("PATCH", "/v2/ci/bindings/xmesh", { paused: true }).body);
  check("CiBinding", call("POST", "/v2/ci/bindings/xmesh/repair", {}).body);
  check("CiBinding", call("DELETE", "/v2/ci/bindings/xmesh").body);
  check("CiOverview", call("GET", "/v2/ci").body);
  for (const r of [call("POST", "/v2/ci/accounts", { token: "short" }), call("GET", "/v2/ci/accounts/none/repos"), call("POST", "/v2/ci/bindings", {})]) {
    const e = r.body as { error: string; message: string };
    assert.ok(r.status >= 400 && typeof e.error === "string" && typeof e.message === "string");
  }
});
