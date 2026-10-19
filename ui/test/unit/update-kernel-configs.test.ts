import assert from "node:assert/strict";
import { test } from "node:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import * as http from "node:http";
import { once } from "node:events";
import { KernelConfigs } from "../../src/main/update/kernel-configs";
import { kernelServices } from "../../src/main/update/kernels";
import { ControlClient } from "../../src/main/client";
import type { Instance } from "../../src/main/discovery";
import { randomEndpoint } from "../../testserver/server";

test("an update retains a custom service configuration across desktop restart", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-upgrade-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const configFile = path.join(dir, "custom", "service.yaml"), stateDir = path.join(dir, "state", "office");
  const file = path.join(dir, "desktop", "kernel-configs.json");
  const instance = { id: "office", manifest: { pid: 12345, endpoint: "unused" } } as Instance;
  const calls: string[] = [];
  const before = new KernelConfigs(file, async () => "", async () => ({ configFile, stateDir }));
  let stopped = false;
  const services = kernelServices({
    discover: () => [instance], gone: () => stopped,
    beforeStop: (node, instance) => before.capture(node, instance),
    run: async () => { assert.ok(fs.existsSync(file)); stopped = true; return { code: 0, stdout: "", stderr: "" }; },
  });
  assert.equal(await services.stop("office"), "stopped");
  const after = new KernelConfigs(file, async (args) => { calls.push(JSON.stringify(args)); return "migrated"; });
  await after.prepare("office");
  assert.deepEqual(calls, [JSON.stringify(["migrate", "--config", configFile, "--state-dir", stateDir])]);
  const missing = new KernelConfigs(path.join(dir, "absent.json"), async (args) => { calls.push(JSON.stringify(args)); return "migrated"; });
  await missing.prepare("office");
  assert.equal(calls[1], JSON.stringify(["migrate", "--name", "xmesh-office"]));
});

test("a service remains running when its upgrade paths cannot be retained", async () => {
  const services = kernelServices({
    discover: () => [{ id: "office", manifest: { pid: 12345 } } as Instance], gone: () => false,
    beforeStop: async () => { throw new Error("config snapshot failed"); },
    run: async () => { throw new Error("must keep service running"); },
  });
  await assert.rejects(services.stop("office"), /config snapshot failed/);
});

test("upgrade paths can be read through v1 and v2 status before service shutdown", async () => {
  for (const major of ["1", "2"]) {
    const endpoint = randomEndpoint();
    const paths = { config_file: path.resolve("custom.yaml"), state_dir: path.resolve("state") };
    const routes: string[] = [];
    const server = http.createServer((req, res) => {
      routes.push(req.url!);
      res.setHeader("X-Xmesh-Control-Version", `${major}.0`);
      if (req.url !== `/v${major}/status`) { res.writeHead(404).end(); return; }
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ paths }));
    });
    server.listen(endpoint);
    await once(server, "listening");
    try {
      assert.deepEqual(await new ControlClient({ endpoint }).getUpgradePaths(), { configFile: paths.config_file, stateDir: paths.state_dir });
      assert.deepEqual(routes, major === "1" ? ["/v2/status", "/v1/status"] : ["/v2/status"]);
    } finally {
      await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
    }
  }
});
