import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, test } from "node:test";
import { ShellController } from "../../src/main/controller";
import { discoverInstances } from "../../src/main/discovery";
import { GatewayCredentials } from "../../src/main/gateway-token";
import { PanelService } from "../../src/main/panel-service";
import type { Capability, StatusResponse } from "../../src/shared/control";
import { statusFixture } from "../../testserver/fixtures";
import { PEER_PASSWORD, etagOf } from "../../testserver/panel-routes";
import { FakeKernel } from "../../testserver/server";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "gateway", "ingress", "audit"];
const FP = "ab".repeat(32);
const PEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n";

let root: string;
let kernels: FakeKernel[] = [];
let controllers: ShellController[] = [];

beforeEach(() => {
  root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-panel-"));
});

afterEach(async () => {
  for (const c of controllers) await c.stop();
  for (const k of kernels) await k.stop();
  kernels = [];
  controllers = [];
  fs.rmSync(root, { recursive: true, force: true });
});

async function setup(capabilities: Capability[] = ALL, status: Partial<StatusResponse> = {}) {
  const k = await FakeKernel.start({ pingMs: 100, capabilities, status });
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
  return { k, c, service: new PanelService(c) };
}

const last = (k: FakeKernel, method: string, p: string) => k.requests.filter((r) => r.method === method && r.path.startsWith(p)).at(-1);

test("sessions and config reads return the kernel's documents", async () => {
  const { k, service } = await setup();
  const s = await service.getSessions();
  assert.ok(s.ok);
  assert.ok(Array.isArray(s.data.pty));
  const c = await service.getConfig();
  assert.ok(c.ok);
  assert.equal(c.data.etag, etagOf(k.panel.content));
});

test("the deny list file goes beside the config the kernel reports, and an existing file with other text is kept", async () => {
  const node = path.join(root, "n1");
  const { service } = await setup(ALL, { paths: { ...statusFixture().paths, config_file: path.join(node, "xmesh.yaml") } });
  const text = '- "~/.ssh"\n';
  const made = await service.createDenyFile({ text });
  assert.ok(made.ok);
  assert.equal(made.data.path, path.join(node, "cwd-deny.yaml"));
  assert.equal(fs.readFileSync(made.data.path, "utf8"), text);
  if (process.platform !== "win32") assert.equal(fs.statSync(made.data.path).mode & 0o777, 0o600);

  assert.ok((await service.createDenyFile({ text })).ok, "the same text is the same file");
  const other = await service.createDenyFile({ text: '- "/x"\n' });
  assert.ok(!other.ok);
  assert.equal(other.error.code, "exists");
  assert.equal(fs.readFileSync(made.data.path, "utf8"), text);
});

test("a reload goes to the kernel's reload route; a kernel without it answers with an error", async () => {
  const { k, service } = await setup();
  const r = await service.reloadConfig();
  assert.ok(r.ok);
  assert.equal(r.data.restart_required, false);
  assert.deepEqual(r.data.restart_paths, []);
  assert.equal(last(k, "POST", "/v2/config/reload")?.path, "/v2/config/reload");

  k.failNext("/v2/config/reload", 404, "not_found", "没有这个接口");
  assert.ok(!(await service.reloadConfig()).ok);
});

test("config save sends If-Match and dry_run; a stale etag comes back with the current one", async () => {
  const { k, service } = await setup();
  const cfg = await service.getConfig();
  assert.ok(cfg.ok);
  const content = `${cfg.data.content}# edit\n`;
  const dry = await service.putConfig({ content, etag: cfg.data.etag, dryRun: true });
  assert.ok(dry.ok);
  assert.equal(k.panel.writes.length, 0, "a dry run writes nothing");
  assert.deepEqual(last(k, "PUT", "/v2/config")?.body, { content, dry_run: true });

  const stale = await service.putConfig({ content, etag: `sha256-${"0".repeat(64)}`, dryRun: false });
  assert.ok(!stale.ok);
  assert.equal(stale.error.code, "precondition_failed");
  assert.equal(stale.error.current_etag, cfg.data.etag);

  const saved = await service.putConfig({ content, etag: cfg.data.etag, dryRun: false });
  assert.ok(saved.ok);
  assert.equal(k.panel.writes.length, 1);
});

test("config diagnostics reach the caller", async () => {
  const { service } = await setup();
  const cfg = await service.getConfig();
  assert.ok(cfg.ok);
  const r = await service.putConfig({ content: "BROKEN: [", etag: cfg.data.etag, dryRun: true });
  assert.ok(!r.ok);
  assert.equal(r.error.code, "config_invalid");
  assert.ok((r.error.diagnostics?.length ?? 0) >= 1);
});

test("PAT create, rotate and delete use fixed routes and the secret comes from the kernel", async () => {
  const { k, service } = await setup();
  const made = await service.createPat({ name: "ci", scopes: ["devices:read"] });
  assert.ok(made.ok);
  assert.ok(made.data.token.length > 0);
  assert.deepEqual(last(k, "POST", "/v2/pats")?.body, { name: "ci", scopes: ["devices:read"] });
  const rot = await service.rotatePat("claude-code");
  assert.ok(rot.ok);
  assert.ok(last(k, "POST", "/v2/pats/claude-code/rotate"));
  const del = await service.deletePat("claude-code");
  assert.deepEqual(del, { ok: true });
  assert.ok(last(k, "DELETE", "/v2/pats/claude-code"));
  const list = await service.listPats();
  assert.ok(list.ok);
});

test("ingress: revoke a client, rotate the password (write-only)", async () => {
  const { k, service } = await setup();
  const before = await service.listIngressClients();
  assert.ok(before.ok);
  const id = before.data.clients[0]!.client_id;
  assert.deepEqual(await service.revokeIngressClient(id), { ok: true });
  assert.ok(last(k, "DELETE", `/v2/ingress/clients/${id}`));
  const r = await service.rotateIngressPassword("correct horse battery");
  assert.ok(r.ok);
  assert.equal(k.panel.passwordRotations, 1);
  assert.deepEqual(last(k, "PUT", "/v2/ingress/password")?.body, { password: "correct horse battery" });
});

test("pair export, and import only with a matching fingerprint", async () => {
  const { k, service } = await setup();
  const exp = await service.exportPair();
  assert.ok(exp.ok);
  const pair = { name: "peer-2", version: 2 as const, device_id: `dev-${FP}`, fingerprint: FP, public_key: "MCowBQYDK2VwAyEAAAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=", tags: ["tag:peer"] };
  const bad = await service.importPair({ pair, fingerprint: "cd".repeat(32) });
  assert.ok(!bad.ok);
  assert.equal(bad.error.code, "fingerprint_mismatch");
  assert.equal(k.panel.imported.length, 0);
  const good = await service.importPair({ pair, fingerprint: FP, tags: ["tag:device"] });
  assert.ok(good.ok);
  assert.deepEqual(k.panel.imported, [{ name: "peer-2", tags: ["tag:device"] }]);
});

test("connection password: set, read back without the value, clear; connect to a discovered device", async () => {
  const { k, service } = await setup();
  const before = await service.getPairPassword();
  assert.ok(before.ok);
  assert.equal(before.data.set, false);
  const set = await service.setPairPassword("correct horse battery");
  assert.ok(set.ok);
  assert.equal(set.data.set, true);
  assert.equal(JSON.stringify(set).includes("correct horse"), false, "the password is never echoed back");
  assert.deepEqual(last(k, "PUT", "/v2/pair/password")?.body, { password: "correct horse battery" });

  const found = await service.discoverPairs();
  assert.ok(found.ok);
  const device = found.data.devices[0]!;
  const wrong = await service.connectPair({ address: device.address, fingerprint: device.fingerprint, password: "wrong password here" });
  assert.ok(!wrong.ok);
  assert.equal(wrong.error.code, "pair_rejected");
  assert.equal(wrong.error.reason, "wrong_password");
  const limited = await service.connectPair({ address: "10.0.0.9.limited", password: PEER_PASSWORD });
  assert.ok(!limited.ok);
  assert.equal(limited.error.code, "rate_limited");
  assert.equal(limited.error.retry_after_ms, 30000);

  const good = await service.connectPair({ fingerprint: device.fingerprint, password: PEER_PASSWORD });
  assert.ok(good.ok, JSON.stringify(good));
  assert.equal(good.data.name, device.name);
  assert.ok(last(k, "POST", "/v2/pair/connect"));
  const after = await service.discoverPairs();
  assert.ok(after.ok);
  assert.equal(after.data.devices.some((d) => d.name === device.name), false, "a paired device leaves the discovery list");

  assert.deepEqual(await service.clearPairPassword(), { ok: true });
  assert.equal(k.panel.pairPassword.set, false);
});

test("device pairing works independently of the selected network mode", async () => {
  const { k, service } = await setup();
  k.panel.mode = "tailnet";
  const r = await service.exportPair();
  assert.ok(r.ok);
  assert.equal(r.data.pair.version, 2);
});

test("audit filters go in the query string and nothing else does", async () => {
  const { k, service } = await setup();
  const r = await service.queryAudit({ limit: "5", status: "error", tool: "exec.run" });
  assert.ok(r.ok);
  const req = last(k, "GET", "/v2/audit")!;
  const q = new URL(req.path, "http://x").searchParams;
  assert.equal(q.get("status"), "error");
  assert.equal(q.get("tool"), "exec.run");
  assert.equal(q.get("limit"), "5");
});

test("a capability the kernel lacks is refused locally without a request", async () => {
  const { k, service } = await setup(["status", "events", "peers", "sessions", "drain"]);
  const before = k.requests.length;
  for (const r of [await service.getConfig(), await service.listPats(), await service.queryAudit({}), await service.exportPair(), await service.listIngressClients()]) {
    assert.ok(!r.ok);
    assert.equal(r.error.code, "not_implemented");
  }
  assert.equal(k.requests.slice(before).filter((x) => x.path.startsWith("/v2/config") || x.path.startsWith("/v2/pats") || x.path.startsWith("/v2/audit")).length, 0);
});

test("call events feed the recent-calls ring, newest last", async () => {
  const { k, c, service } = await setup();
  const seen: number[] = [];
  c.on("calls", (calls: unknown[]) => seen.push(calls.length));
  const call = (n: number) => ({ tool: "exec.run", device: "d1", caller: "u", oauth_client: null, status: "ok", error: null, duration_ms: n });
  for (let i = 1; i <= 3; i++) k.emit("call", call(i));
  const deadline = Date.now() + 3000;
  while (service.recentCalls().length < 3 && Date.now() < deadline) await new Promise((r) => setTimeout(r, 10));
  assert.deepEqual(service.recentCalls().map((x) => x.duration_ms), [1, 2, 3]);
  assert.ok(seen.length >= 1);
});

test("PATCH /v2/config: edits apply to the document, If-Match is sent, and a stale, invalid or unappliable patch writes nothing", async () => {
  const { k, service } = await setup();
  const cfg = await service.getConfig();
  assert.ok(cfg.ok);
  const edits = [
    { path: "/agent/backends/git-tools", value: { transport: "stdio", command: ["git", "mcp-server"], policy: "shared" } },
    { path: "/agent/authz/tag:ops", value: { backends: ["git-tools"], tools: ["fs.read"] } },
  ];
  const stale = await service.patchConfig({ edits, etag: `sha256-${"0".repeat(64)}`, dryRun: false });
  assert.ok(!stale.ok);
  assert.equal(stale.error.code, "precondition_failed");
  assert.equal(stale.error.current_etag, cfg.data.etag);

  const dry = await service.patchConfig({ edits, etag: cfg.data.etag, dryRun: true });
  assert.ok(dry.ok);
  assert.equal(dry.data.applied, false);
  assert.equal(k.panel.writes.length, 0, "a dry run writes nothing");
  const sent = last(k, "PATCH", "/v2/config");
  assert.deepEqual(sent?.body, { edits, dry_run: true });
  assert.equal(sent?.ifMatch, `"${cfg.data.etag}"`);

  const saved = await service.patchConfig({ edits, etag: cfg.data.etag, dryRun: false });
  assert.ok(saved.ok);
  assert.equal(saved.data.applied, true);
  assert.equal(saved.data.restart_required, false, "agent settings reload without a restart");
  assert.equal(k.panel.writes.length, 1);
  const after = await service.getConfig();
  assert.ok(after.ok);
  const agent = (after.data.parsed as { agent: { backends: Record<string, unknown>; authz: Record<string, unknown> } }).agent;
  assert.deepEqual(agent.backends["git-tools"], { transport: "stdio", command: ["git", "mcp-server"], policy: "shared" });
  assert.deepEqual(agent.authz["tag:ops"], { backends: ["git-tools"], tools: ["fs.read"] });

  // An edit that goes through a list is refused whole, and nothing from the batch is written.
  const through = await service.patchConfig({
    edits: [{ path: "/agent/disabled_tools", value: ["exec.run"] }, { path: "/agent/backends/git-tools/command/0", value: "x" }],
    etag: after.data.etag,
    dryRun: false,
  });
  assert.ok(!through.ok);
  assert.equal(through.error.code, "conflict");
  assert.equal(k.panel.writes.length, 1);

  // A document the kernel would reject answers config_invalid with nothing written.
  const broken = await service.patchConfig({ edits: [{ path: "/agent/backends/BROKEN", value: { transport: "stdio", command: ["x"], policy: "shared" } }], etag: after.data.etag, dryRun: false });
  assert.ok(!broken.ok);
  assert.equal(broken.error.code, "config_invalid");
  assert.equal(k.panel.writes.length, 1);
});

test("backend env: values go to the kernel's private store and come back as names only", async () => {
  const { k, service } = await setup();
  const secret = "sk-test-secret-value";
  const first = await service.patchBackendEnv({ name: "github", set: { GITHUB_TOKEN: secret, DEBUG: "1" }, unset: [] });
  assert.deepEqual(first, { ok: true, data: { name: "github", names: ["DEBUG", "GITHUB_TOKEN"] } });
  const sent = last(k, "PATCH", "/v2/backends/github/env");
  assert.deepEqual(sent?.body, { set: { GITHUB_TOKEN: secret, DEBUG: "1" } }, "an empty unset is left out of the body");
  assert.deepEqual(k.panel.envValues("github"), { GITHUB_TOKEN: secret, DEBUG: "1" });
  assert.ok(!JSON.stringify(first).includes(secret));

  const second = await service.patchBackendEnv({ name: "github", set: {}, unset: ["DEBUG"] });
  assert.deepEqual(second, { ok: true, data: { name: "github", names: ["GITHUB_TOKEN"] } });
  assert.deepEqual(last(k, "PATCH", "/v2/backends/github/env")?.body, { unset: ["DEBUG"] });
  assert.deepEqual(k.panel.envValues("github"), { GITHUB_TOKEN: secret });

  // Neither the status nor the config the panel reads ever carries a value.
  const cfg = await service.getConfig();
  assert.ok(cfg.ok);
  assert.ok(!JSON.stringify(cfg.data).includes(secret));
  assert.ok(!JSON.stringify(k.currentStatus).includes(secret));
});

test("backend env: a name the policy refuses answers env_denied with the variable named, and nothing is stored", async () => {
  const { k, service } = await setup();
  const r = await service.patchBackendEnv({ name: "github", set: { LD_PRELOAD: "/tmp/x.so" }, unset: [] });
  assert.ok(!r.ok);
  assert.equal(r.error.code, "invalid_args");
  assert.equal(r.error.reason, "env_denied");
  assert.equal(r.error.field, "set.LD_PRELOAD");
  assert.deepEqual(k.panel.envValues("github"), {});
});

test("backend env: clearing a backend removes every variable and is not an error when there were none", async () => {
  const { k, service } = await setup();
  assert.ok((await service.patchBackendEnv({ name: "git-tools", set: { A: "1" }, unset: [] })).ok);
  assert.deepEqual(await service.clearBackendEnv("git-tools"), { ok: true });
  assert.equal(last(k, "DELETE", "/v2/backends/git-tools/env")?.path, "/v2/backends/git-tools/env");
  assert.deepEqual(k.panel.envValues("git-tools"), {});
  assert.deepEqual(await service.clearBackendEnv("git-tools"), { ok: true });
});

test("backend env needs config.write", async () => {
  const { service } = await setup(ALL.filter((c) => c !== "config.write"));
  const r = await service.patchBackendEnv({ name: "github", set: { A: "1" }, unset: [] });
  assert.ok(!r.ok);
  assert.ok(!(await service.clearBackendEnv("github")).ok);
});

test("the status lists the configured backends with their variable names, and a removed backend loses its variables", async () => {
  const { k, c, service } = await setup();
  const cfg = await service.getConfig();
  assert.ok(cfg.ok);
  const stdio = { transport: "stdio", command: ["npx", "srv"], policy: "shared" };
  assert.ok((await service.patchBackendEnv({ name: "github", set: { GITHUB_TOKEN: "t" }, unset: [] })).ok);
  const added = await service.patchConfig({ edits: [{ path: "/agent/backends/github", value: stdio }, { path: "/agent/backends/remote", value: { transport: "http", url: "http://localhost:9/mcp", policy: "shared" } }], etag: cfg.data.etag, dryRun: false });
  assert.ok(added.ok);
  const names = (): string[][] => {
    const s = c.snapshot.connection;
    return s.kind === "connected" ? s.status.backends.map((b) => [b.name, ...(b.env_names ?? [])]) : [];
  };
  const wait = async (want: string[][]): Promise<void> => {
    const deadline = Date.now() + 3000;
    while (JSON.stringify(names()) !== JSON.stringify(want) && Date.now() < deadline) await new Promise((r) => setTimeout(r, 10));
    assert.deepEqual(names(), want);
  };
  await wait([["github", "GITHUB_TOKEN"], ["remote"]]);

  const mid = await service.getConfig();
  assert.ok(mid.ok);
  assert.ok((await service.patchConfig({ edits: [{ path: "/agent/backends/github", delete: true }], etag: mid.data.etag, dryRun: false })).ok);
  assert.deepEqual(k.panel.envValues("github"), {}, "deleting the backend drops its variables");
  await wait([["remote"]]);
});

test("PATCH /v2/config delete removes a key and leaves the rest of the document alone", async () => {
  const { k, service } = await setup();
  const cfg = await service.getConfig();
  assert.ok(cfg.ok);
  const first = await service.patchConfig({ edits: [{ path: "/agent/disabled_tools", value: ["exec.run"] }], etag: cfg.data.etag, dryRun: false });
  assert.ok(first.ok);
  const mid = await service.getConfig();
  assert.ok(mid.ok);
  const gone = await service.patchConfig({ edits: [{ path: "/agent/disabled_tools", delete: true }], etag: mid.data.etag, dryRun: false });
  assert.ok(gone.ok);
  assert.equal(gone.data.diff[0]?.op, "remove");
  const end = JSON.parse(k.panel.content) as Record<string, unknown>;
  assert.equal("disabled_tools" in ((end.agent as Record<string, unknown>) ?? {}), false);
  assert.deepEqual(end.node, { name: "win-dev-01", roles: ["agent"] });
});
test("the gateway reach is replaced as a whole: access, hosts and the listen scope all apply now", async () => {
  const { k, service } = await setup();
  const hot = await service.updateGateway({ listen: ":8080", access: ["localhost", "lan", "10.0.0.0/8"], hosts: ["xmesh.home.example"] });
  assert.deepEqual(hot, {
    ok: true,
    data: { listen: ":8080", access: ["localhost", "lan", "10.0.0.0/8"], hosts: ["xmesh.home.example"], restart_required: false, apply_errors: [] },
  });
  assert.deepEqual(last(k, "PUT", "/v2/gateway")?.body, { listen: ":8080", access: ["localhost", "lan", "10.0.0.0/8"], hosts: ["xmesh.home.example"] });
  assert.ok(k.currentStatus.gateway?.addresses.some((a) => a.url === "http://xmesh.home.example:8080/mcp"), "the status follows the new reach");

  const cold = await service.updateGateway({ listen: "127.0.0.1:8080", access: ["localhost"], hosts: [] });
  assert.ok(cold.ok);
  assert.equal(cold.data.restart_required, false);
  assert.equal(k.currentStatus.restart_required, false);
  assert.deepEqual(k.currentStatus.gateway?.addresses.map((a) => a.kind), ["localhost"], "loopback only: one address");

  const bad = await service.updateGateway({ listen: ":8080", access: ["192.168.1.999"], hosts: [] });
  assert.ok(!bad.ok);
  assert.equal(bad.error.code, "config_invalid");
  assert.ok((bad.error.diagnostics?.length ?? 0) >= 1);
});

test("rotating the gateway token rewrites its file; the page only learns the name and the time", async () => {
  const { k, service } = await setup();
  const file = path.join(k.tokenDir, "default.pat");
  const before = fs.readFileSync(file, "utf8").trim();
  const r = await service.rotateGatewayPat();
  assert.ok(r.ok);
  assert.deepEqual(Object.keys(r.data).sort(), ["modified_at", "name"]);
  assert.equal(r.data.name, "default");
  const after = fs.readFileSync(file, "utf8").trim();
  assert.notEqual(after, before, "the token file has the new token");
  assert.equal(JSON.stringify(r).includes(after), false, "the new token is not in what the page receives");
  assert.ok(last(k, "POST", "/v2/pats/default/rotate"));
});

test("without a default PAT, rotation is refused and nothing is rotated", async () => {
  const { k, service } = await setup();
  assert.deepEqual(await service.deletePat("default"), { ok: true });
  const r = await service.rotateGatewayPat();
  assert.ok(!r.ok);
  assert.equal(r.error.code, "not_found");
  assert.equal(last(k, "POST", "/v2/pats/default/rotate"), undefined);
});

test("the copy path reads the default PAT's token from its file, the same one the kernel stores", async () => {
  const { k, c } = await setup();
  const token = await new GatewayCredentials(c).read();
  assert.equal(token, k.panel.tokenOf("default"));
  assert.ok(token.length >= 32, "the kernel's tokens are long");
});

