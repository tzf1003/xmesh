import assert from "node:assert/strict";
import { test } from "node:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import * as http from "node:http";
import { once } from "node:events";
import { ComputerEmergencyTargets, ComputerUpdateTarget, computerHostServices, type ComputerHostStatus } from "../../src/main/computer-host";
import type { Instance } from "../../src/main/discovery";
import { randomEndpoint } from "../../testserver/server";

test("updating a desktop host waits for process exit and verifies the restored version", async () => {
  let current: ComputerHostStatus = { running: true, pid: 10, version: "v0.1.0" };
  let exited = false;
  let probes = 0;
  const commands: string[] = [];
  const services = computerHostServices({
    version: "0.2.0", timeoutMs: 100, pollMs: 1,
    gone: () => { if (++probes >= 2) exited = true; return exited; },
    run: async (command) => {
      commands.push(command);
      if (command === "status") return current;
      if (command === "stop") current = { running: false, pid: 0, version: "" };
      if (command === "start") current = { running: true, pid: 20, version: "v0.2.0" };
    },
  });
  assert.equal(await services.stop(), "stopped");
  assert.equal(exited, true);
  await services.start();
  assert.deepEqual(commands, ["status", "stop", "status", "start", "status"]);
});

test("desktop replacement is blocked while the old process survives", async () => {
  const service = computerHostServices({
    version: "0.2.0", timeoutMs: 5, pollMs: 1, gone: () => false,
    run: async (command) => command === "status" ? { running: true, pid: 10, version: "0.1.0" } : undefined,
  });
  await assert.rejects(service.stop(), /旧桌面服务仍在运行/);
  await assert.rejects(service.start(), /运行版本或本机通道未就绪/);
});

test("an absent desktop host stays stopped and probe failures remain visible", async () => {
  const commands: string[] = [];
  const service = computerHostServices({ version: "0.2.0", run: async (command) => {
    commands.push(command); return { running: false, pid: 0, version: "" };
  } });
  assert.equal(await service.stop(), "absent");
  assert.deepEqual(commands, ["status"]);
  await assert.rejects(computerHostServices({ version: "0.2.0", run: async () => { throw new Error("identity mismatch"); } }).stop(), /identity mismatch/);
});

test("replacement also waits for the native child after the host exits", async () => {
  const service = computerHostServices({ version: "0.2.0", timeoutMs: 5, pollMs: 1,
    gone: (pid) => pid === 10,
    run: async (command) => command === "status" ? { running: true, pid: 10, native_pid: 11, version: "0.1.0" } : undefined,
  });
  await assert.rejects(service.stop(), /旧桌面服务仍在运行/);
});

test("emergency stop retains the selected custom endpoint through kernel loss", () => {
  const targets = new ComputerEmergencyTargets();
  const snapshot = {
    selectedInstance: "custom",
    connection: { kind: "connected", status: { paths: { computer_endpoint: "/tmp/custom-computer.sock" } } },
  } as any;
  assert.equal(targets.endpoint(snapshot), "/tmp/custom-computer.sock");
  snapshot.connection = { kind: "offline" };
  assert.equal(targets.endpoint(snapshot), "/tmp/custom-computer.sock");
  snapshot.selectedInstance = "other";
  assert.throws(() => targets.endpoint(snapshot), /尚未提供/);
  snapshot.connection = { kind: "connected", status: { paths: { computer_endpoint: "/tmp/other.sock" } } };
  assert.equal(targets.endpoint(snapshot), "/tmp/other.sock");
  snapshot.selectedInstance = "custom";
  snapshot.connection = { kind: "offline" };
  assert.equal(targets.endpoint(snapshot), "/tmp/custom-computer.sock");
});

test("updating retains the kernel's custom Host endpoint through desktop replacement", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-computer-update-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  for (const major of ["1", "2"]) {
    const endpoint = randomEndpoint(), custom = path.join(dir, "custom-host.sock");
    let publishedEndpoint: unknown = custom;
    const instance = { id: "office", manifest: { pid: 10, endpoint } } as Instance;
    const server = http.createServer((req, res) => {
      res.setHeader("X-Xmesh-Control-Version", `${major}.0`);
      if (req.url !== `/v${major}/status`) { res.writeHead(404).end(); return; }
      res.end(JSON.stringify({ paths: { computer_endpoint: publishedEndpoint } }));
    });
    server.listen(endpoint); await once(server, "listening");
    try {
      const file = path.join(dir, `target-${major}.json`);
      const before = new ComputerUpdateTarget(file, { discover: () => [instance], gone: () => false });
      let running = true;
      const commands: string[] = [];
      const services = computerHostServices({ version: "0.2.0", beforeStop: () => before.capture(),
        gone: () => !running, run: async command => {
          assert.equal(before.endpoint(), custom);
          commands.push(command);
          if (command === "stop") running = false;
          return { running, pid: running ? 10 : 0, version: "0.1.0" };
        } });
      assert.equal(await services.stop(), "stopped");
      assert.ok(commands.includes("stop"));
      publishedEndpoint = null;
      await assert.rejects(before.capture(), /无效的桌面服务端点/);
      assert.equal(before.endpoint(), custom);
      const after = new ComputerUpdateTarget(file, { discover: () => [] });
      await computerHostServices({ version: "0.2.0", run: async command => {
        assert.equal(after.endpoint(), custom);
        if (command === "start") running = true;
        return { running, pid: 20, version: "0.2.0" };
      } }).start();
    } finally { await new Promise<void>(resolve => server.close(() => resolve())); }
  }
});

test("update endpoint discovery failures preserve running Hosts and retained targets", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-computer-update-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const file = path.join(dir, "target.json");
  fs.writeFileSync(file, JSON.stringify({ endpoint: "/retained.sock" }));
  const instances = ["a", "b"].map(id => ({ id, manifest: { pid: 10 } } as Instance));
  const target = new ComputerUpdateTarget(file, { discover: () => instances, gone: () => false,
    readEndpoint: async instance => `/${instance.id}.sock` });
  const services = computerHostServices({ version: "0.2.0", beforeStop: () => target.capture(),
    run: async () => { throw new Error("must keep service running"); } });
  await assert.rejects(services.stop(), /不同桌面服务端点/);
  assert.equal(target.endpoint(), "/retained.sock");
  const unreadable = new ComputerUpdateTarget(file, { discover: () => instances, gone: () => false,
    readEndpoint: async () => { throw new Error("kernel unavailable"); } });
  await assert.rejects(unreadable.capture(), /kernel unavailable/);
  assert.equal(unreadable.endpoint(), "/retained.sock");
});

test("a live kernel can replace a retained custom endpoint with the default Host", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-computer-update-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const file = path.join(dir, "target.json");
  fs.writeFileSync(file, JSON.stringify({ endpoint: "/previous.sock" }));
  const target = new ComputerUpdateTarget(file, { discover: () => [{ manifest: { pid: 10 } } as Instance], gone: () => false,
    readEndpoint: async () => undefined });
  await target.capture(); assert.equal(target.endpoint(), undefined);
  fs.writeFileSync(file, "{}");
  assert.throws(() => target.endpoint(), /无效/);
});

test("an update retains and restores a custom Host after every kernel exits", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-computer-update-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  for (const discover of [() => [], () => [{ manifest: { pid: 10 } } as Instance]]) {
    const file = path.join(dir, "target.json"), endpoint = "/retained-custom.sock";
    fs.writeFileSync(file, JSON.stringify({ endpoint }));
    const target = new ComputerUpdateTarget(file, { discover, gone: () => true,
      readEndpoint: async () => { throw new Error("exited kernel must not be read"); } });
    let running = true;
    const service = computerHostServices({ version: "0.2.0", beforeStop: () => target.capture(),
      gone: () => !running, run: async command => {
        assert.equal(target.endpoint(), endpoint);
        if (command === "stop") running = false;
        return { running, pid: running ? 20 : 0, version: "0.1.0" };
      } });
    assert.equal(await service.stop(), "stopped");
    const restored = new ComputerUpdateTarget(file, { discover: () => [] });
    await computerHostServices({ version: "0.2.0", run: async command => {
      assert.equal(restored.endpoint(), endpoint);
      if (command === "start") running = true;
      return { running, pid: 30, version: "0.2.0" };
    } }).start();
    assert.equal(running, true);
  }
  const missing = path.join(dir, "missing.json");
  const target = new ComputerUpdateTarget(missing, { discover: () => [] });
  await target.capture();
  assert.equal(target.endpoint(), undefined);
  assert.equal(fs.existsSync(missing), false);
});
