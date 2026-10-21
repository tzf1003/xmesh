import assert from "node:assert/strict";
import { test } from "node:test";
import { parse } from "yaml";
import { formatConfigSource } from "../../src/shared/config-yaml";

test("configuration source uses block YAML for JSON and flow collections", () => {
  const input = JSON.stringify({ node: { name: "desktop", roles: ["agent", "gateway"] }, agent: { authz: { "tag:peer": { tools: ["*"], backends: [] } } } });
  const output = formatConfigSource(input);
  assert.deepEqual(parse(output), JSON.parse(input));
  assert.match(output, /  "name": "desktop"/);
  assert.doesNotMatch(output, /[{}]/);
  assert.equal(formatConfigSource(output), output);
});

test("formatting preserves comments, anchors, scalar values and large integers", () => {
  const input = "# node settings\nnode: {name: desktop, roles: [agent]} # keep this\nagent:\n  cwd_policy: &policy {allow: [C:\\work], deny: []}\n  quota: {calls_per_hour: 9007199254740993}\ncopy: *policy\n";
  const output = formatConfigSource(input);
  assert.match(output, /# node settings/);
  assert.match(output, /# keep this/);
  assert.match(output, /&policy/);
  assert.match(output, /9007199254740993/);
  assert.deepEqual(parse(output, { intAsBigInt: true }), parse(input, { intAsBigInt: true }));
});

test("invalid or multi-document configuration remains available for repair", () => {
  for (const input of ["node: [", "node: a\nnode: b\n", "node: a\n---\nnode: b\n"]) assert.equal(formatConfigSource(input), input);
});
