import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { Controller } from "../src/controller.js";
import { Driver } from "../src/driver.js";
import { Locks } from "../src/locks.js";
import { ownProcess, terminateProcesses } from "../src/processes.js";
import { cleanup, config } from "./helpers.js";

test("failed exit cleanup retains locks and a later close retries termination", async (t) => {
  const c = config();
  const controller = new Controller(c);
  const child = spawn(process.execPath, ["-e", "setInterval(()=>{},1000)"], {
    stdio: "ignore",
    windowsHide: true,
  });
  await once(child, "spawn");
  const owned = ownProcess(child.pid!, false);
  const locks = new Locks(path.join(c.state_dir, "fixture-locks"));
  const competing = new Locks(path.join(c.state_dir, "fixture-locks"));
  const release = locks.acquire("account")!;
  const session: any = {
    id: "s_cleanup",
    child,
    closed: false,
    pending: new Map(),
    processes: [owned],
    releases: [release],
    dir: path.join(c.state_dir, "session"),
  };
  const state = controller as any;
  state.sessions.set(session.id, session);
  const originalKill = process.kill.bind(process);
  try {
    if (process.platform === "win32") {
      // Identity corruption stands in for an unconfirmed kill without invoking
      // taskkill on an unrelated process; native Job behavior is covered in Go.
      t.skip("POSIX signal fault injection");
      state.sessions.delete(session.id);
    } else {
      let deny = true;
      t.mock.method(
        process,
        "kill",
        (pid: number, signal?: NodeJS.Signals | number) => {
          if (pid === child.pid && signal === "SIGKILL" && deny) {
            const error = new Error(
              "transient signal denial",
            ) as NodeJS.ErrnoException;
            error.code = "EPERM";
            throw error;
          }
          return originalKill(pid, signal);
        },
      );
      try {
        state.exited(session);
        await assert.rejects(session.exitCleanup, /could not be terminated/);
        assert.equal(competing.acquire("account"), undefined);
        // Retry twice to exercise both cached promises and automatic recovery.
        await assert.rejects(
          controller.closeSession(session.id),
          /could not be terminated/,
        );
        deny = false;
        await controller.closeSession(session.id);
        assert.equal(state.sessions.has(session.id), false);
        assert.ok(competing.acquire("account"));
        assert.throws(() => originalKill(child.pid!, 0), /ESRCH/);
      } finally {
        deny = false;
        await terminateProcesses([owned]);
        state.sessions.delete(session.id);
      }
    }
  } finally {
    child.kill("SIGKILL");
    state.sessions.delete(session.id);
    await controller.close();
    locks.close();
    competing.close();
    cleanup(c);
  }
});

test("explicit screenshot deadlines reach both Web capture paths and hidden Electron capture", async () => {
  const c = config();
  c.limits.step_timeout_ms = 20;
  const driver = new Driver(
    { config: c, environment: { driver: "web" }, session_dir: c.state_dir },
    () => {},
  );
  const timeouts: number[] = [];
  const capture = async (options: any) => {
    timeouts.push(options.timeout);
    fs.writeFileSync(options.path, "fixture");
  };
  const locator = { screenshot: capture, all: async () => [] };
  driver.page = { locator: () => locator, screenshot: capture } as any;
  await driver.step(
    { id: "web", action: "screenshot", timeout_ms: 120 },
    c.state_dir,
  );
  await driver.step(
    {
      id: "target",
      action: "screenshot",
      target: { css: "body" },
      timeout_ms: 150,
    },
    c.state_dir,
  );
  await driver.screenshot(c.state_dir, "failure.png");
  assert.deepEqual(timeouts, [120, 150, 20]);
  driver.electronApp = {
    browserWindow: async () => ({
      evaluate: async () => 1,
      dispose: async () => {},
    }),
    evaluate: async () => {
      await new Promise((r) => setTimeout(r, 60));
      return Buffer.from("fixture").toString("base64");
    },
  } as any;
  driver.assertBackground = async () => {};
  try {
    await driver.step(
      { id: "electron", action: "screenshot", timeout_ms: 200 },
      c.state_dir,
    );
    await assert.rejects(
      driver.screenshot(c.state_dir, "failure.png"),
      /timed out/,
    );
  } finally {
    cleanup(c);
  }
});
