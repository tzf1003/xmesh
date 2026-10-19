import assert from "node:assert/strict";
import { test } from "node:test";
import { observationPats, observeDesktop } from "../../src/renderer/lib/computer-preview";
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

test("preview credentials must grant observation of the selected device and stay bound through cleanup", async () => {
  const pat = (name: string, scopes: [string, ...string[]]) => ({ name, scopes, token_file: "/private/token", modified_at: "" });
  assert.deepEqual(observationPats([
    pat("default", ["devices:read", "exec:*"]), pat("other", ["computer:observe:other"]),
    pat("clipboard", ["computer:clipboard-read:node"]), pat("watch", ["computer:observe:node"]),
    pat("control", ["computer:control:*"]),
  ], "node").map((p) => p.name), ["watch", "control"]);
  const desktop = new FakeComputer();
  const names: string[] = [];
  const transport = api(desktop);
  const selected = (async (request) => {
    if (request.op === "call") { assert.equal(request.pat, "watch"); names.push(request.name); }
    return transport(request);
  }) as PlaygroundApi;
  await observeDesktop(selected, "node-a", "local", "", new AbortController().signal, "watch");
  assert.deepEqual(names, ["local/computer.info", "local/computer.open", "local/computer.observe", "local/computer.close"]);
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

test("window preview binds the observer and rejects a missing target instead of capturing the desktop",async()=>{
  const desktop=new FakeComputer();
  desktop.targets=[{id:"window-one",name:"Target app",app_id:"test.app"}];
  const result=await observeDesktop(api(desktop),"node-a","local","",new AbortController().signal,"watch","window-one");
  assert.equal(result.target,"window-one");
  assert.equal(result.targets[0]?.app_id,"test.app");
  assert.equal(desktop.sessions.size,0);
  const captures=desktop.captures;
  desktop.targets=[];
  await assert.rejects(observeDesktop(api(desktop),"node-a","local","",new AbortController().signal,"watch","window-one"),/window is unavailable/);
  assert.equal(desktop.captures,captures);
});
