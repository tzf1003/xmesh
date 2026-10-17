import assert from "node:assert/strict";
import { test } from "node:test";
import { translator } from "../../src/shared/i18n";
import { EMPTY_SNAPSHOT, type Snapshot } from "../../src/shared/state";
import { buildMenu, findItem, menuIds } from "../../src/main/menu";
import type { StatusResponse } from "../../src/shared/control";
import { peersFixture, statusFixture, statusWith, warningOf } from "../../testserver/fixtures";

const t = translator("zh");

function connected(patch: Partial<StatusResponse> = {}, extra: Partial<Snapshot> = {}): Snapshot {
  return {
    ...EMPTY_SNAPSHOT,
    connection: { kind: "connected", status: statusWith(patch), peers: peersFixture() },
    ...extra,
  };
}

const withPaths = { paths: { config_file: "/c.yaml", state_dir: "/s", log_dir: "/l", audit_dir: "/a" } };

test("connected menu shows header, activity and every action", () => {
  const m = buildMenu(connected(withPaths), t);
  assert.equal(findItem(m, "header.node")?.label, "节点：win-dev-01");
  assert.equal(findItem(m, "header.state")?.label, "状态：正常");
  assert.equal(findItem(m, "header.network")?.label, "网络模式：tailnet");
  assert.equal(findItem(m, "header.peers")?.label, "在线对端：1");
  assert.equal(findItem(m, "activity.pty")?.label, "终端 1/20");
  assert.equal(findItem(m, "activity.exec")?.label, "执行 0/4");
  for (const id of [
    "action.openPanel",
    "action.drain",
    "action.copyGateway",
    "action.copyClaude",
    "action.copyCodex",
    "action.openConfig",
    "action.openLogs",
    "action.openAudit",
    "settings.loginItem",
    "settings.restartKernel",
    "action.quit",
    "action.stopKernel",
  ]) {
    assert.equal(findItem(m, id)?.enabled, true, id);
  }
  assert.equal(findItem(m, "action.resume"), undefined);
});

test("items the kernel gives no data for are disabled: no gateway role, no log or audit directory", () => {
  const base = statusFixture();
  const noGateway: Partial<StatusResponse> = { roles: ["agent"], paths: { ...base.paths, log_dir: null, audit_dir: null } };
  const status = { ...statusWith(noGateway) };
  delete status.gateway;
  const m = buildMenu({ ...EMPTY_SNAPSHOT, connection: { kind: "connected", status, peers: [] } }, t);
  for (const id of ["action.copyGateway", "action.copyClaude", "action.copyCodex", "action.openLogs", "action.openAudit"]) {
    assert.equal(findItem(m, id)?.enabled, false, id);
  }
  assert.equal(findItem(m, "action.openConfig")?.enabled, true);
});

test("an offline UI offers to start each node that has a config but no running kernel", () => {
  const offline = (stoppedNodes: string[]): Snapshot => ({ ...EMPTY_SNAPSHOT, stoppedNodes });
  const one = buildMenu(offline(["beta2-mac"]), t);
  assert.equal(findItem(one, "action.startKernel.beta2-mac")?.label, "启动内核");
  assert.deepEqual(findItem(one, "action.startKernel.beta2-mac")?.action, { type: "start-kernel", node: "beta2-mac" });
  const two = buildMenu(offline(["a", "b"]), t);
  assert.equal(findItem(two, "action.startKernel.a")?.label, "启动内核 a");
  assert.equal(findItem(two, "action.startKernel.b")?.label, "启动内核 b");
  assert.ok(!menuIds(buildMenu(offline([]), t)).some((id) => id.startsWith("action.startKernel")), "no config, nothing to start");
  assert.equal(findItem(buildMenu(connected({}, { stoppedNodes: ["beta2-mac"] }), t), "action.startKernel.beta2-mac"), undefined, "connected: no start entry");
});

test("draining swaps drain for resume; every warning is listed with its own entry", () => {
  const m = buildMenu(
    connected({
      drain: { draining: true, mode: "keep", since: "2026-10-16T15:30:00.000Z" },
      warnings: [warningOf("peer_offline", "warning", "对端 a 离线"), warningOf("peer_offline", "warning", "对端 b 离线")],
    }),
    t,
  );
  assert.equal(findItem(m, "action.drain"), undefined);
  assert.deepEqual(findItem(m, "action.resume")?.action, { type: "resume" });
  assert.equal(findItem(m, "warning.0.peer_offline")?.label, "对端 a 离线");
  assert.equal(findItem(m, "warning.1.peer_offline")?.label, "对端 b 离线");
  assert.equal(findItem(m, "header.state")?.label, "状态：排空中");
});

test("a kernel that does not advertise drain gets the entry disabled, not missing", () => {
  const base = statusFixture();
  const m = buildMenu(connected({ control: { ...base.control, capabilities: ["status", "events"] } }), t);
  assert.equal(findItem(m, "action.drain")?.enabled, false);
});

test("offline menu offers only the panel, settings and quit", () => {
  const ids = menuIds(buildMenu(EMPTY_SNAPSHOT, t)).filter((i) => !i.startsWith("sep."));
  assert.deepEqual(ids, ["header.state", "hint.offline", "action.openPanel", "settings", "settings.loginItem", "settings.restartKernel", "action.quit", "action.stopKernel"]);
  const m = buildMenu(EMPTY_SNAPSHOT, t);
  assert.equal(findItem(m, "settings.restartKernel")?.enabled, false);
  assert.equal(findItem(m, "action.stopKernel")?.enabled, false);
});

test("version mismatch keeps only the upgrade entry, settings and quit", () => {
  const m = buildMenu({ ...EMPTY_SNAPSHOT, connection: { kind: "mismatch", kernelVersion: "9.0.0", apiVersion: "2.0" } }, t);
  assert.equal(findItem(m, "action.openPanel"), undefined);
  assert.equal(findItem(m, "action.drain"), undefined);
  assert.deepEqual(findItem(m, "action.upgrade")?.action, { type: "open-external", key: "docs" });
  assert.match(findItem(m, "hint.mismatch")?.label ?? "", /9\.0\.0/);
});

test("a second instance adds a switcher with the selection checked", () => {
  const snap = connected({}, {
    instances: [
      { id: "a", label: "laptop" },
      { id: "b", label: "server" },
    ],
    selectedInstance: "b",
  });
  const m = buildMenu(snap, t);
  assert.equal(findItem(m, "instance.a")?.checked, false);
  assert.equal(findItem(m, "instance.b")?.checked, true);
  assert.deepEqual(findItem(m, "instance.a")?.action, { type: "select-instance", id: "a" });
  assert.equal(findItem(buildMenu(connected(), t), "instances"), undefined);
});

test("the login item checkbox toggles to the opposite value", () => {
  const on = buildMenu(connected({}, { settings: { loginItem: true, locale: "zh" } }), t);
  assert.equal(findItem(on, "settings.loginItem")?.checked, true);
  assert.deepEqual(findItem(on, "settings.loginItem")?.action, { type: "set-login-item", enabled: false });
});

test("stopping the kernel is separate from quitting the tray", () => {
  const m = buildMenu(connected(), t);
  assert.deepEqual(findItem(m, "action.quit")?.action, { type: "quit" });
  assert.deepEqual(findItem(m, "action.stopKernel")?.action, { type: "stop-kernel" });
});
