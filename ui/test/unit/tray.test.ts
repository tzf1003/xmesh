import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { test } from "node:test";
import * as path from "node:path";
import { Actions, type Platform } from "../../src/main/actions";
import type { ShellController } from "../../src/main/controller";
import { PanelNavigation } from "../../src/main/panel-navigation";
import { resolveUiFile } from "../../src/main/policy";
import { trustedTraySender } from "../../src/main/tray-ipc";
import { trayPosition } from "../../src/main/tray-position";
import { TrayService } from "../../src/main/tray-service";
import { translator } from "../../src/shared/i18n";
import { EMPTY_SNAPSHOT, type Snapshot } from "../../src/shared/state";
import { parseTrayCommand, type PanelTarget, type RecentCallView } from "../../src/shared/tray";
import { peersFixture, statusWith } from "../../testserver/fixtures";

function harness() {
  let dirty = false;
  const calls: unknown[] = [],
    destinations: PanelTarget[] = [];
  const controller = Object.assign(new EventEmitter(), {
    snapshot: {
      ...EMPTY_SNAPSHOT,
      selectedInstance: "node-a",
      instances: [
        { id: "node-a", label: "A" },
        { id: "node-b", label: "B" },
      ],
      connection: {
        kind: "connected",
        status: statusWith(),
        peers: peersFixture(),
      },
    } as Snapshot,
    recentActivity: [] as RecentCallView[],
    lastUpdatedAt: 123,
    drain: async () => {
      calls.push("drain");
    },
    resume: async () => {
      calls.push("resume");
    },
    refreshNow: async () => {
      calls.push("refresh");
    },
    selectInstance: (id: string) => {
      calls.push(id);
    },
    nodeName: (id: string) => id,
  });
  const platform: Platform = {
    writeClipboard: (value) => {
      calls.push(value);
    },
    openPath: async () => "",
    openExternal: async () => {},
    confirm: async () => {
      calls.push("native-confirm");
      return true;
    },
    showPanel: () => {},
    quit: () => {
      calls.push("quit");
    },
    runCli: async (command, node) => {
      calls.push({ command, node });
    },
    notifyError: () => {},
    pickPath: async () => undefined,
    removeStaleState: async () => {},
  };
  const actions = new Actions(controller as unknown as ShellController, platform, translator("zh"), {
    read: async () => "test-token-0123456789abcdef",
  });
  const service = new TrayService(controller as unknown as ShellController, actions, translator("zh"), {
    panelDirty: () => dirty,
    openPanel: (target) => {
      destinations.push(target);
    },
    copy: (value) => {
      calls.push(value);
    },
  });
  const target = () => ({
    instance: service.state.instance,
    bootId: service.state.bootId,
  });
  return {
    service,
    controller,
    calls,
    destinations,
    target,
    setDirty: (value: boolean) => {
      dirty = value;
    },
  };
}

test("compact tray placement fits each work area, including negative and small displays", () => {
  for (const work of [
    { x: 0, y: 24, width: 1440, height: 850 },
    { x: -1440, y: -200, width: 1440, height: 900 },
    { x: 0, y: 0, width: 320, height: 380 },
  ]) {
    for (const anchor of [
      { x: work.x, y: work.y, width: 22, height: 22 },
      {
        x: work.x + work.width - 22,
        y: work.y + work.height - 22,
        width: 22,
        height: 22,
      },
    ]) {
      const r = trayPosition(anchor, work);
      assert.ok(r.x >= work.x && r.y >= work.y);
      assert.ok(r.x + r.width <= work.x + work.width && r.y + r.height <= work.y + work.height);
      assert.ok(r.width <= 400 && r.height <= 580);
    }
  }
});
test("tray preload requests have fixed shapes, bounded strings and known destinations", () => {
  const base = { instance: "node-a", bootId: null };
  assert.ok(parseTrayCommand({ ...base, op: "refresh" }));
  assert.ok(
    parseTrayCommand({
      ...base,
      op: "open-panel",
      page: "terminal",
      device: "server",
    }),
  );
  for (const bad of [
    null,
    [],
    {},
    { ...base, op: "shell", command: "x" },
    { ...base, op: "refresh", extra: 1 },
    { ...base, op: "action", id: "x".repeat(257) },
    { ...base, op: "open-panel", page: "https://evil" },
    { ...base, op: "open-panel", page: "overview", peer: "x\n" },
    { ...base, bootId: {}, op: "refresh" },
  ])
    assert.equal(parseTrayCommand(bad), undefined);
});
test("tray origin alone does not authorize IPC; exact registered top frame is required", () => {
  assert.ok(trustedTraySender({ url: "app://tray/index.html", parent: null }, true));
  for (const frame of [
    null,
    { url: "app://tray/index.html", parent: {} },
    { url: "app://panel/index.html", parent: null },
    { url: "app://user@tray/index.html", parent: null },
    { url: "app://tray:12/index.html", parent: null },
    { url: "app://tray/other.html", parent: null },
  ])
    assert.equal(trustedTraySender(frame, true), false);
  assert.equal(trustedTraySender({ url: "app://tray/index.html", parent: null }, false), false);
  assert.equal(
    resolveUiFile(path.resolve("render"), "app://tray/index.html"),
    path.resolve("render", "tray", "index.html"),
  );
  assert.equal(resolveUiFile("/tmp/render", "app://tray/a%5c..%5cconfig.json"), undefined);
});
test("tray projection contains explicit UI fields and keeps secrets and detailed config private", () => {
  const h = harness(),
    dto = h.service.state;
  assert.equal(dto.instance, "node-a");
  assert.equal(dto.updatedAt, 123);
  assert.equal(dto.connection.kind, "connected");
  const raw = JSON.stringify(dto);
  for (const key of ["config_file", "state_dir", "env_names", "authkey_readable", "workspaces"])
    assert.ok(!raw.includes(key), key);
  assert.ok(dto.connection.kind === "connected" && dto.connection.peers.every((p) => !p.self));
  h.service.dispose();
});
test("stale targets and forged action ids have no desktop effects", async () => {
  const h = harness();
  assert.equal(
    (
      await h.service.execute({
        ...h.target(),
        instance: "node-b",
        op: "action",
        id: "action.quit",
      })
    ).ok,
    false,
  );
  assert.equal(
    (
      await h.service.execute({
        ...h.target(),
        op: "action",
        id: "execute-shell",
      })
    ).ok,
    false,
  );
  assert.equal(
    (
      await h.service.execute({
        ...h.target(),
        op: "copy-peer",
        peer: "arbitrary",
      })
    ).ok,
    false,
  );
  assert.deepEqual(h.calls, []);
  h.service.dispose();
});
test("stop confirmation is single-use, executes once, and bypasses a second native dialog", async () => {
  const h = harness();
  const r = await h.service.execute({
    ...h.target(),
    op: "action",
    id: "action.stopKernel",
  });
  assert.ok(r.ok && r.data.confirmation);
  assert.deepEqual(h.calls, []);
  if (!r.ok || !r.data.confirmation) throw new Error("missing challenge");
  const command = {
    ...h.target(),
    op: "confirm" as const,
    token: r.data.confirmation.token,
  };
  assert.equal((await h.service.execute(command)).ok, true);
  assert.deepEqual(h.calls, [{ command: "stop", node: "node-a" }]);
  assert.equal((await h.service.execute(command)).ok, false);
  assert.equal(h.calls.length, 1);
  h.service.dispose();
});
test("a challenge cannot run against another kernel boot or after hiding the tray", async () => {
  const h = harness();
  const r = await h.service.execute({
    ...h.target(),
    op: "action",
    id: "settings.restartKernel",
  });
  assert.ok(r.ok && r.data.confirmation);
  if (!r.ok || !r.data.confirmation) throw new Error("missing challenge");
  h.service.clearConfirmation();
  assert.equal(
    (
      await h.service.execute({
        ...h.target(),
        op: "confirm",
        token: r.data.confirmation.token,
      })
    ).ok,
    false,
  );
  const again = await h.service.execute({
    ...h.target(),
    op: "action",
    id: "settings.restartKernel",
  });
  if (!again.ok || !again.data.confirmation) throw new Error("missing challenge");
  const c = h.controller.snapshot.connection;
  if (c.kind === "connected") c.status.control.boot_id += "-new";
  assert.equal(
    (
      await h.service.execute({
        ...h.target(),
        op: "confirm",
        token: again.data.confirmation.token,
      })
    ).ok,
    false,
  );
  assert.deepEqual(h.calls, []);
  h.service.dispose();
});
test("capability changes are rechecked at execution and dirty main windows block instance switching", async () => {
  const h = harness();
  h.setDirty(true);
  assert.equal(
    (
      await h.service.execute({
        ...h.target(),
        op: "action",
        id: "instance.node-b",
      })
    ).ok,
    false,
  );
  assert.equal(h.destinations[0]?.page, "config");
  const c = h.controller.snapshot.connection;
  if (c.kind === "connected")
    c.status.control.capabilities = c.status.control.capabilities.filter((x) => x !== "drain");
  assert.equal(
    (
      await h.service.execute({
        ...h.target(),
        op: "action",
        id: "action.drain",
      })
    ).ok,
    false,
  );
  assert.deepEqual(h.calls, []);
  h.service.dispose();
});
test("panel navigation waits for the exact subscribed renderer and keeps only the latest destination", () => {
  const sent: unknown[] = [];
  let current = {
    isDestroyed: () => false,
    send: (_: string, value: unknown) => {
      sent.push(value);
    },
  };
  const nav = new PanelNavigation(
    () => current,
    () => {},
  );
  nav.request({ page: "overview", instance: "a" });
  nav.request({ page: "audit", instance: "a" });
  assert.deepEqual(sent, []);
  nav.ready(true);
  assert.equal(nav.isDirty, true);
  assert.deepEqual(sent, [{ page: "audit", instance: "a" }]);
  current = { ...current };
  assert.equal(nav.isDirty, false);
  nav.request({ page: "settings", instance: "a" });
  assert.equal(sent.length, 1);
  nav.ready(false);
  assert.equal(sent.length, 2);
});

test("custom unresponsive restart requires a single-use confirmation bound to the manifest boot", async () => {
  const h = harness();
  h.controller.snapshot = { ...EMPTY_SNAPSHOT, unresponsiveNodes: ["node-a"] };
  let boot = "first";
  Object.assign(h.controller, { nodeBootId: () => boot });
  const first = await h.service.execute({ ...h.target(), op: "action", id: "action.restartUnresponsive.node-a" });
  if (!first.ok || !first.data.confirmation) throw new Error("missing challenge");
  assert.equal(first.data.confirmation.impactKnown, false);
  assert.deepEqual(h.calls, []);
  boot = "new-boot";
  const stale = await h.service.execute({ ...h.target(), op: "confirm", token: first.data.confirmation.token });
  assert.equal(stale.ok, false);
  assert.deepEqual(h.calls, []);
  const next = await h.service.execute({ ...h.target(), op: "action", id: "action.restartUnresponsive.node-a" });
  if (!next.ok || !next.data.confirmation) throw new Error("missing challenge");
  const confirmed = await h.service.execute({ ...h.target(), op: "confirm", token: next.data.confirmation.token });
  assert.equal(confirmed.ok, true);
  assert.deepEqual(h.calls, [{ command: "restart", node: "node-a" }]);
  h.service.dispose();
});
