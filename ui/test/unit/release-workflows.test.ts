import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { test } from "node:test";
import { parse } from "yaml";

test("every release tag runs the full CI entrypoint and its native suite", () => {
  const workflow = (name: string) => parse(fs.readFileSync(path.resolve(__dirname, "../../../../.github/workflows", name), "utf8"));
  const ci = workflow("go.yml");
  const native = workflow("computer.yml");
  const release = workflow("release.yml").on.push;
  assert.deepEqual(ci.on.push.branches, ["master"]);
  assert.equal(ci.on.push.paths, undefined);
  assert.equal(ci.on.push["paths-ignore"], undefined);
  assert.equal(ci.on.push["tags-ignore"], undefined);
  for (const tag of release.tags) assert.ok(ci.on.push.tags.includes(tag), `Full CI missing for ${tag}`);
  assert.ok(Object.hasOwn(native.on, "workflow_call"));
  assert.equal(ci.jobs.computer.uses, "./.github/workflows/computer.yml");
  assert.ok(ci.jobs.ci.needs.includes("computer"));
});

test("component workflows share the CI trigger and diagnostics are opt-in", () => {
  const workflow = (name: string) => parse(fs.readFileSync(path.resolve(__dirname, "../../../../.github/workflows", name), "utf8"));
  for (const name of ["ui.yml", "ui-mcp.yml", "headscale.yml", "computer.yml"]) {
    assert.deepEqual(Object.keys(workflow(name).on), ["workflow_call"]);
  }
  assert.deepEqual(Object.keys(workflow("windows-diagnostics.yml").on), ["workflow_dispatch"]);
  const packaging = workflow("computer-release.yml");
  assert.ok(Object.hasOwn(packaging.on, "workflow_call"));
  assert.ok(Object.hasOwn(packaging.on, "workflow_dispatch"));
  assert.equal(packaging.on.pull_request, undefined);
});
