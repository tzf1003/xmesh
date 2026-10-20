import assert from "node:assert/strict";
import { test } from "node:test";
import {
  CHANNELS,
  COPY_KINDS,
  PANEL_API_METHODS,
  PATH_KINDS,
  parseBackendEnv,
  parseBoolean,
  parseCopyKind,
  parseExternalKey,
  parseInstanceId,
  parsePathKind,
} from "../../src/shared/ipc";

test("validators accept the whitelist and nothing else", () => {
  for (const k of COPY_KINDS) assert.equal(parseCopyKind(k), k);
  for (const k of PATH_KINDS) assert.equal(parsePathKind(k), k);
  for (const bad of ["", "rm -rf", "constructor", "__proto__", 1, null, undefined, {}, ["gatewayUrl"], { toString: () => "gatewayUrl" }]) {
    assert.equal(parseCopyKind(bad), undefined);
    assert.equal(parsePathKind(bad), undefined);
  }
});

test("external link keys cannot reach prototype properties", () => {
  assert.equal(parseExternalKey("docs"), "docs");
  for (const bad of ["toString", "__proto__", "hasOwnProperty", "https://evil.example/", 3, null]) {
    assert.equal(parseExternalKey(bad), undefined);
  }
});

test("instance ids are plain directory names", () => {
  assert.equal(parseInstanceId("node-1"), "node-1");
  assert.equal(parseInstanceId("My.Node_2"), "My.Node_2");
  for (const bad of ["", ".", "..", "../x", "a/b", "a\\b", "a b", "x".repeat(129), 1, null]) {
    assert.equal(parseInstanceId(bad), undefined, String(bad));
  }
});

test("booleans must be booleans", () => {
  assert.equal(parseBoolean(true), true);
  assert.equal(parseBoolean(false), false);
  for (const bad of ["true", 1, 0, null, undefined]) assert.equal(parseBoolean(bad), undefined);
});

test("every panel method except the two event subscriptions has an IPC channel, and channel names are unique", () => {
  const values = Object.values(CHANNELS);
  assert.equal(new Set(values).size, values.length);
  assert.equal(PANEL_API_METHODS.length - 3, values.length);
});

test("a backend env request is a backend name with variables to write and to remove, bounded like the contract", () => {
  const ok = { name: "github", set: { GITHUB_TOKEN: "t", DEBUG: "" }, unset: ["OLD"] };
  assert.deepEqual(parseBackendEnv(ok), ok);
  assert.deepEqual(parseBackendEnv({ name: "a", set: {}, unset: ["X"] }), { name: "a", set: {}, unset: ["X"] });
  assert.deepEqual(parseBackendEnv({ name: "a", set: { X: "é".repeat(4096) }, unset: [] })?.set.X?.length, 4096, "8192 bytes is the limit");

  const many = (n: number) => Object.fromEntries(Array.from({ length: n }, (_, i) => [`V${i}`, "1"]));
  assert.ok(parseBackendEnv({ name: "a", set: many(64), unset: [] }));
  const bad: unknown[] = [
    null, "x", [], {},
    { name: "a", set: {} , unset: [] },
    { name: "../x", set: { A: "1" }, unset: [] },
    { name: "a", set: { A: "1" } },
    { name: "a", unset: ["A"] },
    { name: "a", set: { A: "1" }, unset: [], extra: 1 },
    { name: "a", set: { "1A": "1" }, unset: [] },
    { name: "a", set: { "A B": "1" }, unset: [] },
    { name: "a", set: { A: 1 }, unset: [] },
    { name: "a", set: { A: "a\0b" }, unset: [] },
    { name: "a", set: { A: "x".repeat(8193) }, unset: [] },
    { name: "a", set: { A: "é".repeat(4097) }, unset: [] },
    { name: "a", set: { A: "1" }, unset: ["A"] },
    { name: "a", set: {}, unset: ["B", "B"] },
    { name: "a", set: {}, unset: ["b c"] },
    { name: "a", set: {}, unset: [1] },
    { name: "a", set: many(65), unset: [] },
    { name: "a", set: {}, unset: Array.from({ length: 65 }, (_, i) => `V${i}`) },
  ];
  for (const b of bad) assert.equal(parseBackendEnv(b), undefined, JSON.stringify(b)?.slice(0, 80));
});
