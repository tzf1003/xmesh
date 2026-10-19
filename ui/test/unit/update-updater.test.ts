import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { mock, test } from "node:test";
import { ApplyError, type Applier, type HandOver } from "../../src/main/update/apply";
import { CHECKSUM_FILE, type RawRelease, installerName } from "../../src/main/update/releases";
import { SourceError, type UpdateSource } from "../../src/main/update/source";
import { UpdateStore } from "../../src/main/update/store";
import { type StopResult, type UpdateNotice, Updater, type UpdaterEnv } from "../../src/main/update/updater";
import type { UpdateState } from "../../src/shared/update";
import { parseVersion } from "../../src/shared/version";

const BODY = Buffer.from("installer bytes");
const sha = (b: Buffer) => createHash("sha256").update(b).digest("hex");
const target = { platform: "win", arch: "x64" } as const;

interface Rig {
  updater: Updater;
  dir: string;
  data: string;
  events: string[];
  notices: UpdateNotice[];
  states: UpdateState[];
  handovers: HandOver[];
  src: { versions: string[]; fail: boolean; sums: string | undefined; body: Buffer; downloads: number; gate?: Promise<void> };
  nodes: { running: string[]; stopResult: Record<string, StopResult | "throw">; };
  applier: { blocked: string; fail: Error | undefined };
}

function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve!: () => void;
  const promise = new Promise<void>((r) => (resolve = r));
  return { promise, resolve };
}

function rig(over: Partial<UpdaterEnv> = {}): Rig {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-updater-"));
  const data = path.join(dir, "updates");
  const events: string[] = [];
  const notices: UpdateNotice[] = [];
  const handovers: HandOver[] = [];
  const src: Rig["src"] = { versions: ["0.1.0-beta.3"], fail: false, sums: undefined, body: BODY, downloads: 0 };
  const nodes: Rig["nodes"] = { running: ["node-a", "node-b"], stopResult: {} };
  const applier: Rig["applier"] = { blocked: "", fail: undefined };
  const releases = (): RawRelease[] =>
    src.versions.map((version) => {
      const v = parseVersion(version)!;
      return {
        tag_name: `v${version}`,
        draft: false,
        prerelease: v.pre.length > 0,
        published_at: "2026-10-18T00:00:00Z",
        body: `notes ${version}`,
        assets: [{ name: installerName(v, target), size: BODY.length }, { name: CHECKSUM_FILE, size: 1 }],
      };
    });
  const source: UpdateSource = {
    async releases() {
      if (src.fail) throw new SourceError("network", "offline");
      return releases();
    },
    async text() {
      if (src.fail) throw new SourceError("network", "offline");
      return src.sums ?? releases().map((r) => `${sha(BODY)}  ${r.assets[0]!.name}`).join("\n");
    },
    async download(_t, _n, dest, onProgress, signal) {
      src.downloads++;
      await src.gate;
      if (signal.aborted) throw new SourceError("cancelled", "cancelled");
      fs.writeFileSync(dest, src.body);
      onProgress({ received: src.body.length, total: src.body.length });
      return { sha256: sha(src.body), bytes: src.body.length };
    },
  };
  const a: Applier = {
    blocked: () => applier.blocked as never,
    async start(h) {
      handovers.push(h);
      if (applier.fail) throw applier.fail;
    },
  };
  const env: UpdaterEnv = {
    version: "0.1.0-beta.2",
    packaged: true,
    platform: "win32",
    arch: "x64",
    kernelBundled: true,
    execPath: "C:\\xmesh\\xmesh.exe",
    pid: 1234,
    dataDir: data,
    source,
    applier: a,
    store: new UpdateStore(path.join(dir, "update.json")),
    runningNodes: () => nodes.running,
    stopNode: async (n) => {
      events.push(`stop ${n}`);
      const r = nodes.stopResult[n] ?? "stopped";
      if (r === "throw") throw new Error(`cannot stop ${n}`);
      return r;
    },
    startNode: async (n) => void events.push(`start ${n}`),
    quit: () => void events.push("quit"),
    openPath: async (p) => (events.push(`open ${path.basename(p)}`), ""),
    notify: (n) => void notices.push(n),
    ...over,
  };
  const updater = new Updater(env);
  const states: UpdateState[] = [];
  updater.on("change", (s) => states.push(s));
  return { updater, dir, data, events, notices, states, handovers, src, nodes, applier };
}

test("a development run, an unsupported system and a blocked install say why and never check", async () => {
  assert.equal(rig({ packaged: false }).updater.state.blocked, "unpackaged");
  assert.equal(rig({ platform: "linux" }).updater.state.blocked, "unsupported");
  assert.equal(rig({ applier: undefined }).updater.state.blocked, "unsupported");
  const r = rig({ packaged: false });
  await r.updater.check();
  assert.equal(r.updater.state.phase, "idle");
  assert.equal(r.updater.state.checkedAt, undefined);
  const t = rig();
  t.applier.blocked = "translocated";
  assert.equal(rig({ applier: { blocked: () => "translocated", start: async () => {} } }).updater.state.blocked, "translocated");
  void t;
});

test("a check finds the newer release, remembers when it looked, and an automatic one announces it once", async () => {
  const r = rig();
  await r.updater.check(false);
  const s = r.updater.state;
  assert.equal(s.phase, "available");
  assert.equal(s.latest?.version, "0.1.0-beta.3");
  assert.equal(s.latest?.notes, "notes 0.1.0-beta.3");
  assert.ok(s.checkedAt);
  assert.deepEqual(r.notices, [{ kind: "available", version: "0.1.0-beta.3" }]);
  await r.updater.check(false);
  assert.equal(r.notices.length, 1, "the same release is announced once");
});

test("a manual check does not notify; nothing newer leaves the card on 'up to date'", async () => {
  const r = rig();
  await r.updater.check(true);
  assert.deepEqual(r.notices, []);
  r.src.versions = ["0.1.0-beta.2", "0.1.0-beta.1"];
  await r.updater.check(true);
  assert.equal(r.updater.state.phase, "idle");
  assert.equal(r.updater.state.latest, undefined);
  assert.ok(r.updater.state.checkedAt);
});

test("a failed manual check shows the error; a failed automatic one stays quiet and keeps what was known", async () => {
  const r = rig();
  await r.updater.check(true);
  r.src.fail = true;
  await r.updater.check(false);
  assert.equal(r.updater.state.phase, "available");
  assert.equal(r.updater.state.error, undefined);
  await r.updater.check(true);
  const st: UpdateState = r.updater.state;
  assert.equal(st.phase, "error");
  assert.equal(st.error?.code, "network");
});

test("the prerelease switch changes the answer at once and is remembered", async () => {
  const r = rig();
  r.src.versions = ["0.1.0-beta.3"];
  await r.updater.check(true);
  assert.equal(r.updater.state.phase, "available");
  r.updater.setPrefs({ auto: true, prerelease: false });
  await new Promise((res) => setTimeout(res, 20));
  assert.equal(r.updater.state.phase, "idle");
  assert.equal(r.updater.state.latest, undefined);
  const saved = JSON.parse(fs.readFileSync(path.join(r.dir, "update.json"), "utf8"));
  assert.deepEqual(saved.prefs, { auto: true, prerelease: false });
});

test("install: downloads, verifies, stops running kernels, writes the pending note, hands over and quits", async () => {
  const r = rig();
  await r.updater.check(true);
  const res = await r.updater.install();
  assert.deepEqual(res, { ok: true });
  assert.deepEqual(r.events, ["stop node-a", "stop node-b", "quit"]);
  assert.equal(r.updater.state.phase, "installing");
  const h = r.handovers[0]!;
  assert.equal(path.basename(h.installer), "xmesh-ui-0.1.0-beta.3-win-x64.exe");
  assert.deepEqual(fs.readFileSync(h.installer), BODY);
  assert.equal(h.pid, 1234);
  assert.equal(h.execPath, "C:\\xmesh\\xmesh.exe");
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(r.data, "pending.json"), "utf8")), { from: "0.1.0-beta.2", to: "0.1.0-beta.3", nodes: ["node-a", "node-b"] });
  assert.ok(r.states.some((s) => s.phase === "downloading" && s.progress));
});

test("install without a found release or while already busy is refused", async () => {
  const r = rig();
  assert.equal((await r.updater.install()).ok, false);
  await r.updater.check(true);
  const gate = deferred();
  r.src.gate = gate.promise;
  const first = r.updater.install();
  const second = await r.updater.install();
  assert.deepEqual(second.ok, false);
  gate.resolve();
  assert.equal((await first).ok, true);
});

test("a node that already exited is skipped during installation", async () => {
  const r = rig();
  r.nodes.stopResult["node-b"] = "absent";
  await r.updater.check(true);
  await r.updater.install();
  const pending = JSON.parse(fs.readFileSync(path.join(r.data, "pending.json"), "utf8"));
  assert.deepEqual(pending.nodes, ["node-a"]);
});

test("a kernel that will not stop cancels the update and brings the stopped ones back", async () => {
  const r = rig();
  r.nodes.stopResult["node-b"] = "throw";
  await r.updater.check(true);
  const res = await r.updater.install();
  assert.equal(!res.ok && res.error.code, "stop_failed");
  assert.deepEqual(r.events, ["stop node-a", "stop node-b", "start node-a"]);
  assert.equal(r.updater.state.phase, "available");
  assert.equal(r.handovers.length, 0);
  assert.equal(fs.existsSync(path.join(r.data, "pending.json")), false);
});

test("a corrupt download is deleted and nothing is stopped", async () => {
  const r = rig();
  r.src.sums = `${"0".repeat(64)}  xmesh-ui-0.1.0-beta.3-win-x64.exe`;
  await r.updater.check(true);
  const res = await r.updater.install();
  assert.equal(!res.ok && res.error.code, "bad_checksum");
  assert.deepEqual(r.events, []);
  assert.deepEqual(fs.readdirSync(r.data), []);
  assert.equal(r.updater.state.phase, "available");
});

test("a release that publishes no checksum for the installer is not installed", async () => {
  const r = rig();
  r.src.sums = `${sha(BODY)}  something-else.exe`;
  await r.updater.check(true);
  const res = await r.updater.install();
  assert.equal(!res.ok && res.error.code, "bad_checksum");
  assert.equal(r.src.downloads, 0);
});

test("cancelling a download is not an error", async () => {
  const r = rig();
  await r.updater.check(true);
  const gate = deferred();
  r.src.gate = gate.promise;
  const run = r.updater.install();
  await new Promise((res) => setTimeout(res, 10));
  r.updater.cancel();
  gate.resolve();
  const res = await run;
  assert.equal(res.ok, false);
  assert.equal(r.updater.state.phase, "available");
  assert.equal(r.updater.state.error, undefined);
  assert.deepEqual(r.events, []);
});

test("a download left by an earlier attempt with the right hash is reused", async () => {
  const r = rig();
  await r.updater.check(true);
  fs.mkdirSync(r.data, { recursive: true });
  fs.writeFileSync(path.join(r.data, "xmesh-ui-0.1.0-beta.3-win-x64.exe"), BODY);
  await r.updater.install();
  assert.equal(r.src.downloads, 0);
  assert.equal(r.handovers.length, 1);
});

test("a hand-over that cannot start restarts the kernels and drops the pending note", async () => {
  const r = rig();
  r.applier.fail = new ApplyError("spawn_failed", "no cmd");
  await r.updater.check(true);
  const res = await r.updater.install();
  assert.equal(!res.ok && res.error.code, "install_failed");
  assert.deepEqual(r.events, ["stop node-a", "stop node-b", "start node-a", "start node-b"]);
  assert.equal(fs.existsSync(path.join(r.data, "pending.json")), false);
  assert.equal(r.updater.state.phase, "available");
  assert.ok(!r.events.includes("quit"));
});

test("kernels come back when the pending note cannot be written", async () => {
  const ref: { data?: string } = {};
  const r = rig({
    stopNode: async (n) => {
      // The disk turns unwritable once the nodes are stopped: a directory sits where the note must go.
      fs.mkdirSync(path.join(ref.data!, "pending.json"), { recursive: true });
      return n === "" ? "absent" : "stopped";
    },
  });
  ref.data = r.data;
  await r.updater.check(true);
  const res = await r.updater.install();
  assert.equal(!res.ok && res.error.code, "install_failed");
  assert.equal(r.handovers.length, 0);
  assert.ok(r.events.includes("start node-a") && r.events.includes("start node-b"));
  assert.equal(r.updater.state.phase, "available");
  assert.ok(!r.events.includes("quit"));
});

test("an app folder the user cannot write to opens the installer for a manual copy", async () => {
  const r = rig();
  r.applier.fail = new ApplyError("not_writable", "/Applications is not writable");
  await r.updater.check(true);
  const res = await r.updater.install();
  assert.equal(!res.ok && res.error.code, "not_writable");
  assert.ok(r.events.includes("open xmesh-ui-0.1.0-beta.3-win-x64.exe"));
  assert.ok(fs.existsSync(path.join(r.data, "xmesh-ui-0.1.0-beta.3-win-x64.exe")), "the file stays for the user");
});

test("after the update the new app restarts the kernels, reports success and clears the leftovers", async () => {
  const r = rig();
  await r.updater.check(true);
  await r.updater.install();
  r.events.length = 0;
  const after = rig({ version: "0.1.0-beta.3", dataDir: r.data });
  await after.updater.finishPending();
  assert.deepEqual(after.events, ["start node-a", "start node-b"]);
  assert.deepEqual(after.updater.state.outcome, { ok: true, version: "0.1.0-beta.3" });
  assert.deepEqual(after.notices, [{ kind: "updated", version: "0.1.0-beta.3" }]);
  assert.deepEqual(fs.readdirSync(r.data), []);
});

test("a kernel that will not start after the update is reported, the others still start", async () => {
  const r = rig();
  await r.updater.check(true);
  await r.updater.install();
  const after = rig({
    version: "0.1.0-beta.3",
    dataDir: r.data,
    startNode: async (n) => {
      if (n === "node-a") throw new Error("service refused");
      after.events.push(`start ${n}`);
    },
  });
  await after.updater.finishPending();
  assert.deepEqual(after.events, ["start node-b"]);
  assert.deepEqual(after.updater.state.outcome, { ok: false, version: "0.1.0-beta.3", message: "kernel restart failed: node-a", unstarted: ["node-a"] });
  assert.deepEqual(after.notices, [{ kind: "failed", version: "0.1.0-beta.3" }]);
});

test("the new app repairs a surviving kernel omitted by an older updater", async () => {
  const r = rig();
  r.nodes.stopResult["node-a"] = "absent";
  r.nodes.stopResult["node-b"] = "absent";
  await r.updater.check(true);
  await r.updater.install();
  const refreshed: string[] = [];
  const after = rig({ version: "0.1.0-beta.3", dataDir: r.data, refreshNode: async (node) => { refreshed.push(node); } });
  await after.updater.finishPending();
  assert.deepEqual(refreshed, ["node-a", "node-b"]);
  assert.deepEqual(after.updater.state.outcome, { ok: true, version: "0.1.0-beta.3" });
});

test("failure to replace a surviving old kernel makes the update incomplete", async () => {
  const r = rig();
  r.nodes.stopResult["node-b"] = "absent";
  await r.updater.check(true);
  await r.updater.install();
  const after = rig({ version: "0.1.0-beta.3", dataDir: r.data, refreshNode: async () => { throw new Error("old kernel remains"); } });
  await after.updater.finishPending();
  assert.deepEqual(after.updater.state.outcome, { ok: false, version: "0.1.0-beta.3", message: "kernel restart failed: node-b", unstarted: ["node-b"] });
  assert.deepEqual(after.notices, [{ kind: "failed", version: "0.1.0-beta.3" }]);
});

test("a leftover that cannot be removed does not keep the kernels down", async () => {
  const r = rig();
  await r.updater.check(true);
  await r.updater.install();
  const after = rig({ version: "0.1.0-beta.3", dataDir: r.data });
  // the module object itself: the updater reads rmSync through it at call time
  const mod = require("node:fs") as typeof fs;
  const real = mod.rmSync;
  const locked = mock.method(mod, "rmSync", (p: fs.PathLike, o?: fs.RmOptions) => {
    if (String(p).endsWith(".exe")) throw Object.assign(new Error("EBUSY"), { code: "EBUSY" });
    return real(p, o);
  });
  try {
    await after.updater.finishPending();
  } finally {
    locked.mock.restore();
  }
  assert.deepEqual(after.events, ["start node-a", "start node-b"]);
  assert.equal(after.updater.state.outcome?.ok, true);
});

test("if the app comes back at the old version the update failed; the reason the script left is shown and kernels still restart", async () => {
  const r = rig();
  await r.updater.check(true);
  await r.updater.install();
  fs.writeFileSync(path.join(r.data, "result.txt"), "installer exit code 2\r\n");
  const after = rig({ version: "0.1.0-beta.2", dataDir: r.data });
  await after.updater.finishPending();
  assert.deepEqual(after.events, ["start node-a", "start node-b"]);
  assert.deepEqual(after.updater.state.outcome, { ok: false, version: "0.1.0-beta.3", message: "installer exit code 2" });
  assert.deepEqual(after.notices, [{ kind: "failed", version: "0.1.0-beta.3" }]);
});

test("without an update in flight, starting only cleans up", async () => {
  const r = rig();
  fs.mkdirSync(r.data, { recursive: true });
  fs.writeFileSync(path.join(r.data, "old.exe.part"), "x");
  await r.updater.finishPending();
  assert.deepEqual(fs.readdirSync(r.data), []);
  assert.equal(r.updater.state.outcome, undefined);
  assert.deepEqual(r.events, []);
});

test("an update stops the desktop host and restores it before starting kernels", async () => {
  const order: string[] = [];
  const r = rig({
    stopComputer: async () => { order.push("computer-stop"); return "stopped"; },
    stopNode: async (node) => { order.push(`stop ${node}`); return "stopped"; },
  });
  await r.updater.check(true);
  assert.deepEqual(await r.updater.install(), { ok: true });
  assert.deepEqual(order, ["computer-stop", "stop node-a", "stop node-b"]);
  assert.equal(JSON.parse(fs.readFileSync(path.join(r.data, "pending.json"), "utf8")).computer, true);
  order.length = 0;
  const after = rig({ version: "0.1.0-beta.3", dataDir: r.data,
    startComputer: async () => { order.push("computer-start"); },
    startNode: async (node) => { order.push(`start ${node}`); },
  });
  await after.updater.finishPending();
  assert.deepEqual(order, ["computer-start", "start node-a", "start node-b"]);
  assert.equal(after.updater.state.outcome?.ok, true);
});

test("a failed installer handover restores the stopped desktop host", async () => {
  let starts = 0;
  const r = rig({ stopComputer: async () => "stopped", startComputer: async () => { starts++; } });
  r.applier.fail = new Error("installer unavailable");
  await r.updater.check(true);
  assert.equal((await r.updater.install()).ok, false);
  assert.equal(starts, 1);
});

test("desktop stop failures block replacement and restore failures fail update acceptance", async () => {
  const blocked = rig({ stopComputer: async () => { throw new Error("desktop survives"); } });
  await blocked.updater.check(true);
  const result = await blocked.updater.install();
  assert.equal(!result.ok && result.error.code, "stop_failed");
  assert.deepEqual(blocked.events, []);
  assert.equal(blocked.handovers.length, 0);
  const r = rig({ stopComputer: async () => "stopped" });
  await r.updater.check(true);
  await r.updater.install();
  const after = rig({ version: "0.1.0-beta.3", dataDir: r.data, startComputer: async () => { throw new Error("desktop unavailable"); } });
  await after.updater.finishPending();
  assert.equal(after.updater.state.outcome?.ok, false);
  assert.deepEqual(after.updater.state.outcome?.unstarted, ["computer-host"]);
});

test("the store keeps preferences and the announced tag, and reads a damaged file as the defaults", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-update-store-"));
  const file = path.join(dir, "update.json");
  const a = new UpdateStore(file);
  assert.deepEqual(a.prefs, { auto: true, prerelease: true });
  assert.equal(a.announced, "");
  assert.equal(a.set({ prefs: { auto: false, prerelease: false }, announced: "v0.2.0" }), true);
  const b = new UpdateStore(file);
  assert.deepEqual(b.prefs, { auto: false, prerelease: false });
  assert.equal(b.announced, "v0.2.0");
  // a second save replaces the file that is already there (the rename must overwrite on Windows too)
  assert.equal(b.set({ announced: "v0.3.0" }), true);
  assert.equal(new UpdateStore(file).announced, "v0.3.0");
  fs.writeFileSync(file, "{not json");
  assert.deepEqual(new UpdateStore(file).prefs, { auto: true, prerelease: true });
  fs.writeFileSync(file, JSON.stringify({ prefs: { auto: "yes" }, announced: 5 }));
  assert.deepEqual(new UpdateStore(file).prefs, { auto: true, prerelease: true });
});
