import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { PlaygroundService, localGatewayUrl } from "../../src/main/playground";
import { historyArgs, parsePlayground } from "../../src/shared/playground";
import { argumentError } from "../../src/renderer/lib/playground";
import type { ShellController } from "../../src/main/controller";
import { EMPTY_SNAPSHOT } from "../../src/shared/state";
import { statusFixture } from "../../testserver/fixtures";
import { FakePlayground } from "../../testserver/playground";

test("playground IPC and destination validation constrain instance, shape, payload and URL", () => {
  assert.ok(parsePlayground({ op: "list", instance: "node", device: "local" }));
  assert.ok(parsePlayground({ op: "call", instance: "node", id: "c1", name: "local/exec.run", arguments: { cmd: "pwd" }, timeoutMs: 1000 }));
  for (const v of [null, {}, { op: "status", instance: "../x" }, { op: "list", instance: "n", device: "x", url: "http://evil/" }, { op: "call", instance: "n", id: "a", name: "x", arguments: [], timeoutMs: 1000 }, { op: "call", instance: "n", id: "a", name: "x", arguments: { data: "é".repeat(140_000) }, timeoutMs: 1000 }]) assert.equal(parsePlayground(v), undefined);
  for (const u of ["https://evil/mcp", "http://127.0.0.1.evil/mcp", "http://u:p@127.0.0.1/mcp", "http://127.0.0.1/mcp?x=1", "http://127.0.0.1/other"]) assert.throws(() => localGatewayUrl(u));
});

test("history recursively redacts secrets and schema checks work under a strict CSP", () => {
  assert.deepEqual(historyArgs({ cmd: "x", env: { A: "secret" }, nested: [{ api_key: "secret", message: "hello" }], stdin: "secret" }), { cmd: "x", env: "[redacted]", nested: [{ api_key: "[redacted]", message: "hello" }], stdin: "[redacted]" });
  const tool = { name: "demo", inputSchema: { type: "object", required: ["count"], properties: { count: { type: "integer", minimum: 1 } } } };
  assert.ok(argumentError(tool, "[]"));
  assert.ok(argumentError(tool, "{}"));
  assert.ok(argumentError(tool, '{"count":0}'));
  assert.equal(argumentError(tool, '{"count":2}'), undefined);
});

test("production MCP path reuses PAT, stamps origin, handles errors, cancellation, changes and token rotation", async () => {
  let token = "a".repeat(40);
  const gateway = new FakePlayground(() => token);
  await gateway.start();
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-playground-"));
  const file = path.join(dir, "default.pat"); fs.writeFileSync(file, token, { mode: 0o600 });
  const status = statusFixture(); status.roles = ["agent", "gateway"]; status.gateway = { listen: "127.0.0.1:1", url: gateway.url, auth: "pat", addresses: [], access: ["localhost"], hosts: [] };
  const snapshot = { ...EMPTY_SNAPSHOT, selectedInstance: "node", connection: { kind: "connected" as const, status, peers: [] } };
  const controller = { snapshot, requireCapable: () => ({ call: async () => ({ pats: [{ name: "default", token_file: file }] }) }) } as unknown as Pick<ShellController, "snapshot" | "requireCapable">;
  const service = new PlaygroundService(controller);
  try {
    const early = service.request({ op: "call", instance: "node", id: "early", name: "local/demo.echo", arguments: { message: "cancel-before-connect" }, timeoutMs: 5000 });
    const cancelEarly = await service.request({ op: "cancel", instance: "node", id: "early" }); assert.ok(cancelEarly.ok); assert.equal(cancelEarly.data.cancelled, true);
    assert.equal((await early).ok, false); assert.equal(gateway.calls.some((x) => x.args.message === "cancel-before-connect"), false);
    const list = await service.request({ op: "list", instance: "node", device: "local" }); assert.ok(list.ok); assert.equal(list.data.tools.length, 5);
    const info = await service.request({ op: "call", instance: "node", id: "info", name: "local/sys.info", arguments: {}, timeoutMs: 5000 }); assert.ok(info.ok); assert.equal(info.data.result.structuredContent?.hostname, "fixture-device");
    assert.ok(gateway.calls.every((x) => x.origin === "panel")); assert.equal(JSON.stringify(info).includes(token), false);
    const denied = await service.request({ op: "call", instance: "node", id: "deny", name: "local/demo.denied", arguments: {}, timeoutMs: 5000 }); assert.ok(denied.ok); assert.equal(denied.data.result.isError, true);
    await gateway.changed(); await new Promise((r) => setTimeout(r, 50));
    const state = await service.request({ op: "status", instance: "node" }); assert.ok(state.ok); assert.ok(state.data.revision > list.data.revision);
    const slow = service.request({ op: "call", instance: "node", id: "slow", name: "local/demo.slow", arguments: {}, timeoutMs: 5000 });
    while (!gateway.calls.some((x) => x.name.endsWith("slow"))) await new Promise((r) => setTimeout(r, 10));
    const cancel = await service.request({ op: "cancel", instance: "node", id: "slow" }); assert.ok(cancel.ok); assert.equal(cancel.data.cancelled, true);
    const cancelled = await slow; assert.equal(cancelled.ok, false);
    token = "b".repeat(40); fs.writeFileSync(file, token);
    assert.ok((await service.request({ op: "list", instance: "node", device: "local" })).ok);
    assert.equal((await service.request({ op: "list", instance: "another", device: "local" })).ok, false);
  } finally { await service.close(); await gateway.stop(); fs.rmSync(dir, { recursive: true, force: true }); }
});
