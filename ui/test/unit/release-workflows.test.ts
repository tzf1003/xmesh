import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { test } from "node:test";
import { parse } from "yaml";

test("native release evidence runs for ordinary master commits and every release tag", () => {
  const workflow = (name: string) => parse(fs.readFileSync(path.resolve(__dirname, "../../../../.github/workflows", name), "utf8"));
  const native = workflow("computer.yml").on.push;
  const release = workflow("release.yml").on.push;
  assert.ok(native.branches.includes("master"));
  assert.equal(native.paths, undefined);
  assert.equal(native["paths-ignore"], undefined);
  assert.equal(native["tags-ignore"], undefined);
  for (const tag of release.tags) assert.ok(native.tags.includes(tag), `Native evidence missing for ${tag}`);
});
