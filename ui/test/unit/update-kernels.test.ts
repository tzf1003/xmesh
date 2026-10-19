import assert from "node:assert/strict";
import { test } from "node:test";
import { FakeKernel } from "../../testserver/server";
import type { Instance } from "../../src/main/discovery";
import { kernelServices } from "../../src/main/update/kernels";

const instance: Instance = {
  id: "mac",
  manifest: {
    api_version: "1.7", kernel_version: "v0.1.0-beta.3", node: "mac", pid: 12345,
    boot_id: "abcd", transport: "unix", endpoint: "/tmp/test-update.sock",
    started_at: "2026-10-19T00:00:00.000Z", capabilities: ["status"],
  },
};

test("a live kernel whose service cannot be found blocks replacement", async () => {
  const k = kernelServices({
    discover: () => [instance], gone: () => false,
    run: async () => ({ code: 1, stdout: '{"error":{"reason":"not_installed"}}', stderr: "服务未安装" }),
  });
  await assert.rejects(k.stop("mac"), /无法停止内核 mac/);
});

test("stop waits for the old process to exit after the service command returns", async () => {
  let stopped = false;
  let probes = 0;
  const k = kernelServices({
    discover: () => [instance],
    gone: (pid) => { assert.equal(pid, 12345); return stopped && ++probes >= 2; },
    run: async (command, node) => { assert.equal(command, "stop"); assert.equal(node, "mac"); stopped = true; return { code: 0, stdout: "", stderr: "" }; },
    timeoutMs: 100, pollMs: 1,
  });
  assert.equal(await k.stop("mac"), "stopped");
  assert.equal(probes, 2);
});

test("a successful stop command with a surviving process blocks replacement", async () => {
  const k = kernelServices({
    discover: () => [instance], gone: () => false,
    run: async () => ({ code: 0, stdout: "", stderr: "" }), timeoutMs: 5, pollMs: 1,
  });
  await assert.rejects(k.stop("mac"), /旧进程仍在运行/);
});

test("a kernel that already exited needs no service action", async () => {
  const k = kernelServices({
    discover: () => [instance], gone: () => true,
    run: async () => { throw new Error("unexpected service command"); },
  });
  assert.equal(await k.stop("mac"), "absent");
  assert.equal(await k.stop("missing"), "absent");
});

test("start waits for the new kernel to answer with the bundled version", async () => {
  let probes = 0;
  const k = kernelServices({
    discover: () => [instance], version: "0.1.0-beta.4",
    run: async () => ({ code: 0, stdout: "", stderr: "" }),
    status: async () => {
      if (++probes === 1) throw new Error("channel not ready");
      return { control: { kernel_version: probes === 2 ? "v0.1.0-beta.3" : "v0.1.0-beta.4" } };
    }, timeoutMs: 100, pollMs: 1,
  });
  await k.start("mac");
  assert.equal(probes, 3);
});

test("start refuses a kernel that keeps reporting the previous version", async () => {
  const k = kernelServices({
    discover: () => [instance], version: "0.1.0-beta.4",
    run: async () => ({ code: 0, stdout: "", stderr: "" }),
    status: async () => ({ control: { kernel_version: "v0.1.0-beta.3" } }), timeoutMs: 5, pollMs: 1,
  });
  await assert.rejects(k.start("mac"), /运行版本 v0.1.0-beta.3，预期 0.1.0-beta.4/);
});

test("an unavailable control channel and a failed service start are reported", async () => {
  const env = { discover: () => [] as Instance[], run: async () => ({ code: 0, stdout: "", stderr: "" }), timeoutMs: 5, pollMs: 1 };
  await assert.rejects(kernelServices(env).start("mac"), /控制通道未就绪/);
  await assert.rejects(kernelServices({ ...env, run: async () => ({ code: 1, stdout: "", stderr: "service failed" }) }).start("mac"), /service failed/);
});

test("startup verification reads the version from the actual control socket", async () => {
  const server = await FakeKernel.start({ kernelVersion: "v0.1.0-beta.4" });
  try {
    const k = kernelServices({
      discover: () => [{ ...instance, manifest: { ...instance.manifest, endpoint: server.endpoint } }],
      version: "0.1.0-beta.4", run: async () => ({ code: 0, stdout: "", stderr: "" }), timeoutMs: 1000,
    });
    await k.start("mac");
    assert.ok(server.requests.some((r) => r.path === "/v1/status"));
  } finally {
    await server.stop();
  }
});

test("refresh replaces a surviving old kernel and verifies its new version", async () => {
  const calls: string[] = [];
  let gone = false;
  let version = "v0.1.0-beta.3";
  const k = kernelServices({
    discover: () => [instance], version: "0.1.0-beta.4", gone: () => gone,
    status: async () => ({ control: { kernel_version: version } }),
    run: async (command) => {
      calls.push(command);
      gone = command === "stop";
      if (command === "start") version = "v0.1.0-beta.4";
      return { code: 0, stdout: "", stderr: "" };
    },
  });
  await k.refresh("mac");
  assert.deepEqual(calls, ["stop", "start"]);
  await k.refresh("mac");
  assert.deepEqual(calls, ["stop", "start"], "a current kernel keeps running");
  gone = true;
  await k.refresh("mac");
  assert.deepEqual(calls, ["stop", "start"], "an exited kernel is not brought back");
});
