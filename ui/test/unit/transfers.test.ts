import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import type { ShellController } from "../../src/main/controller";
import { PlaygroundService } from "../../src/main/playground";
import { TransferService } from "../../src/main/transfers";
import { parseTransfer, type TransferState } from "../../src/shared/transfers";
import { EMPTY_SNAPSHOT } from "../../src/shared/state";
import { FakeKernel } from "../../testserver/server";

test("transfer IPC uses opaque tickets and refuses local paths and invalid values", () => {
  assert.ok(
    parseTransfer({
      op: "start",
      instance: "n",
      direction: "upload",
      device: "local",
      path: "/work/a",
      ticket: "file-1",
    }),
  );
  for (const v of [
    { op: "pick", instance: "n", path: "/etc/passwd" },
    { op: "start", instance: "n", direction: "upload", device: "local", path: "/a", ticket: "../../x" },
    { op: "start", instance: "n", direction: "download", device: "../peer", path: "/a" },
    { op: "status", instance: "../n" },
  ])
    assert.equal(parseTransfer(v), undefined);
});

test("chunked binary roundtrip, empty files, conflict, corruption, cancellation and legacy limits", async () => {
  const kernel = await FakeKernel.start({ playground: true });
  const workspace = kernel.playground!.enableWorkspace();
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-transfer-test-"));
  let input = path.join(root, "source.bin"),
    output = path.join(root, "output.bin"),
    confirmations = 0;
  const data = Buffer.alloc(2 * 1024 * 1024 + 137);
  for (let i = 0; i < data.length; i++) data[i] = i % 256;
  fs.writeFileSync(input, data);
  const snapshot = {
    ...EMPTY_SNAPSHOT,
    selectedInstance: "n",
    connection: { kind: "connected" as const, status: kernel.currentStatus, peers: [] },
  };
  const controller = {
    snapshot,
    requireCapable: () => ({ call: async () => ({ pats: kernel.panel.pats }) }),
  } as unknown as Pick<ShellController, "snapshot" | "requireCapable">;
  const playground = new PlaygroundService(controller);
  const service = new TransferService(playground, controller, {
    upload: async () => input,
    download: async () => output,
    overwrite: async () => {
      confirmations++;
      return true;
    },
  });
  async function pick(): Promise<string> {
    const r = await service.request({ op: "pick", instance: "n" });
    assert.ok(r.ok);
    return r.data.ticket!;
  }
  async function done(id: string): Promise<TransferState> {
    const until = Date.now() + 10_000;
    while (Date.now() < until) {
      const r = await service.request({ op: "status", instance: "n" });
      assert.ok(r.ok);
      const s = r.data.states!.find((x) => x.id === id)!;
      if (["complete", "failed", "cancelled"].includes(s.phase)) return s;
      await new Promise((r) => setTimeout(r, 10));
    }
    throw new Error("transfer did not settle");
  }
  async function start(
    direction: "upload" | "download",
    remote: string,
    ticket?: string,
    force = false,
  ): Promise<TransferState> {
    const r = await service.request({
      op: "start",
      instance: "n",
      direction,
      device: "local",
      path: remote,
      ...(ticket && { ticket }),
      force,
    });
    assert.ok(r.ok);
    assert.ok(r.data.id);
    return done(r.data.id);
  }
  try {
    const ticket = await pick();
    const uploaded = await start("upload", "/work/sample.bin", ticket);
    assert.equal(uploaded.phase, "complete", uploaded.error ?? "");
    assert.deepEqual(workspace.files.get("/work/sample.bin"), data);
    const downloaded = await start("download", "/work/sample.bin");
    assert.equal(downloaded.phase, "complete", downloaded.error ?? "");
    assert.deepEqual(fs.readFileSync(output), data);
    assert.equal(downloaded.sha256, uploaded.sha256);
    const conflict = await start("upload", "/work/sample.bin", ticket);
    assert.equal(conflict.code, "conflict");
    const retry = await service.request({ op: "retry", instance: "n", id: conflict.id });
    assert.ok(retry.ok); assert.equal((await done(retry.data.id!)).phase, "complete");
    assert.equal(confirmations, 1);
    input = path.join(root, "empty.bin");
    fs.writeFileSync(input, "");
    const empty = await pick();
    assert.equal((await start("upload", "/work/empty.bin", empty)).phase, "complete");
    output = path.join(root, "empty-out.bin");
    assert.equal((await start("download", "/work/empty.bin")).phase, "complete");
    assert.equal(fs.statSync(output).size, 0);
    output = path.join(root, "preserved.bin");
    fs.writeFileSync(output, "original");
    workspace.corruptDownload = true;
    const corrupt = await start("download", "/work/sample.bin");
    assert.equal(corrupt.phase, "failed");
    assert.match(corrupt.error!, /checksum/);
    assert.equal(fs.readFileSync(output, "utf8"), "original");
    assert.equal(
      fs.readdirSync(root).some((f) => f.startsWith(".xmesh-download")),
      false,
    );
    workspace.corruptDownload = false;
    workspace.delayMs = 500;
    const pending = await service.request({
      op: "start",
      instance: "n",
      direction: "download",
      device: "local",
      path: "/work/sample.bin",
    });
    assert.ok(pending.ok);
    await new Promise((r) => setTimeout(r, 50));
    await service.request({ op: "cancel", instance: "n", id: pending.data.id! });
    assert.equal((await done(pending.data.id!)).phase, "cancelled");
    assert.equal(fs.readFileSync(output, "utf8"), "original");
    workspace.delayMs = 0;
    workspace.chunked = false;
    kernel.playground!.tools = workspace.tools();
    input = path.join(root, "large.bin");
    fs.writeFileSync(input, Buffer.alloc(11 * 1024 * 1024));
    const large = await start("upload", "/work/large.bin", await pick());
    assert.equal(large.phase, "failed");
    assert.match(large.error!, /10 MiB/);
    const legacy = await start("download", "/work/sample.bin");
    assert.equal(legacy.phase, "complete", legacy.error ?? "");
    assert.deepEqual(fs.readFileSync(output), data);
  } finally {
    service.reset();
    await playground.close();
    await kernel.stop();
    fs.rmSync(root, { recursive: true, force: true });
  }
});
