import assert from "node:assert/strict";
import { test } from "node:test";
import { type BinaryLookup, resolveXmeshBinary, wellKnownDirs } from "../../src/main/system/binary";

const lookup = (over: Partial<BinaryLookup>, present: string[] = []): BinaryLookup => ({
  platform: "linux",
  env: {},
  home: "/home/u",
  exists: (p) => present.includes(p),
  ...over,
});

test("XMESH_BIN is taken at its word, even if it does not exist", () => {
  assert.equal(resolveXmeshBinary(lookup({ env: { XMESH_BIN: "/opt/x/xmesh" } })), "/opt/x/xmesh");
});

test("the binary shipped inside the app beats PATH", () => {
  const l = lookup({ platform: "darwin", env: { PATH: "/usr/bin" }, resourcesPath: "/Applications/xmesh.app/Contents/Resources", home: "/Users/u" }, [
    "/Applications/xmesh.app/Contents/Resources/bin/xmesh",
    "/usr/bin/xmesh",
  ]);
  assert.equal(resolveXmeshBinary(l), "/Applications/xmesh.app/Contents/Resources/bin/xmesh");
});

test("macOS finds Homebrew and ~/.local/bin although a login item's PATH is minimal", () => {
  const base = { platform: "darwin" as const, env: { PATH: "/usr/bin:/bin:/usr/sbin:/sbin" }, home: "/Users/u" };
  assert.equal(resolveXmeshBinary(lookup(base, ["/opt/homebrew/bin/xmesh"])), "/opt/homebrew/bin/xmesh");
  assert.equal(resolveXmeshBinary(lookup(base, ["/Users/u/.local/bin/xmesh"])), "/Users/u/.local/bin/xmesh");
  assert.ok(wellKnownDirs(base).includes("/usr/local/bin"));
});

test("PATH is searched before the usual install directories, and relative PATH entries are ignored", () => {
  const l = lookup({ platform: "darwin", env: { PATH: "relative/bin:/custom/bin" }, home: "/Users/u" }, ["/custom/bin/xmesh", "/usr/local/bin/xmesh"]);
  assert.equal(resolveXmeshBinary(l), "/custom/bin/xmesh");
  assert.equal(resolveXmeshBinary(lookup({ platform: "darwin", env: { PATH: "relative/bin" }, home: "/h" }, ["relative/bin/xmesh"])), "xmesh");
});

test("Windows: xmesh.exe, ';' separated Path, per-user and Program Files locations", () => {
  const env = { Path: "C:\\Windows\\System32;D:\\tools", LOCALAPPDATA: "C:\\Users\\u\\AppData\\Local", ProgramFiles: "C:\\Program Files" };
  const w = { platform: "win32" as const, env, home: "C:\\Users\\u" };
  assert.equal(resolveXmeshBinary(lookup(w, ["D:\\tools\\xmesh.exe"])), "D:\\tools\\xmesh.exe");
  assert.equal(
    resolveXmeshBinary(lookup(w, ["C:\\Users\\u\\AppData\\Local\\Programs\\xmesh\\xmesh.exe"])),
    "C:\\Users\\u\\AppData\\Local\\Programs\\xmesh\\xmesh.exe",
  );
  assert.equal(resolveXmeshBinary(lookup(w, ["C:\\Program Files\\xmesh\\xmesh.exe"])), "C:\\Program Files\\xmesh\\xmesh.exe");
  assert.equal(
    resolveXmeshBinary(lookup({ ...w, resourcesPath: "C:\\Program Files\\xmesh-ui\\resources" }, ["C:\\Program Files\\xmesh-ui\\resources\\bin\\xmesh.exe"])),
    "C:\\Program Files\\xmesh-ui\\resources\\bin\\xmesh.exe",
  );
});

test("falls back to the bare name so the OS reports not-found", () => {
  assert.equal(resolveXmeshBinary(lookup({})), "xmesh");
  assert.equal(resolveXmeshBinary(lookup({ platform: "win32", home: "C:\\Users\\u" })), "xmesh");
});
