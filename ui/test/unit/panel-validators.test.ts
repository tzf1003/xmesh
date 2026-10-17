import assert from "node:assert/strict";
import { test } from "node:test";
import {
  AUDIT_EVENTS,
  CONFIG_MAX_BYTES,
  parseAuditQuery,
  parseClientId,
  parseConfigSave,
  parseDrainMode,
  parsePairImport,
  parsePassword,
  parsePatCreate,
  parsePickKind,
  parseScope,
} from "../../src/shared/ipc";

const ETAG = `sha256-${"a".repeat(64)}`;
const FP = "ab".repeat(32);
const PEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n";
const pair = { name: "peer-1", fingerprint: FP, certificate_pem: PEM, tags: ["tag:peer"] };

test("drain mode is keep or kill", () => {
  assert.equal(parseDrainMode("keep"), "keep");
  assert.equal(parseDrainMode("kill"), "kill");
  for (const v of ["", "KILL", undefined, 1, null, {}]) assert.equal(parseDrainMode(v), undefined);
});

test("scopes follow the contract grammar", () => {
  for (const s of ["devices:read", "exec:*", "pty:shell", "fs:read:docs", "fs:*", "sys:power", "backend:git:*"]) {
    assert.equal(parseScope(s), s, s);
  }
  for (const s of ["", "*", "exec:", "exec:a b", "fs:exec:x", "devices:write", "backend:git", "exec:*;rm", 5]) {
    assert.equal(parseScope(s), undefined, String(s));
  }
});

test("config save: strict keys, a real etag, a boolean dry-run and the 1 MiB byte limit", () => {
  const ok = { content: "node: {}\n", etag: ETAG, dryRun: true };
  assert.deepEqual(parseConfigSave(ok), ok);
  assert.equal(parseConfigSave({ ...ok, extra: 1 }), undefined);
  assert.equal(parseConfigSave({ ...ok, etag: "W/\"x\"" }), undefined);
  assert.equal(parseConfigSave({ ...ok, etag: `"${ETAG}"` }), undefined);
  assert.equal(parseConfigSave({ ...ok, dryRun: "true" }), undefined);
  assert.equal(parseConfigSave({ ...ok, content: 3 }), undefined);
  assert.ok(parseConfigSave({ ...ok, content: "a".repeat(CONFIG_MAX_BYTES) }));
  assert.equal(parseConfigSave({ ...ok, content: "a".repeat(CONFIG_MAX_BYTES + 1) }), undefined);
  // 3-byte characters: the count is in bytes, not code units.
  assert.equal(parseConfigSave({ ...ok, content: "配".repeat(Math.floor(CONFIG_MAX_BYTES / 3) + 1) }), undefined);
  assert.equal(parseConfigSave(null), undefined);
  assert.equal(parseConfigSave([]), undefined);
});

test("PAT creation needs a node-style name and 1..64 distinct valid scopes", () => {
  assert.deepEqual(parsePatCreate({ name: "ci", scopes: ["devices:read"] }), { name: "ci", scopes: ["devices:read"] });
  for (const bad of [
    { name: "ci", scopes: [] },
    { name: "ci", scopes: ["devices:read", "devices:read"] },
    { name: "ci", scopes: ["root"] },
    { name: "-ci", scopes: ["devices:read"] },
    { name: "ci/../x", scopes: ["devices:read"] },
    { name: "ci", scopes: ["devices:read"], admin: true },
    { name: "ci" },
    "ci",
  ]) {
    assert.equal(parsePatCreate(bad), undefined, JSON.stringify(bad));
  }
  const many = Array.from({ length: 65 }, (_, i) => `exec:t${i}`);
  assert.equal(parsePatCreate({ name: "ci", scopes: many }), undefined);
});

test("client ids are printable ASCII without spaces", () => {
  assert.equal(parseClientId("client_1.abc"), "client_1.abc");
  for (const v of ["", "a b", "a\nb", "é", "x".repeat(257), 3]) assert.equal(parseClientId(v), undefined);
});

test("ingress password length is counted in characters, 12 to 256", () => {
  assert.equal(parsePassword("a".repeat(12)), "a".repeat(12));
  assert.equal(parsePassword("a".repeat(11)), undefined);
  assert.equal(parsePassword("a".repeat(256)), "a".repeat(256));
  assert.equal(parsePassword("a".repeat(257)), undefined);
  assert.ok(parsePassword("密码".repeat(6)), "12 characters, 36 bytes");
  assert.equal(parsePassword(12), undefined);
  assert.equal(parsePassword(" " + "a".repeat(12)), undefined, "the kernel refuses edge whitespace");
  assert.equal(parsePassword("a".repeat(12) + "\n"), undefined);
  assert.equal(parsePassword("a b c d e f g h"), "a b c d e f g h", "inner spaces are fine");
});

test("pair import requires the typed fingerprint and a well-formed document", () => {
  const ok = { pair, fingerprint: FP };
  assert.deepEqual(parsePairImport(ok), ok);
  assert.ok(parsePairImport({ ...ok, fingerprint: FP.match(/../g)!.join(":") }));
  assert.deepEqual(parsePairImport({ ...ok, tags: ["tag:device"] })?.tags, ["tag:device"]);
  for (const bad of [
    { pair },
    { ...ok, fingerprint: "abcd" },
    { ...ok, tags: [] },
    { ...ok, tags: ["tag:admin"] },
    { ...ok, tags: ["tag:peer", "tag:peer"] },
    { ...ok, extra: 1 },
    { pair: { ...pair, certificate_pem: "nope" }, fingerprint: FP },
    { pair: { ...pair, name: "bad name" }, fingerprint: FP },
    { pair: { ...pair, tags: ["peer"] }, fingerprint: FP },
    { pair: { ...pair, extra: 1 }, fingerprint: FP },
    { pair: { ...pair, certificate_pem: PEM + "x".repeat(16 * 1024) }, fingerprint: FP },
  ]) {
    assert.equal(parsePairImport(bad), undefined, JSON.stringify(bad).slice(0, 80));
  }
});

test("audit queries accept only the documented filters", () => {
  const q = {
    since: "2026-10-16T01:02:03Z",
    until: "2026-10-16T01:02:03.123Z",
    limit: "100",
    cursor: "abc",
    event: "tool_call",
    tool: "git.status",
    device: "dev-1",
    caller: "user@example",
    status: "error",
  };
  assert.deepEqual(parseAuditQuery(q), q);
  assert.deepEqual(parseAuditQuery({}), {});
  for (const bad of [
    { since: "yesterday" },
    { since: "2026-10-16T01:02:03+08:00" },
    { limit: 100 },
    { limit: "0" },
    { limit: "1001" },
    { event: "rm_rf" },
    { device: "a b" },
    { tool: "" },
    { tool: "a\nb" },
    { status: "maybe" },
    { sort: "asc" },
  ]) {
    assert.equal(parseAuditQuery(bad), undefined, JSON.stringify(bad));
  }
  assert.equal(parseAuditQuery(null), undefined);
});

test("pick kinds are a closed set", () => {
  assert.ok(parsePickKind("workspace"));
  assert.equal(parsePickKind("/etc"), undefined);
});

test("the audit event filter lists every event type of the contract", async () => {
  const fs = await import("node:fs");
  const path = await import("node:path");
  const src = fs.readFileSync(path.join(__dirname, "..", "..", "..", "src", "shared", "generated", "control.ts"), "utf8");
  const block = /export interface AuditEvent \{[\s\S]*?event:([\s\S]*?);/.exec(src)?.[1] ?? "";
  const types = [...block.matchAll(/"([a-z_]+)"/g)].map((m) => m[1]!);
  assert.ok(types.length > 5, "found the event enum");
  assert.deepEqual([...AUDIT_EVENTS].sort(), types.sort());
});
