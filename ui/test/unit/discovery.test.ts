import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { discoverInstances, userConfigDir } from "../../src/main/discovery";

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
