import assert from "node:assert/strict";
import { test } from "node:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { discoverKernelTargets, KernelLifecycle, recoverKernelPaths, type KernelTarget } from "../../src/main/kernel-lifecycle";
import { parseKernelRequest } from "../../src/shared/kernel";

function fixture(options: { running?: boolean; managed?: boolean; fail?: string; survive?: boolean; version?: string } = {}) {
  const target: KernelTarget = { id: "state-dir", name: "office", configFile: path.resolve("custom/config.yaml"), stateDir: path.resolve("custom/state-dir"),
    running: options.running ?? false, managed: options.managed ?? true,
    instance: { id: "state-dir", manifest: { node: "office", pid: 123, started_at: "2026-10-20T00:00:00Z", endpoint: "unused" } } as KernelTarget["instance"] };
  let exited = !target.running;
  const calls: string[][] = [];
  const service = new KernelLifecycle({
    targets: () => [target], resolve: async (node) => node,
    run: async (args) => {
      calls.push([...args]);
      if (args[0] === options.fail) return { code: 1, stdout: "", stderr: "scheduler rejected command" };
      if (args[0] === "stop" && !options.survive) { exited = true; target.running = false; }
      if (args[0] === "start") { exited = false; target.running = true; }
      return { code: 0, stdout: "", stderr: "" };
    },
    changed: () => undefined, gone: () => exited,
    status: async () => ({ control: { kernel_version: options.version ?? "1.0.0" } }), version: "1.0.0", timeoutMs: 8, pollMs: 1,
  });
  return { service, target, calls };
}

test("lifecycle requests reject arbitrary paths, command names and malformed nodes", () => {
  for (const raw of [null, [], { op: "exec", node: "office" }, { op: "start", node: "../office" }, { op: "install", node: "office", config: "C:\\other.yaml" }, { op: "list", node: "office" }]) {
    assert.equal(parseKernelRequest(raw), undefined);
  }
  assert.deepEqual(parseKernelRequest({ op: "reinstall", node: "office" }), { op: "reinstall", node: "office" });
});

test("list does not expose configuration paths and unknown or system nodes cannot be changed", async () => {
  const { service, calls } = fixture({ managed: false });
  assert.deepEqual(await service.request({ op: "list" }), { ok: true, data: { nodes: [{ id: "state-dir", name: "office", running: false, managed: false }] } });
  for (const node of ["state-dir", "unknown"]) {
    const r = await service.request({ op: "reinstall", node });
    assert.equal(r.ok, false);
    if (!r.ok) assert.equal(r.error.code, node === "unknown" ? "not_found" : "forbidden");
  }
  assert.deepEqual(calls, []);
});

test("installation migrates the selected node, registers explicit state and does not start it", async () => {
  const { service, calls, target } = fixture();
  assert.equal((await service.request({ op: "install", node: target.id })).ok, true);
  assert.deepEqual(calls, [["migrate", "--config", target.configFile, "--state-dir", target.stateDir],
    ["install", "--config", target.configFile, "--state-dir", target.stateDir, "--json"]]);
});

test("reinstall stops before unregistering, reuses custom paths and starts the replacement", async () => {
  const { service, calls, target } = fixture({ running: true });
  assert.equal((await service.request({ op: "reinstall", node: target.id })).ok, true);
  assert.deepEqual(calls.map((c) => c[0]), ["stop", "migrate", "uninstall", "install", "migrate", "start"]);
  for (const args of calls) assert.equal(args[2], target.configFile);
  assert.equal(target.running, true);
});

test("uninstall removes only service registration and leaves configuration and state files", async (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-lifecycle-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const { service, target, calls } = fixture({ running: true });
  target.configFile = path.join(root, "xmesh.yaml");
  target.stateDir = root;
  fs.writeFileSync(target.configFile, "original configuration");
  fs.writeFileSync(path.join(root, "device-identity"), "original identity");
  assert.equal((await service.request({ op: "uninstall", node: target.id })).ok, true);
  assert.deepEqual(calls.map((c) => c[0]), ["stop", "uninstall"]);
  assert.equal(fs.readFileSync(target.configFile, "utf8"), "original configuration");
  assert.equal(fs.readFileSync(path.join(root, "device-identity"), "utf8"), "original identity");
});

test("a surviving old process blocks reinstall before registration is removed", async () => {
  const { service, calls } = fixture({ running: true, survive: true });
  const r = await service.request({ op: "reinstall", node: "state-dir" });
  assert.equal(r.ok, false);
  if (!r.ok) assert.equal(r.error.code, "timeout");
  assert.deepEqual(calls.map((c) => c[0]), ["stop"]);
});

test("stop and restart use the selected node; failed migration or wrong version never reports startup success", async () => {
  for (const op of ["stop", "restart"] as const) {
    const { service, calls } = fixture({ running: true });
    assert.equal((await service.request({ op, node: "state-dir" })).ok, true);
    assert.deepEqual(calls.map((c) => c[0]), op === "stop" ? ["stop"] : ["stop", "migrate", "start"]);
  }
  const failed = fixture({ fail: "migrate" });
  assert.equal((await failed.service.request({ op: "start", node: "state-dir" })).ok, false);
  assert.deepEqual(failed.calls.map((c) => c[0]), ["migrate"]);
  const wrong = fixture({ version: "0.9.0" });
  assert.equal((await wrong.service.request({ op: "start", node: "state-dir" })).ok, false);
});

test("a missing service can be reinstalled, but access-denied failures are retained", async () => {
  for (const denied of [false, true]) {
    const { target } = fixture();
    const calls: string[] = [];
    const service = new KernelLifecycle({ targets: () => [target], resolve: async () => target, changed: () => undefined,
      run: async (args) => {
        calls.push(args[0]!);
        return ["stop", "uninstall"].includes(args[0]!)
          ? { code: 1, stdout: denied ? "" : '{"error":{"reason":"not_installed"}}', stderr: "denied" }
          : { code: 0, stdout: "", stderr: "" };
      }, status: async () => ({ control: { kernel_version: "1.0.0" } }), timeoutMs: 8, pollMs: 1 });
    assert.equal((await service.request({ op: "reinstall", node: "state-dir" })).ok, !denied);
    if (denied) assert.deepEqual(calls, ["migrate", "uninstall"]);
    else assert.ok(calls.includes("install"));
  }
});

test("a second request cannot race service replacement on the same node", async () => {
  const { target } = fixture();
  let release!: () => void;
  const blocked = new Promise<void>((r) => { release = r; });
  const service = new KernelLifecycle({ targets: () => [target], resolve: async () => { await blocked; return target; },
    run: async () => ({ code: 0, stdout: "", stderr: "" }), changed: () => undefined });
  const first = service.request({ op: "install", node: target.id });
  assert.equal(service.busy, true);
  const second = await service.request({ op: "uninstall", node: target.id });
  assert.equal(second.ok, false);
  if (!second.ok) assert.equal(second.error.code, "busy");
  release();
  assert.equal((await first).ok, true);
  assert.equal(service.busy, false);
});

test("updates block all mutations while leaving node discovery available", async () => {
  const { target } = fixture();
  const service = new KernelLifecycle({ targets: () => [target], blocked: () => true,
    resolve: async () => { throw new Error("must not resolve during update"); },
    run: async () => { throw new Error("must not execute during update"); }, changed: () => undefined });
  for (const op of ["install", "uninstall", "reinstall", "start", "stop", "restart"]) {
    const result = await service.request({ op, node: target.id });
    assert.ok(!result.ok && result.error.code === "busy");
    assert.equal(service.busy, false);
  }
  assert.equal((await service.request({ op: "list" })).ok, true);
});

test("Windows saved custom paths survive case differences after shutdown", { skip: process.platform !== "win32" }, (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-case-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const paths = { configFile: path.join(root, "custom.yaml"), stateDir: path.join(root, "office").toUpperCase() };
  fs.mkdirSync(paths.stateDir);
  fs.writeFileSync(paths.configFile, "custom configuration");
  const targets = discoverKernelTargets([root], () => paths, [], ["office"]);
  assert.equal(targets.length, 1);
  assert.equal(targets[0]?.configFile, paths.configFile);
});

test("restart targets the original registration after a configuration rename", async () => {
  const { service, target, calls } = fixture({ running: true });
  target.serviceNode = "original-service";
  assert.equal((await service.request({ op: "restart", node: target.id })).ok, true);
  for (const args of calls.filter((args) => ["stop", "start"].includes(args[0]!))) {
    assert.equal(args[args.indexOf("--name") + 1], "xmesh-original-service");
  }
});

test("a stale recorded service name falls back to the configuration's own node name and is corrected", async () => {
  const { target } = fixture({ running: true });
  target.serviceNode = "stale-name";
  const calls: string[][] = [];
  const recorded: string[] = [];
  let exited = false;
  const service = new KernelLifecycle({ targets: () => [target], resolve: async (node) => node,
    installed: async () => { recorded.push("config-name"); return "config-name"; },
    run: async (args) => {
      calls.push([...args]);
      // Only the config-derived name exists; the recorded one is not a service.
      if (args.includes("--name") && args[args.indexOf("--name") + 1] !== "xmesh-config-name") return { code: 1, stdout: '{"error":{"reason":"not_installed"}}', stderr: "服务未安装" };
      if (args[0] === "stop") { exited = true; target.running = false; }
      if (args[0] === "start") { exited = false; target.running = true; }
      return { code: 0, stdout: "", stderr: "" };
    },
    changed: () => undefined, gone: () => exited,
    status: async () => ({ control: { kernel_version: "1.0.0" } }), version: "1.0.0", timeoutMs: 8, pollMs: 1 });
  assert.equal((await service.request({ op: "restart", node: target.id })).ok, true);
  const stop = calls.filter((args) => args[0] === "stop");
  assert.equal(stop.length, 2, "tried the recorded name, then the configuration");
  assert.equal(stop[0]!.includes("--name"), true);
  assert.equal(stop[1]!.includes("--name"), false);
  assert.deepEqual(recorded, ["config-name"], "the working name is recorded for the next request");
  const start = calls.find((args) => args[0] === "start")!;
  assert.equal(start[start.indexOf("--name") + 1], "xmesh-config-name", "start uses the corrected name straight away");
});

test("a service missing under both names is still reported as failed", async () => {
  const { target } = fixture({ running: true });
  target.serviceNode = "stale-name";
  const service = new KernelLifecycle({ targets: () => [target], resolve: async (node) => node, changed: () => undefined,
    run: async () => ({ code: 1, stdout: '{"error":{"error":"not_found","message":"服务未安装: xmesh-x（先运行 xmesh install）","reason":"not_installed"}}', stderr: "" }) });
  const result = await service.request({ op: "restart", node: target.id });
  assert.ok(!result.ok && result.error.message.includes("服务未安装"));
});

test("reinstall captures service options before stopping and starts the newly named registration", async () => {
  const { target } = fixture({ running: true });
  const calls: string[][] = [];
  const password = path.resolve("custom/password-file");
  const service = new KernelLifecycle({ targets: () => [target], resolve: async () => ({ ...target, serviceNode: "old-name" }),
    serviceOptions: async () => { calls.push(["inspect"]); return ["--oauth-password-file", password]; },
    installed: async () => "new-name", changed: () => undefined, gone: () => true,
    status: async () => ({ control: { kernel_version: "1.0.0" } }),
    run: async args => { calls.push([...args]); return { code: 0, stdout: "", stderr: "" }; },
  });
  assert.equal((await service.request({ op: "reinstall", node: target.id })).ok, true);
  assert.equal(calls[0]?.[0], "inspect");
  assert.ok(calls.find(args => args[0] === "install")?.includes(password));
  for (const op of ["stop", "uninstall", "start"]) {
    const args = calls.find(args => args[0] === op)!;
    assert.equal(args[args.indexOf("--name") + 1], op === "start" ? "xmesh-new-name" : "xmesh-old-name");
  }
});

test("saved custom configurations remain manageable after their control manifest is removed", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-targets-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const paths = { configFile: path.join(root, "custom.yaml"), stateDir: path.join(root, "office") };
  fs.mkdirSync(paths.stateDir);
  fs.writeFileSync(paths.configFile, "custom configuration");
  const targets = discoverKernelTargets([root], () => paths, [], ["office"]);
  assert.equal(targets.length, 1);
  assert.equal(targets[0]?.configFile, paths.configFile);
  assert.equal(targets[0]?.running, false);
});

test("a broken user manifest cannot make a discovered system node manageable", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-protected-target-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const roots = [path.join(root, "user"), path.join(root, "system")];
  for (const base of roots) fs.mkdirSync(path.join(base, "office"), { recursive: true });
  fs.writeFileSync(path.join(roots[0]!, "office/control.json"), "{broken");
  fs.writeFileSync(path.join(roots[1]!, "office/control.json"), JSON.stringify({
    api_version: "2.0", kernel_version: "1.0.0", node: "office", pid: 0, boot_id: "ab",
    transport: "unix", endpoint: "/tmp/system.sock", started_at: "2026-10-20T00:00:00Z", capabilities: [],
  }));
  const targets = discoverKernelTargets(roots, () => undefined, [roots[1]!]);
  assert.equal(targets.length, 1);
  assert.equal(targets[0]?.managed, false);
});

test("stopped nodes uninstall and reinstall without invoking stop", async () => {
  for (const op of ["uninstall", "reinstall"] as const) {
    const { service, calls } = fixture({ fail: "stop" });
    assert.equal((await service.request({ op, node: "state-dir" })).ok, true);
    assert.ok(!calls.some(args => args[0] === "stop"));
  }
});

test("failed migration preserves the stopped service registration", async () => {
  const { service, calls } = fixture({ running: true, fail: "migrate" });
  assert.equal((await service.request({ op: "reinstall", node: "state-dir" })).ok, false);
  assert.deepEqual(calls.map(args => args[0]), ["stop", "migrate"]);
});

test("custom instance IDs with underscores and dots remain manageable after shutdown", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-instance-id-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, "office_1.test"));
  fs.writeFileSync(path.join(root, "office_1.test/xmesh.yaml"), "node: { name: office }");
  assert.deepEqual(parseKernelRequest({ op: "start", node: "office_1.test" }), { op: "start", node: "office_1.test" });
  assert.equal(discoverKernelTargets([root], () => undefined, [])[0]?.id, "office_1.test");
});

test("actual registration scope overrides the state-root hint and unknown scope prevents changes", async () => {
  for (const scope of ["system", "user", "unknown"] as const) {
    const { target } = fixture({ managed: scope === "system" });
    let commands = 0;
    const service = new KernelLifecycle({ targets: () => [target], resolve: async () => target,
      managed: async () => { if (scope === "unknown") throw new Error("denied"); return scope === "user"; },
      run: async () => { commands++; return { code: 0, stdout: "", stderr: "" }; }, changed: () => undefined });
    const list = await service.request({ op: "list" });
    assert.ok(list.ok && list.data.nodes[0]?.managed === (scope === "user"));
    assert.equal((await service.request({ op: "install", node: target.id })).ok, scope === "user");
    assert.equal(commands > 0, scope === "user");
  }
});

test("stale and unresponsive custom-config instances recover and retain their registered paths", async () => {
  for (const running of [false, true]) {
    const { target } = fixture({ running });
    let captured = false;
    const actual = { configFile: path.resolve("external/real.yaml"), stateDir: target.stateDir, serviceNode: "original" };
    let remembered: unknown;
    const paths = await recoverKernelPaths(target, "original", async () => { captured = true; throw new Error("control unavailable"); },
      async () => ({ config_file: actual.configFile, state_dir: actual.stateDir }), value => { remembered = value; });
    assert.deepEqual(paths, actual);
    assert.deepEqual(remembered, actual);
    assert.equal(captured, running, "a stale process does not need a control request");
  }
});
