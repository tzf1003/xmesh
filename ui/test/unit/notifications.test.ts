import assert from "node:assert/strict";
import { test } from "node:test";
import { diffNotices, noticeText } from "../../src/main/notifications";
import { translator } from "../../src/shared/i18n";
import { EMPTY_SNAPSHOT, type Snapshot } from "../../src/shared/state";
import type { StatusResponse } from "../../src/shared/control";
import { peerNamed, peersFixture, statusWith, warningOf } from "../../testserver/fixtures";

const snap = (status: Partial<StatusResponse> = {}, peers = peersFixture()): Snapshot => ({
  ...EMPTY_SNAPSHOT,
  connection: { kind: "connected", status: statusWith(status), peers },
});
const t = translator("zh");
const texts = (a: Snapshot, b: Snapshot) => diffNotices(a, b).map((n) => noticeText(n, t));

test("no notices on first connection or when either side is not connected", () => {
  assert.deepEqual(diffNotices(EMPTY_SNAPSHOT, snap({ warnings: [warningOf()] })), []);
  assert.deepEqual(diffNotices(snap(), EMPTY_SNAPSHOT), []);
});

test("peer going offline and coming back; the local node never counts", () => {
  const off = peersFixture().map((p) => (p.name === "cloud-vm" ? { ...p, online: false } : p));
  assert.deepEqual(texts(snap({}, peersFixture()), snap({}, off)), ["设备 cloud-vm 已离线"]);
  assert.deepEqual(texts(snap({}, off), snap({}, peersFixture())), ["设备 cloud-vm 已上线"]);
  const selfDown = peersFixture().map((p) => (p.self ? { ...p, online: false } : p));
  assert.deepEqual(texts(snap({}, peersFixture()), snap({}, selfDown)), []);
});

test("a peer seen for the first time does not notify", () => {
  const more = [...peersFixture(), peerNamed("cloud-vm", { name: "new-box", node_id: "new-box" })];
  assert.deepEqual(texts(snap({}, peersFixture()), snap({}, more)), []);
});

test("peer transitions belong to the same selected instance and kernel boot", () => {
  const before = snap();
  before.selectedInstance = "first";
  const off = peersFixture().map((p) => p.name === "cloud-vm" ? { ...p, online: false } : p);
  const other = snap({}, off);
  other.selectedInstance = "second";
  assert.deepEqual(diffNotices(before, other), []);
  other.selectedInstance = "first";
  assert.equal(diffNotices(before, other).length, 1);
  if (other.connection.kind !== "connected") throw new Error("connected fixture required");
  other.connection.status.control = { ...other.connection.status.control, boot_id: "another-boot" };
  assert.deepEqual(diffNotices(before, other), []);
});

test("degrading warnings notify once, with no warning text; info warnings stay silent", () => {
  const w = { warnings: [warningOf("policy_reload_failed", "error", "cannot read /home/u/secret.yaml")] };
  const out = diffNotices(snap(), snap(w));
  assert.equal(out.length, 1);
  assert.doesNotMatch(noticeText(out[0]!, t), /secret|home/);
  assert.deepEqual(diffNotices(snap(w), snap({ warnings: [...w.warnings, warningOf("peer_offline")] })), []);
  assert.deepEqual(diffNotices(snap(), snap({ warnings: [warningOf("allow_dangerous", "info")] })), []);
});

test("restart_required and exhausted quota notify on the edge only", () => {
  assert.deepEqual(texts(snap(), snap({ restart_required: true })), ["配置变更需要重启内核"]);
  assert.deepEqual(texts(snap({ restart_required: true }), snap({ restart_required: true })), []);
  const base = statusWith().limits;
  const full = { limits: { ...base, pty_in_use: 20 } };
  assert.deepEqual(texts(snap(), snap(full)), ["终端配额已用尽"]);
  assert.deepEqual(texts(snap(full), snap(full)), []);
  const execFull = { limits: { ...base, exec_shared_in_use: base.exec_shared } };
  assert.deepEqual(texts(snap(), snap(execFull)), ["执行配额已用尽"]);
});
