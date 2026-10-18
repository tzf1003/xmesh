import assert from "node:assert/strict";
import { test } from "node:test";
import { CONFIG_PATCH_MAX_EDITS, CONFIG_MAX_BYTES, isJsonValue, parseConfigPatch } from "../../src/shared/ipc";

const ETAG = `sha256-${"a".repeat(64)}`;
const edit = { path: "/agent/backends/git-tools", value: { transport: "stdio", command: ["git", "mcp-server"], policy: "shared" } };
const ok = { edits: [edit, { path: "/agent/disabled_tools", delete: true }], etag: ETAG, dryRun: false };

test("a patch with set and delete edits passes unchanged", () => {
  assert.deepEqual(parseConfigPatch(ok), ok);
  assert.deepEqual(parseConfigPatch({ ...ok, dryRun: true }), { ...ok, dryRun: true });
});

test("values may be any JSON: arrays, strings, null and nested maps", () => {
  for (const value of [["exec.run"], "x", null, false, 0, { a: { b: [1, { c: null }] } }, []]) {
    assert.ok(parseConfigPatch({ edits: [{ path: "/agent/x", value }], etag: ETAG, dryRun: false }), JSON.stringify(value));
  }
});

test("each edit is a pointer with a segment, then either a value or delete, never both", () => {
  const wrap = (e: unknown) => ({ edits: [e], etag: ETAG, dryRun: false });
  assert.ok(parseConfigPatch(wrap({ path: "/agent", value: 1 })));
  assert.ok(parseConfigPatch(wrap({ path: "/agent", delete: true })));
  for (const bad of [
    { path: "/x", delete: true, value: 1 },
    { path: "/x" },
    { path: "/x", delete: false },
    { path: "x", value: 1 },
    { path: "/", value: 1 },
    { path: "", value: 1 },
    { path: "/a\nb", value: 1 },
    { path: "/a\u0000", value: 1 },
    { path: "/x", value: undefined },
    { path: "/x", value: Number.NaN },
    { path: "/x", value: Number.POSITIVE_INFINITY },
    { path: "/x", value: { a: undefined } },
    { path: "/x", value: 1, extra: true },
    { path: "/x", value: 1, delete: "yes" },
    { path: "/x".padEnd(513, "a"), value: 1 },
    null,
    "edit",
  ]) {
    assert.equal(parseConfigPatch(wrap(bad)), undefined, JSON.stringify(bad));
  }
});

test("the edit count is 1 to 64", () => {
  assert.equal(parseConfigPatch({ edits: [], etag: ETAG, dryRun: false }), undefined);
  const full = Array.from({ length: CONFIG_PATCH_MAX_EDITS }, (_, i) => ({ path: `/agent/x${i}`, value: i }));
  assert.ok(parseConfigPatch({ edits: full, etag: ETAG, dryRun: false }));
  assert.equal(parseConfigPatch({ edits: [...full, { path: "/agent/y", value: 1 }], etag: ETAG, dryRun: false }), undefined);
  assert.equal(parseConfigPatch({ edits: "nope", etag: ETAG, dryRun: false }), undefined);
});

test("the request needs a real etag, a boolean dryRun and no stray keys", () => {
  for (const bad of [
    { ...ok, etag: `"${ETAG}"` },
    { ...ok, etag: "sha256-xyz" },
    { ...ok, dryRun: "false" },
    { ...ok, dryRun: undefined },
    { ...ok, extra: 1 },
    { edits: ok.edits, dryRun: false },
    [],
    null,
  ]) {
    assert.equal(parseConfigPatch(bad), undefined, JSON.stringify(bad));
  }
});

test("the whole request is limited to the same 1 MiB as a whole-file save", () => {
  const big = { edits: [{ path: "/agent/blob", value: "a".repeat(CONFIG_MAX_BYTES) }], etag: ETAG, dryRun: false };
  assert.equal(parseConfigPatch(big), undefined);
  // Three-byte characters: the limit counts bytes, not UTF-16 units.
  const cjk = { edits: [{ path: "/agent/blob", value: "配".repeat(Math.floor(CONFIG_MAX_BYTES / 3) + 1) }], etag: ETAG, dryRun: false };
  assert.equal(parseConfigPatch(cjk), undefined);
});

test("JSON values nest at most 64 levels and hold only finite numbers", () => {
  let deep: unknown = 1;
  for (let i = 0; i < 64; i++) deep = [deep];
  assert.equal(isJsonValue(deep), true, "64 levels is allowed");
  assert.equal(isJsonValue([deep]), false, "65 levels is not");
  assert.equal(isJsonValue(Number.NaN), false);
  assert.equal(isJsonValue(undefined), false);
  assert.equal(isJsonValue(() => 1), false);
  assert.equal(isJsonValue(new Date()), false);
});
