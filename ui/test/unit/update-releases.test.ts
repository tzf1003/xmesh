import assert from "node:assert/strict";
import { test } from "node:test";
import {
  CHECKSUM_FILE,
  type RawRelease,
  cleanNotes,
  expectedHash,
  installerName,
  parseChecksums,
  parseDigest,
  pickCandidate,
  targetFor,
} from "../../src/main/update/releases";
import { compareVersions, formatVersion, isNewer, parseVersion, sameVersion } from "../../src/shared/version";
import { DEFAULT_UPDATE_PREFS, parseUpdatePrefs } from "../../src/shared/update";

const v = (s: string) => parseVersion(s)!;

test("versions parse with or without v and reject anything else", () => {
  assert.deepEqual(v("v0.1.0-beta.2"), { major: 0, minor: 1, patch: 0, pre: ["beta", 2] });
  assert.equal(formatVersion(v("v1.2.3")), "1.2.3");
  assert.equal(formatVersion(v("0.1.0-beta.10")), "0.1.0-beta.10");
  for (const bad of ["", "1.2", "v1.2.3.4", "1.2.3-", "01.2.3", "latest", "1.2.3-beta..1"]) assert.equal(parseVersion(bad), undefined, bad);
});

test("precedence follows semver: numbers numerically, a release above its pre-releases", () => {
  const order = ["0.1.0-beta.2", "0.1.0-beta.10", "0.1.0-rc.1", "0.1.0", "0.1.1-beta.1", "0.2.0", "1.0.0"];
  for (let i = 0; i < order.length; i++) {
    for (let j = 0; j < order.length; j++) {
      assert.equal(Math.sign(compareVersions(v(order[i]!), v(order[j]!))), Math.sign(i - j), `${order[i]} vs ${order[j]}`);
    }
  }
  assert.equal(isNewer("v0.1.0-beta.3", "0.1.0-beta.2"), true);
  assert.equal(isNewer("0.1.0-beta.2", "0.1.0-beta.2"), false);
  assert.equal(isNewer("nonsense", "0.1.0"), false);
  assert.equal(sameVersion("v0.1.0-beta.2", "0.1.0-beta.2"), true);
  assert.equal(sameVersion("0.1.0", "0.1.0-beta.2"), false);
});

test("installer names follow electron-builder's artifactName", () => {
  assert.equal(installerName(v("0.1.0-beta.3"), { platform: "win", arch: "x64" }), "xmesh-ui-0.1.0-beta.3-win-x64.exe");
  assert.equal(installerName(v("0.1.0-beta.3"), { platform: "mac", arch: "arm64" }), "xmesh-ui-0.1.0-beta.3-mac-arm64.dmg");
  assert.deepEqual(targetFor("win32", "x64"), { platform: "win", arch: "x64" });
  assert.deepEqual(targetFor("darwin", "arm64"), { platform: "mac", arch: "arm64" });
  assert.deepEqual(targetFor("linux", "x64"), { platform: "linux", arch: "x64" });
  assert.equal(installerName(v("0.2.0"), { platform: "linux", arch: "arm64" }), "install-linux.sh");
  assert.equal(targetFor("freebsd", "x64"), undefined);
  assert.equal(targetFor("win32", "ia32"), undefined);
});

const win = { platform: "win", arch: "x64" } as const;

function release(tag: string, over: Partial<RawRelease> = {}): RawRelease {
  const ver = v(tag);
  return {
    tag_name: tag,
    draft: false,
    prerelease: ver.pre.length > 0,
    published_at: "2026-10-18T00:00:00Z",
    body: "notes",
    assets: [
      { name: installerName(ver, win), size: 1234, digest: `sha256:${"a".repeat(64)}` },
      { name: installerName(ver, { platform: "mac", arch: "arm64" }), size: 99 },
      { name: CHECKSUM_FILE, size: 100 },
    ],
    ...over,
  };
}

test("the newest eligible release wins, whatever order the API lists them in", () => {
  const list = [release("v0.1.0-beta.1"), release("v0.1.0-beta.3"), release("v0.1.0-beta.2")];
  const c = pickCandidate(list, "0.1.0-beta.1", true, win)!;
  assert.equal(c.tag, "v0.1.0-beta.3");
  assert.equal(c.asset, "xmesh-ui-0.1.0-beta.3-win-x64.exe");
  assert.equal(c.release.version, "0.1.0-beta.3");
  assert.equal(c.release.prerelease, true);
  assert.equal(c.release.size, 1234);
  assert.equal(c.digest, `sha256:${"a".repeat(64)}`);
});

test("nothing is offered when the app is current or ahead", () => {
  const list = [release("v0.1.0-beta.1"), release("v0.1.0-beta.2")];
  assert.equal(pickCandidate(list, "0.1.0-beta.2", true, win), undefined);
  assert.equal(pickCandidate(list, "0.2.0", true, win), undefined);
});

test("the stable channel skips pre-releases, including a pre-release tag that is not marked as one", () => {
  const list = [release("v0.2.0-beta.1"), release("v0.1.5-rc.1", { prerelease: false }), release("v0.1.1")];
  assert.equal(pickCandidate(list, "0.1.0", false, win)!.tag, "v0.1.1");
  assert.equal(pickCandidate(list, "0.1.0", true, win)!.tag, "v0.2.0-beta.1");
  assert.equal(pickCandidate([release("v0.2.0-beta.1")], "0.1.0", false, win), undefined);
});

test("drafts, odd tags and releases without this system's installer or the checksum file are skipped", () => {
  const noMac = release("v0.1.0-beta.9");
  noMac.assets = noMac.assets.filter((a) => !a.name.endsWith("win-x64.exe"));
  const noSums = release("v0.1.0-beta.8");
  noSums.assets = noSums.assets.filter((a) => a.name !== CHECKSUM_FILE);
  const list = [release("v0.1.0-beta.7", { draft: true }), { ...release("v0.1.0-beta.5"), tag_name: "nightly" }, { ...release("v0.1.0-beta.5"), tag_name: "v0.1.0-beta.6x+meta" }, noMac, noSums, release("v0.1.0-beta.4")];
  assert.equal(pickCandidate(list, "0.1.0-beta.1", true, win)!.tag, "v0.1.0-beta.4");
});

test("release notes are plain text of bounded length", () => {
  assert.equal(cleanNotes(null), "");
  assert.equal(cleanNotes("a\r\nb\u0000c\u0007"), "a\nbc");
  const long = cleanNotes("x".repeat(5000));
  assert.ok(long.length <= 2001 && long.endsWith("…"));
});

test("SHA256SUMS is read in both sha256sum notations", () => {
  const a = "a".repeat(64);
  const b = "B".repeat(64);
  const m = parseChecksums(`${a}  xmesh-ui-1-win-x64.exe\r\n${b} *other file.dmg\nnot a line\n`);
  assert.equal(m.get("xmesh-ui-1-win-x64.exe"), a);
  assert.equal(m.get("other file.dmg"), "b".repeat(64));
  assert.equal(m.size, 2);
  assert.equal(parseDigest(`sha256:${a}`), a);
  assert.equal(parseDigest("sha1:abc"), undefined);
  assert.equal(parseDigest(undefined), undefined);
});

test("the expected hash needs a source, and two sources must agree", () => {
  const a = "a".repeat(64);
  const b = "b".repeat(64);
  const sums = new Map([["f", a]]);
  assert.deepEqual(expectedHash(undefined, sums, "f"), { ok: true, sha256: a });
  assert.deepEqual(expectedHash(`sha256:${a}`, new Map(), "f"), { ok: true, sha256: a });
  assert.deepEqual(expectedHash(`sha256:${a}`, sums, "f"), { ok: true, sha256: a });
  assert.deepEqual(expectedHash(`sha256:${b}`, sums, "f"), { ok: false, reason: "conflict" });
  assert.deepEqual(expectedHash(undefined, new Map(), "f"), { ok: false, reason: "missing" });
});

test("update preferences take exactly two booleans", () => {
  assert.deepEqual(parseUpdatePrefs({ auto: false, prerelease: true }), { auto: false, prerelease: true });
  for (const bad of [null, [], {}, { auto: true }, { auto: "yes", prerelease: true }, { auto: true, prerelease: true, x: 1 }]) {
    assert.equal(parseUpdatePrefs(bad), undefined);
  }
  assert.deepEqual(DEFAULT_UPDATE_PREFS, { auto: true, prerelease: true });
});
