import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { validate, checkSteps } from "../src/protocol.js";
import { loadConfig, inside } from "../src/config.js";
import { config } from "./helpers.js";
import { redact, Evidence, gc } from "../src/artifacts.js";
import { Locks } from "../src/locks.js";
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
    fs.rmSync(c.state_dir, { recursive: true, force: true });
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
    fs.rmSync(c.state_dir, { recursive: true, force: true });
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
    fs.rmSync(c.state_dir, { recursive: true, force: true });
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
    fs.rmSync(c.state_dir, { recursive: true, force: true });
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
    fs.rmSync(c.state_dir, { recursive: true, force: true });
  }
});
