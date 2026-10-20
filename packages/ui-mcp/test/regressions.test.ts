import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { Controller } from "../src/controller.js";
import { navigationEndpoint } from "../src/navigation.js";
import { Driver } from "../src/driver.js";
import { Locks } from "../src/locks.js";
import { ownProcess, terminateProcesses } from "../src/processes.js";
import { cleanup, config } from "./helpers.js";

test("idle cleanup starts only after initialization completes", async (t) => {
  const c = config();
  c.limits.session_idle_ms = 60000;
  const controller = new Controller(c);
  const state = controller as any;
  let now = 70000;
  t.mock.method(Date, "now", () => now);
  const closed: string[] = [];
  t.mock.method(controller, "closeSession", async (id: string) => { closed.push(id); });
  const session: any = { id: "s_initializing", initializing: true, closed: false, lastUse: 0 };
  state.sessions.set(session.id, session);
  try {
    await new Promise((r) => setTimeout(r, 1100));
    assert.equal(closed.length, 0);
    session.initializing = false;
    session.lastUse = now;
    await new Promise((r) => setTimeout(r, 1100));
    assert.equal(closed.length, 0);
    now += 60001;
    await new Promise((r) => setTimeout(r, 1100));
    assert.ok(closed.includes(session.id));
  } finally {
    state.sessions.delete(session.id);
    await controller.close();
    cleanup(c);
  }
});

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

test("Chromium endpoint readiness waits for missing and partially written startup data", async () => {
  const c = config();
  const file = path.join(c.state_dir, "DevToolsActivePort");
  const partial = setTimeout(() => fs.writeFileSync(file, "12345\r\n"), 30);
  const complete = setTimeout(
    () => fs.writeFileSync(file, "12345\r\n/devtools/browser/fixture-123\r\n"),
    60,
  );
  try {
    assert.deepEqual(await navigationEndpoint(c.state_dir, () => true, 2000), {
      port: "12345",
      endpoint: "/devtools/browser/fixture-123",
    });
    fs.writeFileSync(file, "invalid");
    await assert.rejects(
      navigationEndpoint(c.state_dir, () => true, 50),
      /did not become ready/,
    );
    await assert.rejects(
      navigationEndpoint(c.state_dir, () => false, 2000),
      /did not become ready/,
    );
  } finally {
    clearTimeout(partial);
    clearTimeout(complete);
    cleanup(c);
  }
});

test("Chromium endpoint readiness retries transient file sharing failures within its deadline", async (t) => {
  const c = config();
  const file = path.join(c.state_dir, "DevToolsActivePort");
  fs.writeFileSync(file, "12345\n/devtools/browser/fixture-123\n");
  const read = fs.readFileSync.bind(fs);
  const failures = ["EACCES", "EPERM", "EBUSY"];
  let persistent = false;
  t.mock.method(fs, "readFileSync", (input: any, options: any) => {
    if (input === file) {
      const code = persistent ? "EACCES" : failures.shift();
      if (code)
        throw Object.assign(new Error("endpoint publication in progress"), {
          code,
        });
    }
    return read(input, options);
  });
  try {
    assert.deepEqual(await navigationEndpoint(c.state_dir, () => true, 2000), {
      port: "12345",
      endpoint: "/devtools/browser/fixture-123",
    });
    assert.equal(failures.length, 0);
    persistent = true;
    await assert.rejects(
      navigationEndpoint(c.state_dir, () => true, 50),
      (error: any) =>
        error.code === "preflight_failed" &&
        error.details.filesystem_error === "EACCES",
    );
  } finally {
    cleanup(c);
  }
});
