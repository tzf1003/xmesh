import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { mock, test } from "node:test";
import { spawnSync } from "node:child_process";
import {
  type Instance,
  discoverConfigured,
  discoverInstances,
  kernelGone,
  removeStaleControl,
  stoppedNodes,
  userConfigDir,
} from "../../src/main/discovery";

test("userConfigDir matches Go's os.UserConfigDir per platform", () => {
  assert.equal(userConfigDir("win32", { APPDATA: "C:\\Users\\u\\AppData\\Roaming" }, "C:\\Users\\u"), "C:\\Users\\u\\AppData\\Roaming");
  assert.equal(userConfigDir("darwin", {}, "/Users/u"), path.join("/Users/u", "Library", "Application Support"));
  assert.equal(userConfigDir("linux", { XDG_CONFIG_HOME: "/x" }, "/home/u"), "/x");
  assert.equal(userConfigDir("linux", {}, "/home/u"), path.join("/home/u", ".config"));
});

test("discoverInstances reads every <root>/*/control.json and skips broken ones", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-disc-"));
  try {
    const put = (name: string, body: string): void => {
      fs.mkdirSync(path.join(root, name), { recursive: true });
      fs.writeFileSync(path.join(root, name, "control.json"), body);
    };
    const manifest = (endpoint: string) => ({ api_version: "1.0", kernel_version: "1.0.0", node: "n", pid: 5, boot_id: "ab", transport: "unix", endpoint, started_at: "2026-10-16T09:00:00.000Z", capabilities: ["status"] });
    put("b", JSON.stringify(manifest("/tmp/b.sock")));
    put("a", JSON.stringify(manifest("/tmp/a.sock")));
    put("half-written", '{"endpoint": "/tm');
    put("no-endpoint", "{}");
    fs.mkdirSync(path.join(root, "empty"));
    fs.writeFileSync(path.join(root, "stray-file"), "x");
    assert.deepEqual(
      discoverInstances(root).map((i) => i.id),
      ["a", "b"],
    );
    assert.deepEqual(discoverInstances(path.join(root, "missing")), []);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test("discoverConfigured lists node directories with a config file; stoppedNodes leaves out the running ones", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-cfg-"));
  try {
    const put = (name: string, file: string): void => {
      fs.mkdirSync(path.join(root, name), { recursive: true });
      fs.writeFileSync(path.join(root, name, file), "node:\n  name: x\n");
    };
    put("office-pc", "xmesh.yaml");
    put("beta2-mac", "xmesh.yaml");
    put("no-config", "control.json");
    put("not a node name", "xmesh.yaml");
    fs.writeFileSync(path.join(root, "stray.yaml"), "x");
    assert.deepEqual(discoverConfigured(root), ["beta2-mac", "office-pc"]);
    assert.deepEqual(discoverConfigured(path.join(root, "missing")), []);
    assert.deepEqual(stoppedNodes(discoverConfigured(root), [{ id: "office-pc" } as Instance]), ["beta2-mac"]);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test("kernelGone only answers true for a pid that provably has no process", () => {
  assert.equal(kernelGone(process.pid), false, "this test process is alive");
  const child = spawnSync(process.execPath, ["-e", ""]);
  assert.equal(child.status, 0);
  assert.equal(kernelGone(child.pid), true);
  assert.equal(kernelGone(0), false, "no pid recorded is not proof of a dead kernel");
  assert.equal(kernelGone(-1), false);
});

test("kernelGone keeps access errors and invalid PIDs unknown", () => {
  const probe = mock.method(process, "kill", () => { throw Object.assign(new Error("denied"), { code: "EPERM" }); });
  try {
    assert.equal(kernelGone(12), false);
    assert.equal(kernelGone(Number.NaN), false);
  } finally {
    probe.mock.restore();
  }
});

test("removeStaleControl deletes a leftover file, refuses a live kernel and a name outside the roots", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-stale-"));
  try {
    const put = (id: string, pid: number): string => {
      const dir = path.join(root, id);
      fs.mkdirSync(dir, { recursive: true });
      fs.writeFileSync(
        path.join(dir, "control.json"),
        JSON.stringify({ api_version: "1.0", kernel_version: "1", node: id, pid, boot_id: "ab", transport: "unix", endpoint: "/tmp/x.sock", started_at: "2026-10-17T09:27:19.610Z", capabilities: [] }),
      );
      return dir;
    };
    const stale = put("stale", 21);
    const live = put("live", 22);
    fs.writeFileSync(path.join(stale, "xmesh.yaml"), "user config");
    const gone = (pid: number) => pid === 21;
    assert.equal(removeStaleControl(root, "live", gone), false, "a running kernel keeps its file");
    assert.ok(fs.existsSync(path.join(live, "control.json")));
    assert.equal(removeStaleControl(root, "../stale", gone), false, "no path may leave the roots");
    assert.equal(removeStaleControl(root, "missing", gone), false);
    assert.equal(removeStaleControl(root, "stale", gone), true);
    assert.ok(!fs.existsSync(path.join(stale, "control.json")));
    assert.equal(fs.readFileSync(path.join(stale, "xmesh.yaml"), "utf8"), "user config");
    const reused = put("reused", process.pid);
    const identities: Array<[number, string | undefined]> = [];
    assert.equal(removeStaleControl(root, "reused", (pid, startedAt) => {
      identities.push([pid, startedAt]);
      return kernelGone(pid, startedAt, () => Date.parse("2026-10-18T09:27:19.610Z"));
    }), true);
    assert.deepEqual(identities, Array(2).fill([process.pid, "2026-10-17T09:27:19.610Z"]));
    assert.equal(fs.existsSync(path.join(reused, "control.json")), false);
    put("restarting", 21);
    assert.equal(removeStaleControl(root, "restarting", () => { put("restarting", 22); return true; }), false);
    assert.equal(JSON.parse(fs.readFileSync(path.join(root, "restarting", "control.json"), "utf8")).pid, 22);
    put("publishing", 21);
    let probes = 0;
    assert.equal(removeStaleControl(root, "publishing", () => {
      if (++probes === 2) put("publishing", 22);
      return true;
    }), true);
    assert.equal(JSON.parse(fs.readFileSync(path.join(root, "publishing", "control.json"), "utf8")).pid, 22, "publication after capture survives cleanup");
    put("restoring", 21);
    probes = 0;
    assert.equal(removeStaleControl(root, "restoring", () => {
      if (++probes === 2) { put("restoring", 22); return false; }
      return true;
    }), false);
    assert.equal(JSON.parse(fs.readFileSync(path.join(root, "restoring", "control.json"), "utf8")).pid, 22, "restoration preserves a newer publication");
    put("system", 21);
    assert.equal(removeStaleControl(root, "system", gone, [root]), false);
    assert.ok(discoverInstances(root, [root]).find((i) => i.id === "system")?.protected);
    assert.ok(fs.existsSync(path.join(root, "system", "control.json")));
    const secondary = path.join(root, "secondary");
    fs.mkdirSync(path.join(secondary, "live"), { recursive: true });
    const secondaryFile = path.join(secondary, "live", "control.json");
    fs.writeFileSync(secondaryFile, fs.readFileSync(path.join(root, "restarting", "control.json"), "utf8").replace('"pid":22', '"pid":21'));
    assert.equal(removeStaleControl([root, secondary], "live", gone), false);
    assert.ok(fs.existsSync(secondaryFile), "the first root determines the node being cleaned");
    assert.equal(removeStaleControl(root, "stale", gone), false, "nothing left to remove");
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
