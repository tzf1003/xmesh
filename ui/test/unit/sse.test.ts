import assert from "node:assert/strict";
import { test } from "node:test";
import { SseParser } from "../../src/shared/sse";

test("parses events split across arbitrary chunks", () => {
  const p = new SseParser();
  const text = "id: 1\nevent: status\ndata: {\"a\":1}\n\nid: 2\nevent: peer\ndata: x\n\n";
  const got = [];
  for (const ch of text) got.push(...p.push(ch));
  assert.deepEqual(got, [
    { id: "1", event: "status", data: '{"a":1}' },
    { id: "2", event: "peer", data: "x" },
  ]);
});

test("joins multi-line data, accepts CRLF and ignores comments", () => {
  const p = new SseParser();
  const got = p.push(": keepalive\r\nevent: warning\r\ndata: a\r\ndata: b\r\n\r\n");
  assert.deepEqual(got, [{ event: "warning", data: "a\nb" }]);
});

test("a CR at the end of a chunk waits for a possible LF", () => {
  const p = new SseParser();
  assert.deepEqual(p.push("data: 1\r"), []);
  assert.deepEqual(p.push("\n\r\n"), [{ event: "message", data: "1" }]);
});

test("the last id carries over to events that do not set one", () => {
  const p = new SseParser();
  const got = p.push("id: 7\ndata: a\n\ndata: b\n\n");
  assert.deepEqual(
    got.map((m) => m.id),
    ["7", "7"],
  );
});

test("an event without data is not dispatched", () => {
  const p = new SseParser();
  assert.deepEqual(p.push("event: status\n\n"), []);
  assert.deepEqual(p.push("data: ok\n\n"), [{ event: "message", data: "ok" }]);
});
