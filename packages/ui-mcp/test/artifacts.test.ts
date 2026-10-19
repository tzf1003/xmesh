import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { bytes } from "../src/artifacts.js";

function directory(t: test.TestContext): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-evidence-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}

test("evidence accounting continues after a guardian file is atomically renamed", (t) => {
  const dir = directory(t);
  const temporary = path.join(dir, "guardian.json.pending.tmp");
  fs.writeFileSync(temporary, "state");
  fs.writeFileSync(path.join(dir, "retained.dat"), "retained");
  const stat = fs.statSync.bind(fs);
  t.mock.method(fs, "statSync", (file: any, options: any) => {
    if (file === temporary)
      fs.renameSync(temporary, path.join(dir, "guardian.json"));
    return stat(file, options);
  });
  assert.equal(bytes(dir), Buffer.byteLength("retained"));
});

test("evidence accounting continues after a nested directory is removed", (t) => {
  const dir = directory(t);
  const removed = path.join(dir, "expired");
  fs.mkdirSync(removed);
  fs.writeFileSync(path.join(removed, "result.json"), "expired");
  fs.writeFileSync(path.join(dir, "retained.dat"), "retained");
  const read = fs.readdirSync.bind(fs);
  t.mock.method(fs, "readdirSync", (file: any, options: any) => {
    if (file === removed) fs.rmSync(removed, { recursive: true });
    return read(file, options);
  });
  assert.equal(bytes(dir), Buffer.byteLength("retained"));
  assert.equal(bytes(path.join(dir, "absent")), 0);
});

test("evidence accounting propagates failures other than disappearing paths", async (t) => {
  for (const operation of ["readdirSync", "statSync"] as const) {
    await t.test(operation, (t) => {
      const dir = directory(t);
      fs.writeFileSync(path.join(dir, "evidence.dat"), "evidence");
      const denied = Object.assign(new Error("permission denied"), {
        code: "EACCES",
      });
      t.mock.method(fs, operation, () => {
        throw denied;
      });
      assert.throws(() => bytes(dir), (error) => error === denied);
    });
  }
});
