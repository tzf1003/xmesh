import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, test } from "node:test";
import { ShellController } from "../../src/main/controller";
import { discoverInstances } from "../../src/main/discovery";
import { PanelService } from "../../src/main/panel-service";
import type { Capability } from "../../src/shared/control";
import { etagOf } from "../../testserver/panel-routes";
import { FakeKernel } from "../../testserver/server";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit"];
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

async function setup(capabilities: Capability[] = ALL) {
  const k = await FakeKernel.start({ pingMs: 100, capabilities });
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

test("config save sends If-Match and dry_run; a stale etag comes back with the current one", async () => {
  const { k, service } = await setup();
  const cfg = await service.getConfig();
  assert.ok(cfg.ok);
  const content = `${cfg.data.content}# edit\n`;
  const dry = await service.putConfig({ content, etag: cfg.data.etag, dryRun: true });
  assert.ok(dry.ok);
  assert.equal(k.panel.writes.length, 0, "a dry run writes nothing");
  assert.deepEqual(last(k, "PUT", "/v1/config")?.body, { content, dry_run: true });

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
  assert.deepEqual(last(k, "POST", "/v1/pats")?.body, { name: "ci", scopes: ["devices:read"] });
  const rot = await service.rotatePat("claude-code");
  assert.ok(rot.ok);
  assert.ok(last(k, "POST", "/v1/pats/claude-code/rotate"));
  const del = await service.deletePat("claude-code");
  assert.deepEqual(del, { ok: true });
  assert.ok(last(k, "DELETE", "/v1/pats/claude-code"));
  const list = await service.listPats();
  assert.ok(list.ok);
});

test("ingress: revoke a client, rotate the password (write-only)", async () => {
  const { k, service } = await setup();
  const before = await service.listIngressClients();
  assert.ok(before.ok);
  const id = before.data.clients[0]!.client_id;
  assert.deepEqual(await service.revokeIngressClient(id), { ok: true });
  assert.ok(last(k, "DELETE", `/v1/ingress/clients/${id}`));
  const r = await service.rotateIngressPassword("correct horse battery");
  assert.ok(r.ok);
  assert.equal(k.panel.passwordRotations, 1);
  assert.deepEqual(last(k, "PUT", "/v1/ingress/password")?.body, { password: "correct horse battery" });
});

test("pair export, and import only with a matching fingerprint", async () => {
  const { k, service } = await setup();
  const exp = await service.exportPair();
  assert.ok(exp.ok);
  const pair = { name: "peer-2", fingerprint: FP, certificate_pem: PEM, tags: ["tag:peer"] };
  const bad = await service.importPair({ pair, fingerprint: "cd".repeat(32) });
  assert.ok(!bad.ok);
  assert.equal(bad.error.code, "fingerprint_mismatch");
  assert.equal(k.panel.imported.length, 0);
  const good = await service.importPair({ pair, fingerprint: FP, tags: ["tag:device"] });
  assert.ok(good.ok);
  assert.deepEqual(k.panel.imported, [{ name: "peer-2", tags: ["tag:device"] }]);
});

test("pairing outside lan mode is reported as unsupported_mode", async () => {
  const { k, service } = await setup();
  k.panel.mode = "tailnet";
  const r = await service.exportPair();
  assert.ok(!r.ok);
  assert.equal(r.error.code, "unsupported_mode");
});

test("audit filters go in the query string and nothing else does", async () => {
  const { k, service } = await setup();
  const r = await service.queryAudit({ limit: "5", status: "error", tool: "exec.run" });
  assert.ok(r.ok);
  const req = last(k, "GET", "/v1/audit")!;
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
  assert.equal(k.requests.slice(before).filter((x) => x.path.startsWith("/v1/config") || x.path.startsWith("/v1/pats") || x.path.startsWith("/v1/audit")).length, 0);
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

test("PATCH /v1/config: edits apply to the document, If-Match is sent, and a stale, invalid or unappliable patch writes nothing", async () => {
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
  const sent = last(k, "PATCH", "/v1/config");
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

test("PATCH /v1/config delete removes a key and leaves the rest of the document alone", async () => {
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
