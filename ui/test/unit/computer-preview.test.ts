import assert from "node:assert/strict";
import { test } from "node:test";
import { observeDesktop } from "../../src/renderer/lib/computer-preview";
import type { PlaygroundApi } from "../../src/shared/playground";
import { FakeComputer } from "../../testserver/computer";

function api(desktop: FakeComputer, before?: (tool: string) => void): PlaygroundApi {
  return (async (request) => {
    assert.equal(request.instance, "node-a");
    if (request.op !== "call") return { ok: true, data: { cancelled: true } };
    assert.ok(request.name.startsWith("local/computer."));
    const tool = request.name.slice("local/computer.".length);
    before?.(tool);
    return { ok: true, data: { result: desktop.call(tool, request.arguments), durationMs: 1 } };
  }) as PlaygroundApi;
}

test("preview uses a bounded observation lease and closes it after receiving the image", async () => {
  const desktop = new FakeComputer();
  const result = await observeDesktop(api(desktop), "node-a", "local", "", new AbortController().signal);
  assert.equal(result.width, 1);
  assert.equal(result.display, "fixture-display");
  assert.ok(result.source.startsWith("data:image/png;base64,"));
  assert.deepEqual(desktop.modes, ["observe"]);
  assert.equal(desktop.sessions.size, 0);
});

test("preview closes its observer after cancellation and rejects policy or image errors", async () => {
  const desktop = new FakeComputer();
  const abort = new AbortController();
  await assert.rejects(observeDesktop(api(desktop, (tool) => { if (tool === "observe") abort.abort(); }), "node-a", "local", "", abort.signal), /abort/i);
  assert.equal(desktop.sessions.size, 0);
  desktop.image = "https://untrusted.example/screen.png";
  await assert.rejects(observeDesktop(api(desktop), "node-a", "local", "", new AbortController().signal), /Invalid desktop image/);
  assert.equal(desktop.sessions.size, 0);
  desktop.deny = true;
  await assert.rejects(observeDesktop(api(desktop), "node-a", "local", "", new AbortController().signal), /Observation denied/);
});
