import assert from "node:assert/strict";
import { test } from "node:test";
import { type EnvRow, blankRow, checkEnvRows, envChangeOf, namesAfter, storedRows } from "../../src/renderer/lib/mcp-env";

const row = (r: Partial<EnvRow>): EnvRow => ({ id: 0, name: "", value: "", stored: false, removed: false, ...r });

test("stored rows carry only names: the value is empty and the name is fixed", () => {
  const rows = storedRows(["API_KEY", "TOKEN"]);
  assert.deepEqual(
    rows.map((r) => [r.name, r.value, r.stored, r.removed]),
    [
      ["API_KEY", "", true, false],
      ["TOKEN", "", true, false],
    ],
  );
  assert.equal(new Set(rows.map((r) => r.id)).size, 2, "row ids are distinct");
  assert.ok(blankRow().id !== rows[0]!.id);
});

test("nothing typed means no change", () => {
  assert.equal(envChangeOf([]), undefined);
  assert.equal(envChangeOf(storedRows(["A", "B"])), undefined, "leaving a stored value empty keeps it");
  assert.equal(envChangeOf([blankRow()]), undefined, "an untouched blank row is dropped");
});

test("a typed value writes, a removed stored row deletes, and a removed new row does nothing", () => {
  const change = envChangeOf([
    row({ name: "NEW", value: "v1" }),
    row({ name: "REPLACED", value: "v2", stored: true }),
    row({ name: "GONE", stored: true, removed: true }),
    row({ name: "DRAFT", value: "x", removed: true }),
    row({ name: "KEPT", stored: true }),
  ]);
  assert.deepEqual(change, { set: { NEW: "v1", REPLACED: "v2" }, unset: ["GONE"] });
});

test("removing a stored variable and adding it back is one write, not a delete", () => {
  const change = envChangeOf([row({ name: "A", stored: true, removed: true }), row({ name: "A", value: "fresh" })]);
  assert.deepEqual(change, { set: { A: "fresh" }, unset: [] });
});

test("values are kept exactly as typed, and names are trimmed", () => {
  const change = envChangeOf([row({ name: " A ", value: "  spaced  " })]);
  assert.deepEqual(change, { set: { A: "  spaced  " }, unset: [] });
});

test("the names a save leaves behind", () => {
  assert.deepEqual(
    namesAfter([
      row({ name: "KEPT", stored: true }),
      row({ name: "GONE", stored: true, removed: true }),
      row({ name: "NEW", value: "v" }),
      row({ name: "EMPTY" }),
      blankRow(),
    ]),
    ["KEPT", "NEW"],
  );
});

test("row problems are reported once per kind", () => {
  const code = (rows: EnvRow[]) => checkEnvRows(rows).map((p) => p.code);
  assert.deepEqual(code([row({ name: "OK", value: "1" })]), []);
  assert.deepEqual(code([row({ name: "has space", value: "1" }), row({ name: "9x", value: "1" })]), ["env.name"]);
  assert.deepEqual(code([row({ name: "A" })]), ["env.value"]);
  assert.deepEqual(code([row({ name: "A", value: "1" }), row({ name: "A", value: "2" })]), ["env.duplicate"]);
  assert.deepEqual(code([row({ name: "A", stored: true }), row({ name: "A", value: "2" })]), ["env.duplicate"]);
  assert.deepEqual(code([row({ name: "A", value: "x".repeat(8193) })]), ["env.tooLong"]);
  assert.deepEqual(code([row({ name: "A", value: "a\0b" })]), ["env.tooLong"]);
  assert.deepEqual(code([row({ name: "A", value: "é".repeat(4097) })]), ["env.tooLong"], "the limit is in bytes");
  assert.deepEqual(code([row({ name: "A", value: "é".repeat(4096) })]), []);
});

test("a removed stored row and a blank row are not counted or checked", () => {
  assert.deepEqual(checkEnvRows([row({ name: "A", stored: true, removed: true }), blankRow()]), []);
  const many = Array.from({ length: 65 }, (_, i) => row({ name: `V${i}`, value: "1" }));
  assert.deepEqual(checkEnvRows(many).map((p) => p.code), ["env.tooMany"]);
  assert.deepEqual(checkEnvRows(many.slice(0, 64)), []);
});
