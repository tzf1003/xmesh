import assert from "node:assert/strict";
import { test } from "node:test";
import { applyView, hasNews, settingName, waitingSettings } from "../../src/renderer/lib/apply-state";
import { outcomeOf, type SaveOutcome } from "../../src/renderer/lib/reload";
import { statusWith, warningOf } from "../../testserver/fixtures";

const clean: SaveOutcome = { restart: false, paths: [], applyErrors: [] };

test("a save with nothing left over is applied", () => {
  const v = applyView(clean, statusWith());
  assert.equal(v.applied, true);
  assert.equal(v.restart, false);
  assert.deepEqual(v.problems, []);
  assert.equal(hasNews(v), true);
});

test("without a save and with a quiet kernel the bar has nothing to say", () => {
  const v = applyView(undefined, statusWith());
  assert.equal(v.applied, false);
  assert.equal(hasNews(v), false);
  assert.equal(hasNews(applyView(undefined, undefined)), false);
});

test("a restart pending on the kernel shows on any page, with no save to go with it", () => {
  const v = applyView(undefined, statusWith({ restart_required: true }));
  assert.equal(v.restart, true);
  assert.equal(v.applied, false);
  assert.deepEqual(v.waiting, []);
  assert.equal(hasNews(v), true);
});

test("a save that needs a restart is not applied, and names the settings that wait", () => {
  const v = applyView({ restart: true, paths: ["/node/name", "/node/roles"], applyErrors: [] }, statusWith());
  assert.equal(v.restart, true);
  assert.equal(v.applied, false);
  assert.deepEqual(v.waiting, ["节点名称", "Agent 角色"]);
});

test("settings the panel has no name for are shown as the pointer, and repeats collapse", () => {
  assert.equal(settingName("/something/new"), "/something/new");
  assert.equal(settingName("/mesh/mode"), "组网方式");
  assert.equal(settingName("/meshy"), "/meshy");
  assert.deepEqual(waitingSettings(["/mesh/mode", "/mesh/lan/port", "/audit"]), ["组网方式", "审计日志"]);
});

test("a setting the kernel could not take is not applied and carries the kernel's reason", () => {
  const v = applyView({ ...clean, applyErrors: ["address already in use"] }, statusWith());
  assert.equal(v.applied, false);
  assert.deepEqual(v.problems, ["address already in use"]);
  assert.equal(hasNews(v), true);
});

test("a file the kernel cannot load is not applied", () => {
  const v = applyView({ ...clean, loadError: "yaml: 缩进错误" }, statusWith());
  assert.equal(v.applied, false);
  assert.equal(v.loadError, "yaml: 缩进错误");
});

test("without a save, the settings the kernel reports as failing are the problems; other warnings are not", () => {
  const status = statusWith({ warnings: [warningOf("policy_reload_failed", "error", "网关没能改到 :8080"), warningOf("peer_offline")] });
  assert.deepEqual(applyView(undefined, status).problems, ["网关没能改到 :8080"]);
});

test("a save settles the failures itself: an old warning in the status does not outlive a retry that worked", () => {
  const status = statusWith({ warnings: [warningOf("policy_reload_failed", "error", "网关没能改到 :8080")] });
  const v = applyView(clean, status);
  assert.deepEqual(v.problems, []);
  assert.equal(v.applied, true);
});

test("a restart can come from the status while the save itself is clean", () => {
  const v = applyView(clean, statusWith({ restart_required: true }));
  assert.equal(v.restart, true);
  assert.equal(v.applied, false);
});

test("a gateway update answer becomes the same outcome as a config save", () => {
  assert.deepEqual(outcomeOf({ restart_required: false, apply_errors: [{ message: "a" }, { message: "b" }] }), { restart: false, paths: [], applyErrors: ["a", "b"] });
  assert.deepEqual(outcomeOf({ restart_required: true }), { restart: true, paths: [], applyErrors: [] });
});
