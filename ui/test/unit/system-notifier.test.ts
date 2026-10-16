import assert from "node:assert/strict";
import { test } from "node:test";
import { type NotificationHandle, Notifier } from "../../src/main/system/notifier";

function setup(supported = true) {
  let now = 1_000_000;
  const shown: { body: string; handle: Handle }[] = [];
  const clicks: number[] = [];
  let failed = 0;
  class Handle implements NotificationHandle {
    listeners: Record<string, () => void> = {};
    visible = false;
    show(): void {
      this.visible = true;
    }
    on(event: string, l: () => void): this {
      this.listeners[event] = l;
      return this;
    }
  }
  const n = new Notifier({
    supported: () => supported,
    create: (o) => {
      assert.equal(o.title, "xmesh");
      const handle = new Handle();
      shown.push({ body: o.body, handle });
      return handle;
    },
    now: () => now,
    onClick: () => void clicks.push(now),
    title: "xmesh",
    summary: (c) => `${c} changes`,
    onFailed: () => void failed++,
  });
  return { n, shown, clicks, failed: () => failed, advance: (ms: number) => void (now += ms), bodies: () => shown.map((s) => s.body) };
}

test("shows each notice and does nothing when the OS has no notifications", () => {
  const s = setup();
  s.n.push([{ key: "a", text: "A" }, { key: "b", text: "B" }]);
  assert.deepEqual(s.bodies(), ["A", "B"]);
  assert.ok(s.shown.every((x) => x.handle.visible));
  const off = setup(false);
  off.n.push([{ key: "a", text: "A" }]);
  assert.deepEqual(off.bodies(), []);
});

test("the same key is suppressed for a minute, then fires again", () => {
  const s = setup();
  s.n.push([{ key: "peer.x.offline", text: "x off" }]);
  s.advance(30_000);
  s.n.push([{ key: "peer.x.offline", text: "x off" }]);
  assert.deepEqual(s.bodies(), ["x off"]);
  s.advance(31_000);
  s.n.push([{ key: "peer.x.offline", text: "x off" }]);
  assert.deepEqual(s.bodies(), ["x off", "x off"]);
});

test("a burst in one batch collapses into a summary", () => {
  const s = setup();
  s.n.push([1, 2, 3, 4, 5].map((i) => ({ key: `k${i}`, text: `n${i}` })));
  assert.deepEqual(s.bodies(), ["5 changes"]);
});

test("a global cap holds back a flood and releases after the window", () => {
  const s = setup();
  for (let i = 0; i < 10; i++) s.n.push([{ key: `k${i}`, text: `n${i}` }]);
  assert.equal(s.shown.length, 6);
  s.advance(61_000);
  s.n.push([{ key: "later", text: "later" }]);
  assert.equal(s.shown.length, 7);
});

test("clicking opens the panel; a failure is reported; handles are released", () => {
  const s = setup();
  s.n.push([{ key: "a", text: "A" }, { key: "b", text: "B" }]);
  s.shown[0]!.handle.listeners.click!();
  assert.equal(s.clicks.length, 1);
  s.shown[1]!.handle.listeners.failed!();
  assert.equal(s.failed(), 1);
  assert.equal((s.n as unknown as { live: Set<unknown> }).live.size, 0);
});
