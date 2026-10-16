import assert from "node:assert/strict";
import { test } from "node:test";
import {
  apiMajor,
  hasCapability,
  isDegrading,
  isSupportedApiVersion,
  parseEvent,
  parseManifest,
  parsePeers,
  parseStatus,
} from "../../src/shared/control";
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
  const m = parseManifest({ endpoint: "/x.sock", transport: "unix", api_version: "1.7", capabilities: ["status", "teleport", 3] });
  assert.deepEqual(m?.capabilities, ["status"]);
});

test("every contract StatusResponse fixture parses and keeps its fields", () => {
  for (const v of ["healthy", "degraded-draining"] as const) {
    const raw = statusFixture(v);
    assert.deepEqual(parseStatus(JSON.parse(JSON.stringify(raw))), raw);
  }
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
  // The contract's own degraded fixture follows the same order.
  assert.equal(deriveIcon({ kind: "connected", status: statusFixture("degraded-draining"), peers: [] }), "draining");
});
