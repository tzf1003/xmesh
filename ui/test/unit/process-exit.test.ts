import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { stopNativeTestProcess, waitForNativeExit } from "../e2e/process-exit";

test("native exit is observed while a descendant still holds the output pipe", { skip: process.platform === "win32" ? "Windows parent-exit policy can terminate the descendant; POSIX pipe inheritance case" : false }, async () => {
  const code = `const {spawn}=require('node:child_process'); const child=spawn(process.execPath,['-e','setTimeout(()=>{},10000)'],{stdio:['ignore','inherit','inherit']}); process.stdout.write(String(child.pid),()=>process.exit(0));`;
  const parent = spawn(process.execPath, ["-e", code], { stdio: ["ignore", "pipe", "pipe"] });
  let helper: number | undefined;
  let closed = false;
  parent.once("close", () => { closed = true; });
  try {
    const [data] = await once(parent.stdout!, "data");
    helper = Number(String(data).trim());
    await waitForNativeExit(parent, 5_000);
    assert.equal(parent.exitCode, 0);
    assert.ok(Number.isInteger(helper) && helper > 0);
    assert.doesNotThrow(() => process.kill(helper!, 0), "the descendant remains alive after native parent exit");
    assert.equal(closed, false, "the inherited pipe must not determine native shutdown");
    await waitForNativeExit(parent, 1);
  } finally {
    if (helper && Number.isInteger(helper)) {
      try { process.kill(helper); } catch (error) { if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error; }
    }
    if (parent.exitCode === null && parent.signalCode === null) parent.kill();
    parent.stdout?.destroy();
    parent.stderr?.destroy();
  }
});

test("an owned process that remains alive fails the bounded exit check", async () => {
  const child = spawn(process.execPath, ["-e", "setTimeout(()=>{},10000)"], { stdio: "ignore" });
  try {
    await assert.rejects(waitForNativeExit(child, 20), /did not exit/);
    assert.equal(child.exitCode, null);
  } finally {
    const exited = once(child, "exit");
    child.kill();
    await exited;
  }
});

test("fixture cleanup reclaims its native child when an automation exit request stays pending", async () => {
  const child = spawn(process.execPath, ["-e", "process.on('SIGTERM',()=>{}); console.log('ready'); setTimeout(()=>{},10000)"], { stdio: ["ignore", "pipe", "ignore"] });
  try {
    await once(child.stdout!, "data");
    await stopNativeTestProcess(child, () => new Promise(() => {}), 30, 100);
    assert.ok(child.exitCode !== null || child.signalCode !== null);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    child.stdout?.destroy();
  }
});
