// Loads the compiled preload against a fake `electron` and inspects exactly what it hands to the page.

import assert from "node:assert/strict";
import Module from "node:module";
import * as path from "node:path";
import { test } from "node:test";
import { CALLS_EVENT, CHANNELS, PANEL_API_GLOBAL, PANEL_API_METHODS, SNAPSHOT_EVENT } from "../../src/shared/ipc";

interface Exposed {
  name: string;
  api: Record<string, (...a: unknown[]) => unknown>;
}

function loadPreload() {
  const exposed: Exposed[] = [];
  const invoked: { channel: string; args: unknown[] }[] = [];
  const listeners = new Map<string, Set<(e: unknown, ...a: unknown[]) => void>>();
  const fake = {
    contextBridge: {
      exposeInMainWorld: (name: string, api: Exposed["api"]) => exposed.push({ name, api }),
    },
    ipcRenderer: {
      invoke: async (channel: string, ...args: unknown[]) => {
        invoked.push({ channel, args });
        return { ok: true };
      },
      on: (c: string, l: (e: unknown, ...a: unknown[]) => void) => {
        if (!listeners.has(c)) listeners.set(c, new Set());
        listeners.get(c)!.add(l);
      },
      removeListener: (c: string, l: (e: unknown, ...a: unknown[]) => void) => listeners.get(c)?.delete(l),
    },
  };
  const M = Module as unknown as { _load: (req: string, ...rest: unknown[]) => unknown };
  const orig = M._load;
  M._load = (req, ...rest) => (req === "electron" ? fake : orig.call(M, req, ...rest));
  const file = path.join(__dirname, "..", "..", "src", "preload", "index.js");
  delete require.cache[require.resolve(file)];
  try {
    require(file);
  } finally {
    M._load = orig;
  }
  return { exposed, invoked, listeners };
}

test("the preload exposes one frozen object named xmesh with exactly the whitelisted methods", () => {
  const { exposed } = loadPreload();
  assert.equal(exposed.length, 1);
  assert.equal(exposed[0]!.name, PANEL_API_GLOBAL);
  assert.deepEqual(Object.keys(exposed[0]!.api).sort(), [...PANEL_API_METHODS].sort());
  assert.ok(Object.isFrozen(exposed[0]!.api));
  for (const v of Object.values(exposed[0]!.api)) assert.equal(typeof v, "function");
});

test("each method calls its own channel with only the caller's argument", async () => {
  const { exposed, invoked } = loadPreload();
  const api = exposed[0]!.api;
  await api.getSnapshot!();
  await api.drain!();
  await api.resume!();
  await api.startKernel!("n1");
  await api.restartUnresponsive!("n1");
  await api.cleanupStale!("n1");
  await api.selectInstance!("n1");
  await api.copy!("gatewayUrl");
  await api.openPath!("logs");
  await api.setLoginItem!(true);
  await api.setDisplay!({ zoom: 1.1, uiFont: "", codeFont: "" });
  await api.openExternal!("docs");
  assert.deepEqual(invoked, [
    { channel: CHANNELS.getSnapshot, args: [] },
    { channel: CHANNELS.drain, args: [] },
    { channel: CHANNELS.resume, args: [] },
    { channel: CHANNELS.startKernel, args: ["n1"] },
    { channel: CHANNELS.restartUnresponsive, args: ["n1"] },
    { channel: CHANNELS.cleanupStale, args: ["n1"] },
    { channel: CHANNELS.selectInstance, args: ["n1"] },
    { channel: CHANNELS.copy, args: ["gatewayUrl"] },
    { channel: CHANNELS.openPath, args: ["logs"] },
    { channel: CHANNELS.setLoginItem, args: [true] },
    { channel: CHANNELS.setDisplay, args: [{ zoom: 1.1, uiFont: "", codeFont: "" }] },
    { channel: CHANNELS.openExternal, args: ["docs"] },
  ]);
});

test("panel data methods use their own fixed channels and forward only the caller's argument", async () => {
  const { exposed, invoked } = loadPreload();
  const api = exposed[0]!.api;
  const cfg = { content: "x", etag: "e", dry_run: true };
  const patch = { edits: [{ path: "/agent/disabled_tools", delete: true }], etag: "e", dryRun: false };
  await api.getSessions!();
  await api.getConfig!();
  await api.putConfig!(cfg);
  await api.patchConfig!(patch);
  await api.patchBackendEnv!({ name: "github", set: { GITHUB_TOKEN: "t" }, unset: [] });
  await api.clearBackendEnv!("github");
  await api.listPats!();
  await api.createPat!({ name: "ci" });
  await api.rotatePat!("ci");
  await api.deletePat!("ci");
  await api.listIngressClients!();
  await api.revokeIngressClient!("c1");
  await api.rotateIngressPassword!("a-long-enough-password");
  await api.exportPair!();
  await api.importPair!({ text: "t", fingerprint: "f" });
  await api.getPairPassword!();
  await api.setPairPassword!("correct horse battery");
  await api.clearPairPassword!();
  await api.discoverPairs!();
  await api.connectPair!({ address: "10.0.0.2", password: "correct horse battery" });
  await api.queryAudit!({ limit: 5 });
  await api.getRecentCalls!();
  await api.runSetup!({ any: 1 });
  await api.pickPath!("workspace");
  await api.mesh!({ op: "status" });
  await api.ci!({ op: "get" });
  await api.copyGateway!({ kind: "claudeCodeCommand", url: "http://127.0.0.1:8080/mcp" });
  await api.updateGateway!({ listen: ":8080", access: ["localhost"], hosts: [] });
  await api.rotateGatewayPat!();
  assert.deepEqual(
    invoked.map((i) => i.channel),
    [
      CHANNELS.getSessions, CHANNELS.getConfig, CHANNELS.putConfig, CHANNELS.patchConfig, CHANNELS.patchBackendEnv, CHANNELS.clearBackendEnv,
      CHANNELS.listPats, CHANNELS.createPat, CHANNELS.rotatePat, CHANNELS.deletePat, CHANNELS.listIngressClients, CHANNELS.revokeIngressClient,
      CHANNELS.rotateIngressPassword, CHANNELS.exportPair, CHANNELS.importPair, CHANNELS.getPairPassword,
      CHANNELS.setPairPassword, CHANNELS.clearPairPassword, CHANNELS.discoverPairs, CHANNELS.connectPair,
      CHANNELS.queryAudit, CHANNELS.getRecentCalls, CHANNELS.runSetup, CHANNELS.pickPath, CHANNELS.mesh,
      CHANNELS.ci,
      CHANNELS.copyGateway, CHANNELS.updateGateway, CHANNELS.rotateGatewayPat,
    ],
  );
  assert.deepEqual(invoked[2]!.args, [cfg]);
  assert.deepEqual(invoked[3]!.args, [patch]);
  assert.deepEqual(invoked[4]!.args, [{ name: "github", set: { GITHUB_TOKEN: "t" }, unset: [] }]);
  assert.deepEqual(invoked[5]!.args, ["github"]);
  assert.deepEqual(invoked[8]!.args, ["ci"]);
  assert.deepEqual(invoked.at(-3)!.args, [{ kind: "claudeCodeCommand", url: "http://127.0.0.1:8080/mcp" }]);
  assert.deepEqual(invoked.at(-1)!.args, [], "rotation takes no argument from the page");
});

test("onCalls hands the page the calls only and unsubscribes", () => {
  const { exposed, listeners } = loadPreload();
  const got: unknown[][] = [];
  const off = exposed[0]!.api.onCalls!((...a: unknown[]) => got.push(a)) as () => void;
  const set = listeners.get(CALLS_EVENT)!;
  assert.equal(set.size, 1);
  for (const l of set) l({ sender: "secret" }, [{ id: 1 }]);
  assert.deepEqual(got, [[[{ id: 1 }]]]);
  off();
  assert.equal(set.size, 0);
  assert.throws(() => exposed[0]!.api.onCalls!("nope"), TypeError);
});

test("no method lets the page pick a channel", async () => {
  const { exposed, invoked } = loadPreload();
  const api = exposed[0]!.api;
  // Extra arguments are never forwarded as a channel; the channel is fixed per method.
  await api.drain!("xmesh:set-login-item", true);
  assert.equal(invoked.at(-1)!.channel, CHANNELS.drain);
});

test("onSnapshot hands the page the snapshot only, never the IPC event, and unsubscribes", () => {
  const { exposed, listeners } = loadPreload();
  const got: unknown[][] = [];
  const off = exposed[0]!.api.onSnapshot!((...a: unknown[]) => got.push(a)) as () => void;
  const set = listeners.get(SNAPSHOT_EVENT)!;
  assert.equal(set.size, 1);
  for (const l of set) l({ sender: "secret" }, { connection: { kind: "offline" } });
  assert.deepEqual(got, [[{ connection: { kind: "offline" } }]]);
  off();
  assert.equal(set.size, 0);
  assert.throws(() => exposed[0]!.api.onSnapshot!("not a function"), TypeError);
});

test("the preload source mentions only the fixed channel literals", async () => {
  const fs = await import("node:fs");
  const src = fs.readFileSync(path.join(__dirname, "..", "..", "src", "preload", "index.js"), "utf8");
  const literals = new Set([...src.matchAll(/"(xmesh:[a-z-]+)"/g)].map((m) => m[1]));
  assert.deepEqual([...literals].sort(), [...Object.values(CHANNELS), SNAPSHOT_EVENT, CALLS_EVENT].sort());
  assert.doesNotMatch(src, /exposeInMainWorld\([^)]*ipcRenderer/);
});
