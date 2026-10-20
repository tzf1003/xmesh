import assert from "node:assert/strict";
import { test } from "node:test";
import type { CiBinding, CiInstance } from "../../src/shared/control";
import type { MeshDeviceReport } from "../../src/shared/mesh";
import { bindingNeedsAttention, deviceNeedsAttention, filterBindings, filterMeshDevices } from "../../src/renderer/pages/module-model";

const instance = (patch: Partial<CiInstance> = {}): CiInstance => ({ index: 1, name: "runner", state: "idle", restarts: 0, since: "2026-10-20T10:00:00Z", ...patch });
const binding = (patch: Partial<CiBinding> = {}): CiBinding => ({ id: "build", scope: "repo", target: "team/build", source: "token", concurrency: 1, labels: [], instances: [instance()], paused: false, removing: false, dir: "/runners/build", ...patch });
const device = (patch: Partial<MeshDeviceReport> = {}): MeshDeviceReport => ({ device: { id: "node-a", name: "build-linux", hostname: "build.internal", addresses: ["100.101.0.2"], tags: ["tag:device"], online: true, authorized: true }, roles: ["agent"], issues: [], ...patch });

test("repository search and status filters combine, including active jobs during shutdown", () => {
  const bindings = [binding({ id: "a", labels: ["GPU"], instances: [instance({ state: "stopping", job: { name: "test", started_at: "2026-10-20T10:00:00Z" } })] }), binding({ id: "b", target: "team/docs", paused: true }), binding({ id: "c", target: "team/test", instances: [instance({ state: "broken" })] })];
  assert.deepEqual(filterBindings(bindings, " gpu ", "busy").map((item) => item.id), ["a"]);
  assert.deepEqual(filterBindings(bindings, "TEAM", "paused").map((item) => item.id), ["b"]);
  assert.deepEqual(filterBindings(bindings, "team", "attention").map((item) => item.id), ["c"]);
  assert.deepEqual(filterBindings(bindings, "docs", "busy"), []);
});

test("backoff problems remain actionable while clean startup does not need repair", () => {
  assert.equal(bindingNeedsAttention(binding({ instances: [instance({ state: "backoff", problem: { code: "download_failed", message: "retry" } })] })), true);
  assert.equal(bindingNeedsAttention(binding({ instances: [instance({ state: "starting" })] })), false);
});

test("device search covers addresses, tags, hostnames and roles and respects online filtering", () => {
  const first = device();
  const second = device({ device: { ...first.device, id: "node-b", name: "office", online: false, addresses: ["100.101.0.3"] }, roles: ["gateway"] });
  assert.deepEqual(filterMeshDevices([first, second], "100.101.0.3", "offline"), [second]);
  assert.deepEqual(filterMeshDevices([first, second], "GATEWAY", "online"), []);
  assert.deepEqual(filterMeshDevices([first], "BUILD.INTERNAL", "all"), [first]);
  assert.deepEqual(filterMeshDevices([first], "tag:device", "online"), [first]);
});

test("device attention includes missing authorization and actionable issues", () => {
  const healthy = device({ issues: [{ severity: "info", code: "info", message: "Known device" }] });
  const unauthorized = device({ device: { ...healthy.device, authorized: false } });
  const warning = device({ issues: [{ severity: "warn", code: "tags", message: "Review tags" }] });
  assert.equal(deviceNeedsAttention(healthy), false);
  assert.equal(deviceNeedsAttention(unauthorized), true);
  assert.deepEqual(filterMeshDevices([healthy, unauthorized, warning], "", "attention"), [unauthorized, warning]);
});
