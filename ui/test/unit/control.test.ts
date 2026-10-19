import assert from "node:assert/strict";
import { test } from "node:test";
import {
  apiMajor,
  ControlRequestError,
  hasCapability,
  isDegrading,
  isSupportedApiVersion,
  parseEvent,
  parseManifest,
  parsePeers,
  parseStatus,
} from "../../src/shared/control";
import type { CiBinding } from "../../src/shared/control";
import { deriveIcon } from "../../src/shared/state";
import { eventFixture, fixture, peersFixture, statusFixture, statusWith, warningOf } from "../../testserver/fixtures";

test("apiMajor and compatibility", () => {
  assert.equal(apiMajor("1"), 1);
  assert.equal(apiMajor("1.4"), 1);
  assert.equal(apiMajor("v1.4.2"), 1);
  assert.ok(Number.isNaN(apiMajor("")));
  assert.ok(Number.isNaN(apiMajor("x1")));
  assert.equal(isSupportedApiVersion("1.9"), true);
  assert.equal(isSupportedApiVersion("2.0"), false);
  assert.equal(isSupportedApiVersion(""), false);
});

test("the contract's control.json fixtures parse", () => {
  for (const name of ["ControlEndpointFile.unix", "ControlEndpointFile.pipe"]) {
    const m = parseManifest(fixture(name));
    assert.ok(m, name);
    assert.equal(m.api_version, "1.0");
  }
});

test("parseManifest rejects what the UI cannot connect to, and drops unknown capabilities", () => {
  assert.equal(parseManifest(null), undefined);
  assert.equal(parseManifest({}), undefined);
  assert.equal(parseManifest({ endpoint: "", transport: "unix", api_version: "1.0" }), undefined);
  assert.equal(parseManifest({ endpoint: "/x.sock", transport: "tcp", api_version: "1.0" }), undefined);
  assert.equal(parseManifest({ endpoint: "/x.sock", transport: "unix" }), undefined);
  const m = parseManifest({ endpoint: "/x.sock", transport: "unix", api_version: "1.8", capabilities: ["status", "teleport", 3] });
  assert.deepEqual(m?.capabilities, ["status"]);
});

test("every contract StatusResponse fixture parses and keeps its fields", () => {
  for (const v of ["healthy", "degraded-draining"] as const) {
    const raw = statusFixture(v);
    assert.deepEqual(parseStatus(JSON.parse(JSON.stringify(raw))), raw);
  }
});

test("manifest endpoints match their local transport", () => {
  for (const endpoint of ["x.sock", "http://127.0.0.1:7434", "\\\\.\\pipe\\xmesh-node", "/x\0.sock"]) {
    assert.equal(parseManifest({ endpoint, transport: "unix", api_version: "1.0" }), undefined, endpoint);
  }
  for (const endpoint of ["/x.sock", "\\\\remote\\pipe\\xmesh-node", "\\\\.\\pipe\\", "\\\\.\\pipe\\xmesh\0node", "\\\\.\\pipe\\xmesh/node"]) {
    assert.equal(parseManifest({ endpoint, transport: "pipe", api_version: "1.0" }), undefined, endpoint);
  }
  assert.ok(parseManifest({ endpoint: "/tmp/xmesh.sock", transport: "unix", api_version: "1.0" }));
  assert.ok(parseManifest({ endpoint: "\\\\.\\pipe\\xmesh-node", transport: "pipe", api_version: "1.0" }));
});

test("status validates fields the shell and renderer consume", () => {
  const mutations: Array<(raw: Record<string, any>) => void> = [
    (raw) => { delete raw.network; },
    (raw) => { raw.network = null; },
    (raw) => { raw.network.mode = "unknown"; },
    (raw) => { raw.network.tags = [null]; },
    (raw) => { raw.control.boot_id = 5; },
    (raw) => { raw.control.capabilities = [null]; },
    (raw) => { raw.drain.draining = "false"; },
    (raw) => { raw.paths.config_file = {}; },
    (raw) => { raw.limits.pty_in_use = "20"; },
    (raw) => { raw.peers.online = -1; },
    (raw) => { raw.roles = [null]; },
    (raw) => { raw.shells = null; },
    (raw) => { raw.workspaces = [null]; },
    (raw) => { raw.backends = [null]; },
    (raw) => { raw.gateway = null; },
    (raw) => { raw.gateway = { listen: ":1", url: "http://localhost", auth: "pat", addresses: [null] }; },
    (raw) => { raw.gateway = { listen: ":1", url: "http://localhost", auth: "pat", access: {} }; },
    (raw) => { raw.ingress = { listen: "tunnel", issuer: {} }; },
    (raw) => { raw.ci = { bindings: [null] }; },
    (raw) => { raw.ci = { bindings: [{ id: "b", target: "o/r", scope: "repo", source: "token", labels: [], concurrency: 1, instances: [null] }] }; },
  ];
  for (const mutate of mutations) {
    const raw = structuredClone(statusFixture());
    mutate(raw);
    assert.throws(() => parseStatus(raw), (err: unknown) => err instanceof ControlRequestError && err.code === "internal");
  }
});

test("status retains compatibility with gateways preceding reach fields", () => {
  const raw = JSON.parse(JSON.stringify(statusFixture()));
  raw.gateway = { listen: "127.0.0.1:7433", url: "http://127.0.0.1:7433/mcp", auth: "pat" };
  assert.deepEqual(parseStatus(raw).gateway, { ...raw.gateway, access: [], hosts: [], addresses: [] });
});

test("parseStatus refuses garbage and tolerates a missing capability list", () => {
  assert.throws(() => parseStatus(null), /unexpected status/);
  assert.throws(() => parseStatus({ control: {} }), /unexpected status/);
  const raw = JSON.parse(JSON.stringify(statusFixture()));
  delete raw.control.capabilities;
  raw.warnings.push("not an object", { code: 1 });
  const s = parseStatus(raw);
  assert.deepEqual(s.control.capabilities, []);
  assert.deepEqual(s.warnings, []);
});

test("parsePeers reads the contract fixture and skips malformed entries", () => {
  assert.equal(parsePeers({ peers: peersFixture() }).length, 4);
  assert.deepEqual(parsePeers({ peers: [{ online: true }, "x", null] }), []);
  assert.deepEqual(parsePeers(null), []);
});

test("parseEvent accepts every contract event fixture and ignores unknown types", () => {
  for (const v of ["hello", "reset", "status", "peer", "call", "session-pty", "warning", "config", "lifecycle"]) {
    const ev = parseEvent(JSON.stringify(eventFixture(v)));
    assert.ok(ev, v);
  }
  assert.equal(parseEvent('{"type":"from_the_future","data":{}}'), undefined);
  assert.equal(parseEvent("not json"), undefined);
  assert.equal(parseEvent("[]"), undefined);
});

test("capabilities gate by membership", () => {
  assert.equal(hasCapability(["status", "events"], "events"), true);
  assert.equal(hasCapability(["status"], "drain"), false);
});

test("only warning and error severities degrade", () => {
  assert.equal(isDegrading(warningOf("restart_required", "info")), false);
  assert.equal(isDegrading(warningOf("peer_offline", "warning")), true);
  assert.equal(isDegrading(warningOf("policy_reload_failed", "error")), true);
  // Windows L2 refused for the account: the tray must not read "ok".
  assert.equal(isDegrading(warningOf("sandbox_unavailable", "warning")), true);
});

test("icon priority (RUL-18): offline > mismatch > draining > restart > degraded > ok", () => {
  const c = (patch: Parameters<typeof statusWith>[0]) => ({ kind: "connected" as const, status: statusWith(patch), peers: [] });
  const draining = { draining: true, mode: "keep" as const, since: "2026-10-16T15:30:00.000Z" };
  const bad = [warningOf("peer_offline", "warning")];
  assert.equal(deriveIcon({ kind: "offline" }), "offline");
  assert.equal(deriveIcon({ kind: "mismatch", kernelVersion: "", apiVersion: "2.0" }), "mismatch");
  assert.equal(deriveIcon(c({})), "ok");
  assert.equal(deriveIcon(c({ warnings: bad })), "degraded");
  assert.equal(deriveIcon(c({ restart_required: true, warnings: bad })), "restart");
  assert.equal(deriveIcon(c({ drain: draining, restart_required: true, warnings: bad })), "draining");
  // An info-level warning never makes it yellow.
  assert.equal(deriveIcon(c({ warnings: [warningOf("allow_dangerous", "info")] })), "ok");
  // A CI runner that needs the user turns it yellow; an idle or busy one does not.
  const broken = fixture<CiBinding>("CiBinding.broken");
  assert.equal(deriveIcon(c({ ci: { bindings: [broken] } })), "degraded");
  assert.equal(deriveIcon(c({ ci: { bindings: [fixture<CiBinding>("CiBinding.busy")] } })), "ok");
  // The contract's own degraded fixture follows the same order.
  assert.equal(deriveIcon({ kind: "connected", status: statusFixture("degraded-draining"), peers: [] }), "draining");
});
