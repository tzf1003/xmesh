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
  assert.ok(parsePlayground({ op: "call", instance: "node", id: "c1", name: "local/computer.info", arguments: {}, timeoutMs: 1000, pat: "desktop-watch" }));
  assert.equal(parsePlayground({ op: "call", instance: "node", id: "c1", name: "local/computer.info", arguments: {}, timeoutMs: 1000, pat: "../token" }), undefined);
  for (const v of [null, {}, { op: "status", instance: "../x" }, { op: "list", instance: "n", device: "x", url: "http://evil/" }, { op: "call", instance: "n", id: "a", name: "x", arguments: [], timeoutMs: 1000 }, { op: "call", instance: "n", id: "a", name: "x", arguments: { data: "é".repeat(140_000) }, timeoutMs: 1000 }]) assert.equal(parsePlayground(v), undefined);
  for (const u of ["https://evil/mcp", "http://127.0.0.1.evil/mcp", "http://u:p@127.0.0.1/mcp", "http://127.0.0.1/mcp?x=1", "http://127.0.0.1/other"]) assert.throws(() => localGatewayUrl(u));
});

test("named desktop credentials use independent MCP sessions while default calls remain active", async () => {
  const tokens = ["a".repeat(40), "b".repeat(40)];
  const gateway = new FakePlayground(() => tokens);
  await gateway.start();
  gateway.enableDesktop();
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-preview-creds-"));
  const pats = ["default", "desktop-watch"].map((name, i) => {
    const file = path.join(dir, name + ".token"); fs.writeFileSync(file, tokens[i]!, { mode: 0o600 });
    return { name, token_file: file };
  });
  const status = statusFixture(); status.roles = ["agent", "gateway"]; status.gateway = { listen: "127.0.0.1:1", url: gateway.url, auth: "pat", addresses: [], access: ["localhost"], hosts: [] };
  const controller = { snapshot: { ...EMPTY_SNAPSHOT, selectedInstance: "node", connection: { kind: "connected", status, peers: [] } }, requireCapable: () => ({ call: async () => ({ pats }) }) } as unknown as Pick<ShellController, "snapshot" | "requireCapable">;
  const service = new PlaygroundService(controller);
  try {
    const slow = service.request({ op: "call", instance: "node", id: "slow-default", name: "local/demo.slow", arguments: {}, timeoutMs: 5000 });
    for (let n = 0; !gateway.calls.some((c) => c.name.endsWith("slow")) && n < 100; n++) await new Promise((r) => setTimeout(r, 10));
    assert.equal(gateway.sessions.size, 1);
    const first = [...gateway.sessions.keys()][0]!;
    const preview = await service.request({ op: "call", instance: "node", id: "preview", name: "local/computer.info", arguments: {}, timeoutMs: 5000, pat: "desktop-watch" });
    assert.equal(preview.ok, true);
    assert.equal(gateway.sessions.size, 2);
    assert.equal(gateway.sessions.has(first), true);
    assert.equal((await service.request({ op: "call", instance: "node", id: "missing", name: "local/computer.info", arguments: {}, timeoutMs: 5000, pat: "missing" })).ok, false);
    assert.equal(gateway.sessions.size, 2);
    await service.request({ op: "cancel", instance: "node", id: "slow-default" });
    assert.equal((await slow).ok, false);
  } finally { await service.close(); await gateway.stop(); fs.rmSync(dir, { recursive: true, force: true }); }
});

test("history recursively redacts secrets and schema checks work under a strict CSP", () => {
  assert.deepEqual(historyArgs({ cmd: "x", env: { A: "secret" }, nested: [{ api_key: "secret", message: "hello" }], stdin: "secret" }), { cmd: "[redacted]", env: { A: "[redacted]" }, nested: [{ api_key: "[redacted]", message: "[redacted]" }], stdin: "[redacted]" });
  const tool = { name: "demo", inputSchema: { type: "object", required: ["count"], properties: { count: { type: "integer", minimum: 1 } } } };
  assert.ok(argumentError(tool, "[]"));
  assert.ok(argumentError(tool, "{}"));
  assert.ok(argumentError(tool, '{"count":0}'));
  assert.equal(argumentError(tool, '{"count":2}'), undefined);
});

async function credentialPool() {
  const tokens = Array.from({ length: 13 }, (_, i) => String.fromCharCode(97 + i).repeat(40));
  const gateway = new FakePlayground(() => tokens);
  await gateway.start();
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-preview-pool-"));
  const pats = tokens.map((token, i) => {
    const name = i === 0 ? "default" : `credential-${i}`;
    const file = path.join(dir, name + ".token");
    fs.writeFileSync(file, token, { mode: 0o600 });
    return { name, token_file: file };
  });
  const status = statusFixture(); status.roles = ["agent", "gateway"]; status.gateway = { listen: "127.0.0.1:1", url: gateway.url, auth: "pat", addresses: [], access: ["localhost"], hosts: [] };
  const controller = { snapshot: { ...EMPTY_SNAPSHOT, selectedInstance: "node", connection: { kind: "connected", status, peers: [] } }, requireCapable: () => ({ call: async () => ({ pats }) }) } as unknown as Pick<ShellController, "snapshot" | "requireCapable">;
  const service = new PlaygroundService(controller);
  return { gateway, service, rotateDefault() { fs.writeFileSync(pats[0]!.token_file, tokens[1]!, { mode: 0o600 }); }, async close() { await service.close(); await gateway.stop(); fs.rmSync(dir, { recursive: true, force: true }); } };
}

test("credential pool evicts idle sessions while a default call stays active", async () => {
  const pool = await credentialPool();
  const { service, gateway } = pool;
  try {
    const slow = service.request({ op: "call", instance: "node", id: "pinned", name: "local/demo.slow", arguments: {}, timeoutMs: 10000 });
    for (let n = 0; gateway.calls.length < 1 && n < 200; n++) await new Promise((r) => setTimeout(r, 10));
    assert.equal(gateway.calls.length, 1);
    const pinned = [...gateway.sessions.keys()][0]!;
    for (let i = 1; i <= 12; i++) {
      const result = await service.request({ op: "call", instance: "node", id: `preview-${i}`, name: "local/sys.info", arguments: {}, timeoutMs: 5000, pat: `credential-${i}` });
      assert.equal(result.ok, true, JSON.stringify(result));
      assert.ok(gateway.sessions.size <= 8);
      assert.ok(gateway.sessions.has(pinned), "active default session was evicted");
    }
    assert.equal(gateway.sessions.size, 8);
    await service.request({ op: "cancel", instance: "node", id: "pinned" });
    assert.equal((await slow).ok, false);
  } finally { await pool.close(); }
});

test("credential pool preserves eight active sessions and reuses capacity after completion", async () => {
  const pool = await credentialPool();
  const { service, gateway } = pool;
  const calls = Array.from({ length: 8 }, (_, i) => service.request({ op: "call", instance: "node", id: `pinned-${i}`, name: "local/demo.slow", arguments: {}, timeoutMs: 10000, pat: `credential-${i + 1}` }));
  try {
    for (let n = 0; gateway.calls.length < 8 && n < 300; n++) await new Promise((r) => setTimeout(r, 10));
    assert.equal(gateway.calls.length, 8);
    const sessions = [...gateway.sessions.keys()].sort();
    const full = await service.request({ op: "list", instance: "node", device: "gateway" });
    assert.equal(full.ok, false);
    if (!full.ok) assert.match(full.error.message, /Too many Gateway credentials/);
    assert.deepEqual([...gateway.sessions.keys()].sort(), sessions);
    for (let i = 0; i < 8; i++) await service.request({ op: "cancel", instance: "node", id: `pinned-${i}` });
    assert.ok((await Promise.all(calls)).every((result) => !result.ok));
    assert.equal((await service.request({ op: "list", instance: "node", device: "gateway" })).ok, true);
    assert.equal(gateway.sessions.size, 8);
  } finally { await pool.close(); await Promise.all(calls); }
});

test("rotating a credential preserves its active call until completion before reconnecting", async () => {
  const pool = await credentialPool();
  const { service, gateway } = pool;
  try {
    const slow = service.request({ op: "call", instance: "node", id: "rotation", name: "local/demo.slow", arguments: {}, timeoutMs: 10000 });
    for (let n = 0; gateway.calls.length < 1 && n < 200; n++) await new Promise((r) => setTimeout(r, 10));
    assert.equal(gateway.calls.length, 1);
    const original = [...gateway.sessions.keys()];
    pool.rotateDefault();
    const rotated = await service.request({ op: "list", instance: "node", device: "gateway" });
    assert.equal(rotated.ok, false);
    if (!rotated.ok) assert.match(rotated.error.message, /credential changed.*active/);
    assert.deepEqual([...gateway.sessions.keys()], original);
    const cancel = await service.request({ op: "cancel", instance: "node", id: "rotation" });
    assert.ok(cancel.ok && cancel.data.cancelled, "credential rotation interrupted the active call");
    assert.equal((await slow).ok, false);
    assert.equal((await service.request({ op: "list", instance: "node", device: "gateway" })).ok, true);
    assert.equal(gateway.sessions.size, 1);
    assert.notDeepEqual([...gateway.sessions.keys()], original);
  } finally { await pool.close(); }
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

test("history removes values under arbitrary keys and within arrays", () => {
  const args = {message:"password-in-message",headers:["Bearer secret"],query:{value:"credential"},count:123456};
  const result = historyArgs(args);
  assert.deepEqual(result,{message:"[redacted]",headers:["[redacted]"],query:{value:"[redacted]"},count:"[redacted]"});
  for (const secret of ["password-in-message","Bearer secret","credential","123456"]) assert.equal(JSON.stringify(result).includes(secret),false);
  assert.equal(args.message,"password-in-message");
});
