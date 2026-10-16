import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { discoverInstances, stateRoots } from "../../src/main/discovery";
import { SeenFlags } from "../../src/main/system/firstrun";

test("stateRoots: the user's config dir, plus %ProgramData%\\xmesh on Windows only; the test override replaces all", () => {
  assert.deepEqual(stateRoots({ APPDATA: "C:\\Users\\u\\AppData\\Roaming", ProgramData: "D:\\PD" }, "win32", "C:\\Users\\u"), [
    path.join("C:\\Users\\u\\AppData\\Roaming", "xmesh"),
    "D:\\PD\\xmesh",
  ]);
  assert.equal(stateRoots({}, "win32", "C:\\Users\\u")[1], "C:\\ProgramData\\xmesh");
  assert.deepEqual(stateRoots({}, "darwin", "/Users/u"), [path.join("/Users/u", "Library", "Application Support", "xmesh")]);
  assert.deepEqual(stateRoots({ XMESH_UI_STATE_ROOT: "/t" }, "win32", "C:\\h"), ["/t"]);
});

test("discoverInstances merges roots and lists a node name once, from the first root", () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-roots-"));
  try {
    const put = (root: string, name: string, endpoint: string): void => {
      fs.mkdirSync(path.join(tmp, root, name), { recursive: true });
      fs.writeFileSync(
        path.join(tmp, root, name, "control.json"),
        JSON.stringify({ api_version: "1.0", kernel_version: "1.0.0", node: name, pid: 5, boot_id: "ab", transport: "unix", endpoint, started_at: "2026-10-16T09:00:00.000Z", capabilities: ["status"] }),
      );
    };
    put("user", "a", "/user/a.sock");
    put("system", "a", "/system/a.sock");
    put("system", "svc", "/system/svc.sock");
    const found = discoverInstances([path.join(tmp, "user"), path.join(tmp, "system"), path.join(tmp, "missing")]);
    assert.deepEqual(found.map((i) => [i.id, i.manifest.endpoint]), [["a", "/user/a.sock"], ["svc", "/system/svc.sock"]]);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});

test("SeenFlags reports a flag once and keeps others", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-seen-"));
  try {
    const f = new SeenFlags(path.join(dir, "sub", "seen.json"));
    assert.equal(f.has("hint"), false);
    assert.equal(f.markOnce("hint"), true);
    assert.equal(f.markOnce("hint"), false);
    assert.equal(f.markOnce("other"), true);
    assert.equal(new SeenFlags(path.join(dir, "sub", "seen.json")).has("hint"), true);
    fs.writeFileSync(path.join(dir, "bad.json"), "{not json");
    assert.equal(new SeenFlags(path.join(dir, "bad.json")).markOnce("x"), true);
    // unwritable location: counts as seen so a read-only profile never nags
    fs.writeFileSync(path.join(dir, "file"), "x");
    assert.equal(new SeenFlags(path.join(dir, "file", "seen.json")).markOnce("x"), false);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
