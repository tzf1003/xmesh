import assert from "node:assert/strict";
import { test } from "node:test";
import {
  CHANNELS,
  COPY_KINDS,
  PANEL_API_METHODS,
  PATH_KINDS,
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
  assert.equal(PANEL_API_METHODS.length - 2, values.length);
});
