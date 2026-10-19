import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { validate, checkSteps } from "../src/protocol.js";
import { loadConfig, inside } from "../src/config.js";
import { cleanup, config } from "./helpers.js";
import { redact, Evidence, gc } from "../src/artifacts.js";
import { Locks } from "../src/locks.js";
import { Controller } from "../src/controller.js";
import type { RunResult } from "../src/types.js";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

test("protocol validates entire batch and requires action operands", () => {
  const base = { protocol_version: 1, session_id: "s", request_id: "r" };
  for (const step of [
    { id: "x", action: "fill", target: { label: "User" } },
    { id: "x", action: "assert", expect: {} },
    { id: "x", action: "click", target: { css: "a", text: "a" } },
    { id: "x", action: "shell", value: "whoami" },
  ]) {
    assert.throws(
      () => validate("RunRequest", { ...base, steps: [step] }),
      /protocol/,
    );
  }
  validate("RunRequest", {
    ...base,
    steps: [
      {
        id: "x",
        action: "assert",
        target: { testId: "r" },
        expect: { visible: true },
      },
    ],
  });
  assert.throws(
    () =>
      checkSteps([{ id: "x", action: "click", target: { alias: "loop" } }], {
        driver: "web",
        locators: { loop: { alias: "loop" } },
      }),
    /cycle/,
  );
  assert.throws(
    () =>
      checkSteps(
        [{ id: "x", action: "navigate", value: "https://outside.example" }],
        {
          driver: "web",
          url: "https://test.example",
          allowed_origins: ["https://test.example"],
        },
      ),
    /origin/,
  );
});
test("configuration resolves paths and rejects misspelled capabilities", () => {
  const c = config();
  try {
    const file = path.join(c.state_dir, "env.json");
    fs.writeFileSync(
      file,
      JSON.stringify({
        version: 1,
        state_dir: "state",
        environments: {
          web: {
            driver: "web",
            url: "http://localhost:3000",
            accounts: { a: { storage_state: "login.json" } },
          },
        },
      }),
    );
    const loaded = loadConfig(file);
    assert.equal(loaded.state_dir, path.join(c.state_dir, "state"));
    assert.deepEqual(loaded.environments.web?.allowed_origins, [
      "http://localhost:3000",
    ]);
    fs.writeFileSync(
      file,
      JSON.stringify({
        version: 1,
        state_dir: "state",
        environments: {
          web: {
            driver: "web",
            url: "http://localhost",
            scripts_enabled: true,
          },
        },
      }),
    );
    assert.throws(() => loadConfig(file), /configuration/);
  } finally {
    cleanup(c);
  }
});
test("file references reject traversal and symlink escape", () => {
  const c = config(),
    root = path.join(c.state_dir, "input");
  fs.mkdirSync(root);
  fs.writeFileSync(path.join(c.state_dir, "secret"), "private");
  try {
    assert.throws(() => inside(root, "../secret"), /outside/);
    fs.writeFileSync(path.join(root, "ok"), "ok");
    assert.equal(inside(root, "ok"), path.join(root, "ok"));
    if (process.platform !== "win32") {
      fs.symlinkSync(path.join(c.state_dir, "secret"), path.join(root, "link"));
      assert.throws(() => inside(root, "link"), /outside/);
    }
  } finally {
    cleanup(c);
  }
});
test("evidence is owned by opaque ID and redacts known secrets", () => {
  const c = config();
  try {
    const evidence = new Evidence(c, "i", "r", ['a"secret']);
    evidence.event({ value: 'a"secret' });
    assert.doesNotMatch(
      fs.readFileSync(path.join(evidence.dir, "events.jsonl"), "utf8"),
      /secret/,
    );
    assert.deepEqual(redact({ a: "known" }, ["known"]), { a: "[REDACTED]" });
    fs.writeFileSync(path.join(evidence.dir, "a.txt"), "abcdef");
    const artifact = evidence.add("a.txt", "a.txt", "text/plain");
    assert.equal(
      evidence.read(artifact.id, 2, 2).data,
      Buffer.from("cd").toString("base64"),
    );
    assert.throws(() => evidence.read("a.txt"), /unknown/);
    fs.writeFileSync(
      path.join(evidence.dir, "result.json"),
      JSON.stringify({ ended_at: "2000-01-01T00:00:00Z" }),
    );
    assert.equal(gc(c).removed, 1);
  } finally {
    cleanup(c);
  }
});

test("JUnit reports remove terminal controls and escape failure text", () => {
  const c = config();
  try {
    const evidence = new Evidence(c, "i", "r", []);
    const result = {
      protocol_version: 1,
      instance_id: "i",
      session_id: "s",
      run_id: "r",
      environment_id: "e",
      driver: "web",
      mode: "steps",
      fixtures: "mock",
      coverage: ["ui"],
      versions: {},
      status: "failed",
      started_at: new Date().toISOString(),
      steps: [],
      events: [],
      artifacts: [],
      evidence_incomplete: false,
      error: {
        code: "execution_failed",
        message: '\u001b[31m<failure>\u0000 & "quoted"\u001b[0m',
        details: {},
      },
    } as RunResult;
    const junit = evidence
      .reports(result)
      .find((artifact) => artifact.mime_type === "application/xml")!;
    const text = Buffer.from(
      evidence.read(junit.id).data as string,
      "base64",
    ).toString("utf8");
    assert.ok(!/[\u0000-\u0008\u000B\u000C\u000E-\u001F]/.test(text));
    assert.match(text, /&lt;failure&gt; &amp; &quot;quoted&quot;/);
  } finally {
    cleanup(c);
  }
});
test("resource locks exclude another holder and release idempotently", () => {
  const c = config();
  try {
    const a = new Locks(path.join(c.state_dir, "locks")),
      b = new Locks(path.join(c.state_dir, "locks"));
    const release = a.acquire("account");
    assert.ok(release);
    assert.equal(b.acquire("account"), undefined);
    release();
    release();
    assert.ok(b.acquire("account"));
    a.close();
    b.close();
  } finally {
    cleanup(c);
  }
});

test("resource locks serialize concurrent backend processes", async () => {
  const c = config();
  fs.writeFileSync(path.join(c.state_dir, "count"), "0");
  try {
    await Promise.all(
      Array.from(
        { length: 4 },
        () =>
          new Promise<void>((resolve, reject) => {
            const child = spawn(
              process.execPath,
              [
                fileURLToPath(new URL("./lock-worker.js", import.meta.url)),
                c.state_dir,
              ],
              { stdio: ["ignore", "pipe", "pipe"] },
            );
            let error = "";
            child.stderr.on("data", (data) => {
              error += data.toString();
            });
            child.on("error", reject);
            child.on("exit", (code) =>
              code === 0 ? resolve() : reject(new Error(error)),
            );
          }),
      ),
    );
    assert.equal(
      fs.readFileSync(path.join(c.state_dir, "count"), "utf8"),
      "12",
    );
  } finally {
    cleanup(c);
  }
});

test("Electron preflight reads app-bundle fuses and requires background capability", async () => {
  const c = config(),
    app = path.join(c.state_dir, "Fixture.app", "Contents"),
    executable = path.join(app, "MacOS", "Electron"),
    framework = path.join(
      app,
      "Frameworks",
      "Electron Framework.framework",
      "Electron Framework",
    );
  fs.mkdirSync(path.dirname(executable), { recursive: true });
  fs.mkdirSync(path.dirname(framework), { recursive: true });
  fs.writeFileSync(executable, "");
  const wire = (values: string) =>
    Buffer.concat([
      Buffer.from("dL7pKGdnNz796PbbjQWNKmHXBZaB9tsX"),
      Buffer.from([1, 9]),
      Buffer.from(values),
    ]);
  fs.writeFileSync(framework, wire("111011111"));
  const controller = new Controller(c);
  try {
    assert.ok(
      controller
        .preflight({ driver: "electron", executable, background_capable: true })
        .issues.includes("electron_inspect_disabled"),
    );
    fs.writeFileSync(framework, wire("111111111"));
    assert.ok(
      !controller
        .preflight({ driver: "electron", executable, background_capable: true })
        .issues.includes("electron_inspect_disabled"),
    );
    assert.ok(
      controller
        .preflight({ driver: "electron", executable })
        .issues.includes("background_support_unconfirmed"),
    );
  } finally {
    await controller.close();
    cleanup(c);
  }
});

test("backend startup marks unfinished journals from a previous process identity interrupted", async () => {
  const c = config(),
    instance = path.join(c.state_dir, "instances", "previous"),
    run = path.join(instance, "runs", "r_previous");
  fs.mkdirSync(run, { recursive: true });
  fs.writeFileSync(
    path.join(instance, "owner.json"),
    JSON.stringify({ pid: process.pid, birth: "previous-process-identity" }),
  );
  const file = path.join(run, "result.json");
  fs.writeFileSync(
    file,
    JSON.stringify({
      run_id: "r_previous",
      status: "running",
      steps: [{ id: "write", status: "started" }],
    }),
  );
  const controller = new Controller(c);
  try {
    const result = JSON.parse(fs.readFileSync(file, "utf8"));
    assert.equal(result.status, "interrupted");
    assert.equal(result.outcome_unknown, true);
    assert.ok(result.ended_at);
    assert.throws(() => controller.result("r_previous"), /unknown run/);
  } finally {
    await controller.close();
    cleanup(c);
  }
});

test("a contender paused before publication cannot join an existing lock holder", async () => {
  const c = config();
  const children: ReturnType<typeof spawn>[] = [];
  const wait = async (name: string) => {
    const end = Date.now() + 20000;
    while (!fs.existsSync(path.join(c.state_dir, name))) {
      if (Date.now() > end) throw new Error(`barrier timed out: ${name}`);
      await new Promise((r) => setTimeout(r, 10));
    }
  };
  const release = (name: string) =>
    fs.writeFileSync(path.join(c.state_dir, name), "");
  try {
    for (const actor of ["a", "b"])
      children.push(
        spawn(
          process.execPath,
          [
            fileURLToPath(
              new URL("./lock-schedule-worker.js", import.meta.url),
            ),
            c.state_dir,
            actor,
          ],
          { stdio: "ignore", windowsHide: true },
        ),
      );
    await Promise.all([wait("a.paused"), wait("b.paused")]);
    release("a.publish");
    await wait("a.granted");
    release("b.publish");
    await wait("b.rejected");
    assert.equal(fs.existsSync(path.join(c.state_dir, "b.granted")), false);
    release("a.release");
    await Promise.all(
      children.map(
        (child) =>
          new Promise<void>((resolve, reject) => {
            if (child.exitCode !== null) {
              child.exitCode === 0
                ? resolve()
                : reject(new Error("worker failed"));
              return;
            }
            child.once("exit", (code) =>
              code === 0 ? resolve() : reject(new Error("worker failed")),
            );
            child.once("error", reject);
          }),
      ),
    );
  } finally {
    for (const child of children) if (child.exitCode === null) child.kill();
    cleanup(c);
  }
});
