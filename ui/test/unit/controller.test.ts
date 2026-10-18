import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, test } from "node:test";
import { ShellController } from "../../src/main/controller";
import { discoverConfigured, discoverInstances } from "../../src/main/discovery";
import type { Snapshot } from "../../src/shared/state";
import { peerNamed, peersFixture, warningOf } from "../../testserver/fixtures";
import { FakeKernel } from "../../testserver/server";

let root: string;
let controllers: ShellController[] = [];
let kernels: FakeKernel[] = [];

beforeEach(() => {
  root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-unit-"));
});

afterEach(async () => {
  for (const c of controllers) await c.stop();
  for (const k of kernels) await k.stop();
  controllers = [];
  kernels = [];
  fs.rmSync(root, { recursive: true, force: true });
});

async function kernel(opts: Parameters<typeof FakeKernel.start>[0] = {}): Promise<FakeKernel> {
  const k = await FakeKernel.start({ pingMs: 100, ...opts });
  kernels.push(k);
  return k;
}

function controller(opts: { kernelGone?: (pid: number) => boolean; protected?: boolean } = {}): ShellController {
  let login = false;
  const c = new ShellController({
    ...(opts.kernelGone && { kernelGone: opts.kernelGone }),
    discover: () => discoverInstances(root, opts.protected ? [root] : []),
    configured: () => discoverConfigured(root),
    getLoginItem: () => login,
    setLoginItem: (v) => {
      login = v;
    },
    pollMs: 30,
    reconnectMs: 20,
    maxBackoffMs: 100,
    statusPollMs: 30,
    refreshDelayMs: 5,
    streamIdleMs: 1000,
  });
  controllers.push(c);
  return c;
}

async function until(c: ShellController, ok: (s: Snapshot) => boolean, what: string): Promise<Snapshot> {
  if (ok(c.snapshot)) return c.snapshot;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      c.off("change", on);
      reject(new Error(`timed out waiting for ${what}; at ${JSON.stringify(c.snapshot.connection).slice(0, 200)}`));
    }, 5000);
    const on = (s: Snapshot): void => {
      if (!ok(s)) return;
      clearTimeout(timer);
      c.off("change", on);
      resolve(s);
    };
    c.on("change", on);
  });
}

/** Connected and subscribed: events sent from now on are delivered. */
async function ready(c: ShellController, k: FakeKernel): Promise<void> {
  await until(c, (s) => s.connection.kind === "connected", "connected");
  const deadline = Date.now() + 3000;
  while (k.connectedStreams === 0 && Date.now() < deadline) await new Promise((r) => setTimeout(r, 5));
  assert.equal(k.connectedStreams, 1, "event stream open");
}

const connectedWith = (pred: (s: Extract<Snapshot["connection"], { kind: "connected" }>) => boolean) => (s: Snapshot) =>
  s.connection.kind === "connected" && pred(s.connection);

test("with no manifest the controller reports offline, then connects when one appears", async () => {
  const c = controller();
  c.start();
  await until(c, (s) => s.connection.kind === "offline" && s.instances.length === 0, "offline");
  const k = await kernel();
  k.writeManifest(root, "n1");
  const s = await until(c, connectedWith(() => true), "connected");
  assert.equal(s.connection.kind === "connected" && s.connection.status.hostname, "win-dev-01");
  assert.deepEqual(s.instances, [{ id: "n1", label: "win-dev-01" }]);
  assert.equal(s.selectedInstance, "n1");
});

test("a node with a config but no running kernel is listed as stopped, so the UI can start it", async () => {
  fs.mkdirSync(path.join(root, "beta2-mac"));
  fs.writeFileSync(path.join(root, "beta2-mac", "xmesh.yaml"), "node:\n  name: beta2-mac\n");
  const c = controller();
  c.start();
  const s = await until(c, (x) => x.connection.kind === "offline" && x.stoppedNodes.length === 1, "stopped node");
  assert.deepEqual(s.stoppedNodes, ["beta2-mac"]);
  assert.deepEqual(s.instances, []);
});

test("a manifest whose kernel does not answer is listed as unresponsive, not as stopped, so the UI can restart it", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  await k.stop();
  // The endpoint is closed while the hosting process is still alive.
  const c = controller({ kernelGone: () => false });
  c.start();
  const s = await until(c, (x) => x.connection.kind === "offline" && x.unresponsiveNodes.length === 1, "unresponsive node");
  assert.deepEqual(s.unresponsiveNodes, ["n1"]);
  assert.deepEqual(s.staleNodes, []);
  assert.deepEqual(s.stoppedNodes, []);
});

test("a failed endpoint with an exited process is stale and the node stays startable", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  await k.stop();
  fs.writeFileSync(path.join(root, "n1", "xmesh.yaml"), "node:\n  name: n1\n");
  const c = controller({ kernelGone: () => true });
  c.start();
  const s = await until(c, (x) => x.connection.kind === "offline" && x.staleNodes.length === 1, "stale node");
  assert.deepEqual(s.staleNodes, ["n1"]);
  assert.deepEqual(s.unresponsiveNodes, []);
  assert.deepEqual(s.stoppedNodes, ["n1"], "the config is still there, so starting is what helps");
  const replacement = await kernel();
  replacement.writeManifest(root, "n1");
  await ready(c, replacement);
  assert.deepEqual(c.snapshot.staleNodes, []);
  assert.deepEqual(c.snapshot.stoppedNodes, []);
});

test("a successful control response takes precedence over the manifest PID", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  const c = controller({ kernelGone: () => true });
  c.start();
  await ready(c, k);
  assert.deepEqual(c.snapshot.staleNodes, []);
});

test("status changes pushed over the event stream reach the snapshot", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  await ready(c, k);
  k.setStatus({ restart_required: true });
  await until(c, connectedWith((x) => x.status.restart_required), "restart_required");
});

test("drain and resume call the kernel and update the snapshot from its answer", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  await until(c, connectedWith(() => true), "connected");
  await c.drain();
  assert.deepEqual(k.requests.find((r) => r.path === "/v1/drain")?.body, { mode: "keep" });
  await until(c, connectedWith((x) => x.status.drain.draining), "draining");
  await c.resume();
  await until(c, connectedWith((x) => !x.status.drain.draining), "resumed");
});

test("drain while offline fails instead of pretending", async () => {
  const c = controller();
  await assert.rejects(() => c.drain(), /not connected/);
});

test("a kernel error envelope surfaces as a ControlRequestError with its code", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  await until(c, connectedWith(() => true), "connected");
  k.failNext("/v1/drain", 409, "conflict", "already draining");
  await assert.rejects(
    () => c.drain(),
    (e: Error & { code?: string; httpStatus?: number }) =>
      e.name === "ControlRequestError" && e.code === "conflict" && e.httpStatus === 409 && e.message === "already draining",
  );
});

test("an incompatible X-Xmesh-Control-Version header is reported as a mismatch, and recovers", async () => {
  const k = await kernel({ kernelVersion: "9.9.9" });
  k.writeManifest(root, "n1"); // control.json still says 1.0
  k.setControlVersion("2.0"); // but the running kernel answers 2.0
  const c = controller();
  c.start();
  const s = await until(c, (x) => x.connection.kind === "mismatch", "mismatch");
  assert.deepEqual(s.connection, { kind: "mismatch", kernelVersion: "9.9.9", apiVersion: "2.0" });
  k.setControlVersion("1.0");
  await until(c, connectedWith(() => true), "connected again");
});

test("losing the stream triggers a reconnect that resumes from Last-Event-ID", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  await ready(c, k);
  k.setWarnings([warningOf()]);
  await until(c, connectedWith((x) => x.status.warnings.length === 1), "warning");

  k.dropStreams();
  const deadline = Date.now() + 3000;
  while (k.connectedStreams === 0 && Date.now() < deadline) await new Promise((r) => setTimeout(r, 10));
  assert.equal(k.connectedStreams, 1);
  const reopen = k.requests.filter((r) => r.path === "/v1/events").at(-1);
  assert.ok(reopen?.lastEventId, "second subscription must send Last-Event-ID");

  k.setWarnings([]);
  await until(c, connectedWith((x) => x.status.warnings.length === 0), "warning cleared after reconnect");
});

test("a dead kernel is reported offline and picked up again at the same endpoint", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  await until(c, connectedWith(() => true), "connected");
  const endpoint = k.endpoint;
  await k.stop();
  await until(c, (s) => s.connection.kind === "offline", "offline");
  const back = await kernel({ endpoint });
  assert.equal(back.endpoint, endpoint);
  await until(c, connectedWith(() => true), "reconnected");
});

test("a stale manifest does not hide a live second instance", async () => {
  const dead = await kernel();
  dead.writeManifest(root, "a-stale");
  await dead.stop();
  const live = await kernel({ status: { hostname: "server" } });
  live.writeManifest(root, "b-live");
  const c = controller();
  c.start();
  const s = await until(c, connectedWith(() => true), "connected to the live instance");
  assert.equal(s.selectedInstance, "b-live");
  assert.equal(s.connection.kind === "connected" && s.connection.status.hostname, "server");
});

test("selectInstance pins the controller to one node", async () => {
  const a = await kernel({ status: { hostname: "alpha" } });
  const b = await kernel({ status: { hostname: "beta" } });
  a.writeManifest(root, "a");
  b.writeManifest(root, "b");
  const c = controller();
  c.start();
  await until(c, connectedWith((x) => x.status.hostname === "alpha"), "alpha");
  c.selectInstance("b");
  await until(c, connectedWith((x) => x.status.hostname === "beta"), "beta");
  assert.equal(c.snapshot.selectedInstance, "b");
});

test("setLoginItem goes through the injected setter and is reflected", async () => {
  const c = controller();
  assert.equal(c.snapshot.settings.loginItem, false);
  c.setLoginItem(true);
  assert.equal(c.snapshot.settings.loginItem, true);
});

test("setDisplay goes through the injected setter, is reflected in the snapshot and notifies listeners", () => {
  const saved: unknown[] = [];
  const c = new ShellController({
    discover: () => [],
    getLoginItem: () => false,
    setLoginItem: () => {},
    getDisplay: () => ({ zoom: 1.1, uiFont: "", codeFont: "" }),
    setDisplay: (p) => void saved.push(p),
  });
  controllers.push(c);
  assert.equal(c.snapshot.settings.display.zoom, 1.1);
  const seen: number[] = [];
  c.on("change", (s) => seen.push(s.settings.display.zoom));
  c.setDisplay({ zoom: 1.5, uiFont: "Inter", codeFont: "" });
  assert.deepEqual(saved, [{ zoom: 1.5, uiFont: "Inter", codeFont: "" }]);
  assert.equal(c.snapshot.settings.display.uiFont, "Inter");
  assert.deepEqual(seen, [1.5]);
});

test("a manifest with an unsupported api_version is a mismatch without talking to the kernel", async () => {
  const k = await kernel({ controlVersion: "2.0", kernelVersion: "9.9.9" });
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  const s = await until(c, (x) => x.connection.kind === "mismatch", "mismatch");
  assert.deepEqual(s.connection, { kind: "mismatch", kernelVersion: "9.9.9", apiVersion: "2.0" });
  assert.equal(k.requests.length, 0);
});

test("a kernel restart is announced by reset: the controller re-reads and shows the new boot", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  await ready(c, k);
  k.setWarnings([warningOf()]);
  await until(c, connectedWith((x) => x.status.warnings.length === 1), "warning");
  const oldBoot = (c.snapshot.connection.kind === "connected" && c.snapshot.connection.status.control.boot_id) || "";
  assert.notEqual(oldBoot, "");

  // New process, same endpoint; the manifest is not rewritten yet, so the old Last-Event-ID is sent.
  k.restartBoot();
  k.setStatus({ warnings: [] });
  await until(c, connectedWith((x) => x.status.control.boot_id !== oldBoot && x.status.warnings.length === 0), "new boot");
  const reopen = k.requests.filter((r) => r.path === "/v1/events").at(-1);
  assert.ok(reopen?.lastEventId?.startsWith(`${oldBoot}.`), "resumed with the old boot's event id");
});

test("a rewritten manifest with a new boot id starts a fresh client with no stale event id", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  await ready(c, k);
  k.setStatus({ restart_required: true });
  await until(c, connectedWith((x) => x.status.restart_required), "event seen");

  k.restartBoot();
  k.writeManifest(root, "n1");
  k.setStatus({ restart_required: false });
  await until(c, connectedWith((x) => !x.status.restart_required), "after restart");
  const reopen = k.requests.filter((r) => r.path === "/v1/events").at(-1);
  assert.equal(reopen?.lastEventId, undefined);
});

test("a kernel without the events capability is polled, and drain is refused when it lacks drain", async () => {
  const k = await kernel({ capabilities: ["status", "peers"] });
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  await until(c, connectedWith(() => true), "connected");
  assert.equal(k.connectedStreams, 0);
  k.setStatus({ restart_required: true });
  await until(c, connectedWith((x) => x.status.restart_required), "seen by polling");
  await assert.rejects(
    () => c.drain(),
    (e: Error & { code?: string }) => e.name === "ControlRequestError" && e.code === "not_implemented",
  );
});

test("the peer list is read when peers is advertised and changes arrive as events", async () => {
  const k = await kernel();
  k.writeManifest(root, "n1");
  const c = controller();
  c.start();
  await ready(c, k);
  const names = (s: Snapshot): string[] => (s.connection.kind === "connected" ? s.connection.peers.map((p) => p.name) : []);
  assert.deepEqual(names(c.snapshot), peersFixture().map((p) => p.name));
  k.setPeers(peersFixture().map((p) => (p.name === "linux-box" ? peerNamed("linux-box", { online: true, connection: "direct" }) : p)));
  await until(
    c,
    (s) => s.connection.kind === "connected" && s.connection.peers.find((p) => p.name === "linux-box")?.online === true,
    "peer online",
  );
  assert.equal(c.snapshot.connection.kind === "connected" && c.snapshot.connection.status.peers.online, 3);
});

test("nudge drops a live event stream and the controller resubscribes at once", async () => {
  const k = await kernel();
  k.writeManifest(root, "node-a");
  const c = controller();
  c.start();
  await ready(c, k);
  const eventRequests = (): number => k.requests.filter((r) => r.path === "/v1/events").length;
  const opened = eventRequests();
  c.nudge();
  const deadline = Date.now() + 3000;
  while (eventRequests() === opened && Date.now() < deadline) await new Promise((r) => setTimeout(r, 5));
  assert.ok(eventRequests() > opened, "stream reopened after nudge");
});


test("an incompatible manifest with an exited process and missing transport can be cleaned", async () => {
  const k = await kernel({ controlVersion: "99.0" });
  k.writeManifest(root, "old");
  await k.stop();
  const c = controller({ kernelGone: () => true, protected: true });
  c.start();
  const s = await until(c, (s) => s.staleNodes.includes("old"), "stale incompatible kernel");
  assert.equal(s.connection.kind, "offline");
  assert.deepEqual(s.protectedStaleNodes, ["old"]);
});

test("a reachable incompatible kernel stays a mismatch even when the manifest PID looks gone", async () => {
  const k = await kernel({ controlVersion: "99.0" });
  k.writeManifest(root, "old");
  const c = controller({ kernelGone: () => true });
  c.start();
  const s = await until(c, (s) => s.connection.kind === "mismatch", "reachable incompatible kernel");
  assert.deepEqual(s.staleNodes, []);
  assert.deepEqual(k.requests, []);
});
