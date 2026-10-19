import assert from "node:assert/strict";
import { test } from "node:test";
import { buildMeshArgs, meshError } from "../../src/main/mesh-service";
import { normalizeResult, parseMeshRequest } from "../../src/shared/mesh";

test("policy documents validate each IPC field and keep policy source off argv", () => {
  const edit = { op: "policy.document", edit: { text: "{}", region: { id: 900, text: '{"RegionID":900}' } } };
  const parsed = parseMeshRequest(edit);
  assert.ok(parsed.ok);
  assert.deepEqual(buildMeshArgs(parsed.value, undefined, "/private/credential"), ["mesh", "policy", "document", "--json"]);
  const publish = parseMeshRequest({ op: "policy.publish", text: "{ /* source */ }", apply: true, expectVersion: '"etag"' });
  assert.ok(publish.ok);
  const argv = buildMeshArgs(publish.value, undefined, "/private/credential");
  assert.ok(argv.includes('--expect-version="etag"'));
  assert.ok(argv.includes("--apply"));
  assert.ok(!argv.join(" ").includes("source"));
  for (const bad of [
    { ...edit, extra: true },
    { op: "policy.document", edit: { text: "{}", region: { id: 900, text: "{}", extra: 1 } } },
    { op: "policy.document", edit: { text: "{}", removeID: -1 } },
    { op: "policy.document", edit: { text: "{}", omitDefault: "true" } },
    { op: "policy.document", edit: { text: "{}", template: "personal" } },
    { op: "policy.document", edit: { text: "" } },
    { op: "policy.document", edit: { text: "x".repeat(262145) } },
    { op: "policy.publish", text: "{}", apply: true, expectVersion: "" },
    { op: "policy.publish", text: "{}", apply: "yes", expectVersion: "v1" },
  ]) assert.equal(parseMeshRequest(bad).ok, false);
});

test("document IPC rejects malformed responses rather than displaying an empty relay list", () => {
  for (const bad of [null, {}, { text: "{}", omitDefault: false, regions: null }, { text: "{}", omitDefault: false, regions: [{ id: "900", code: "a", name: "A", hosts: [], text: "{}" }] }]) {
    assert.throws(() => normalizeResult("policy.document", bad));
  }
  const document = { text: "{}", omitDefault: true, regions: [{ id: 900, code: "a", name: "A", hosts: ["192.0.2.8"], text: "{}" }] };
  assert.deepEqual(normalizeResult("policy.document", document), document);
  assert.equal(meshError({ code: 1, stdout: "", stderr: "meshadmin: policy changed since it was read: 云端策略已变化" }).code, "conflict");
});
