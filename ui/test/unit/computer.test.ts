import { test } from "node:test";
import assert from "node:assert/strict";
import { parseComputerRequest } from "../../src/shared/computer";
import { parsePatCreate } from "../../src/shared/ipc";

test("desktop IPC allows only the fixed local operations", () => {
  assert.deepEqual(parseComputerRequest({ op: "pause", paused: true }), { op: "pause", paused: true });
  assert.deepEqual(parseComputerRequest({ op: "get" }), { op: "get" });
  for (const request of [null, [], { op: "execute", cmd: "x" }, { op: "get", path: "/tmp" }, { op: "pause", paused: "true" }, { op: "revoke", session_id: "../x" }]) assert.equal(parseComputerRequest(request), undefined);
  assert.deepEqual(parseComputerRequest({ op: "revoke", session_id: "cs-12345678" }), { op: "revoke", session_id: "cs-12345678" });
});

test("desktop PAT scopes preserve the device and capability boundary", () => {
  assert.ok(parsePatCreate({ name: "desktop", scopes: ["computer:observe:mac", "computer:control:win"] }));
  assert.equal(parsePatCreate({ name: "desktop", scopes: ["computer:*:*"] }), undefined);
});
