import assert from "node:assert/strict";
import { test } from "node:test";
import { ComputerEmergencyTargets, computerHostServices, type ComputerHostStatus } from "../../src/main/computer-host";

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
