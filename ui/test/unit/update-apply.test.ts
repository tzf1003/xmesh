import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { ApplyError, MAC_SCRIPT, type Spawned, WINDOWS_SCRIPT, macApplier, macBundle, windowsApplier } from "../../src/main/update/apply";

const handOver = (dir: string) => ({ installer: path.join(dir, "i.dmg"), execPath: "/Applications/xmesh.app/Contents/MacOS/xmesh", pid: 4242, dir, resultFile: path.join(dir, "result.txt") });
/** A child process that reports it started on the next tick. */
const child = (): Spawned => ({
  once(ev, fn) {
    if (ev === "spawn") setImmediate(() => fn(new Error("unused")));
    return this;
  },
  unref() {},
});
const tmpDir = () => fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-apply-"));

test("macBundle finds the .app a MacOS executable lives in", () => {
  assert.equal(macBundle("/Applications/xmesh.app/Contents/MacOS/xmesh"), "/Applications/xmesh.app");
  assert.equal(macBundle("/Users/a b/Apps/xmesh.app/Contents/MacOS/xmesh"), "/Users/a b/Apps/xmesh.app");
  assert.equal(macBundle("/usr/local/bin/xmesh"), undefined);
  assert.equal(macBundle("/opt/electron/electron"), undefined);
});

test("macOS refuses read-only copies: AppTranslocation, a mounted disk image, a bare binary", () => {
  const blocked = (p: string) => macApplier(p, undefined, () => true).blocked();
  assert.equal(blocked("/Applications/xmesh.app/Contents/MacOS/xmesh"), "");
  assert.equal(blocked("/private/var/folders/x/AppTranslocation/ABC/d/xmesh.app/Contents/MacOS/xmesh"), "translocated");
  assert.equal(blocked("/Volumes/xmesh 0.1.0/xmesh.app/Contents/MacOS/xmesh"), "translocated");
  assert.equal(blocked("/usr/local/bin/xmesh"), "translocated");
});

test("macOS: an app folder the user cannot write to is reported before anything runs", async () => {
  const dir = tmpDir();
  let spawned = false;
  const a = macApplier("/Applications/xmesh.app/Contents/MacOS/xmesh", () => {
    spawned = true;
    return child();
  }, () => false);
  await assert.rejects(a.start(handOver(dir)), (e: ApplyError) => e.code === "not_writable");
  assert.equal(spawned, false);
});

test("macOS hands /bin/sh the script and four plain arguments", async () => {
  const dir = tmpDir();
  const seen: { cmd: string; args: string[]; opts: Record<string, unknown> }[] = [];
  const a = macApplier("/Applications/xmesh.app/Contents/MacOS/xmesh", (cmd, args, opts) => {
    seen.push({ cmd, args, opts: opts as Record<string, unknown> });
    return child();
  }, () => true);
  await a.start(handOver(dir));
  const s = seen[0]!;
  assert.equal(s.cmd, "/bin/sh");
  assert.deepEqual(s.args.slice(1), ["4242", path.join(dir, "i.dmg"), "/Applications/xmesh.app", path.join(dir, "result.txt")]);
  assert.equal(s.opts.detached, true);
  assert.equal(s.opts.stdio, "ignore");
  assert.equal(fs.readFileSync(s.args[0]!, "utf8"), MAC_SCRIPT);
  // Windows has no execute bit.
  if (process.platform !== "win32") assert.equal(fs.statSync(s.args[0]!).mode & 0o100, 0o100);
});

test("Windows hands cmd a script that holds no path, and passes every path through the environment", async () => {
  const dir = tmpDir();
  const seen: { cmd: string; args: string[]; opts: { env?: Record<string, string>; windowsVerbatimArguments?: boolean } }[] = [];
  const a = windowsApplier((cmd, args, opts) => {
    seen.push({ cmd, args, opts: opts as never });
    return child();
  });
  assert.equal(a.blocked(), "");
  const h = { ...handOver(dir), installer: "C:\\Users\\a b\\xmesh-ui-0.1.0-win-x64.exe", execPath: "C:\\Users\\a b\\xmesh\\xmesh.exe" };
  await a.start(h);
  const s = seen[0]!;
  assert.equal(s.cmd, "cmd.exe");
  assert.deepEqual(s.args.slice(0, 3), ["/d", "/s", "/c"]);
  assert.equal(s.args[3], `"${path.join(dir, "apply-update.cmd")}"`);
  assert.equal(s.opts.windowsVerbatimArguments, true);
  assert.equal(s.opts.env!.XMESH_UPD_PID, "4242");
  assert.equal(s.opts.env!.XMESH_UPD_INSTALLER, h.installer);
  assert.equal(s.opts.env!.XMESH_UPD_APP, h.execPath);
  assert.equal(s.opts.env!.XMESH_UPD_RESULT, h.resultFile);
  assert.equal(fs.readFileSync(path.join(dir, "apply-update.cmd"), "utf8"), WINDOWS_SCRIPT);
});

test("the Windows script is ASCII, CRLF, waits for the app, runs the installer silently and starts the app only on success", () => {
  assert.ok(/^[\x20-\x7e\r\n]*$/.test(WINDOWS_SCRIPT), "cmd reads it in the console code page");
  assert.ok(WINDOWS_SCRIPT.includes("\r\n") && !/[^\r]\n/.test(WINDOWS_SCRIPT));
  const lines = WINDOWS_SCRIPT.split("\r\n");
  const at = (needle: string): number => lines.findIndex((l) => l.includes(needle));
  assert.ok(at("tasklist") >= 0 && at("tasklist") < at("/S"));
  // --updated keeps the kernels' logon tasks (build/installer.nsh skips `xmesh uninstall` for an upgrade).
  assert.ok(at('"%XMESH_UPD_INSTALLER%" /S --updated') > at(":gone"));
  const ok = lines.slice(at('if not "%code%"=="0" goto failed'));
  assert.ok(ok[1]!.startsWith('start "" "%XMESH_UPD_APP%"'), "the app starts right after a successful install");
  for (const forbidden of [/[A-Za-z]:\\/, /timeout/i, /del /i, /rmdir/i]) assert.ok(!forbidden.test(WINDOWS_SCRIPT), String(forbidden));
});

// --- the macOS script itself, run against stub tools ---

const sh = process.platform === "win32" ? { skip: "needs /bin/sh" } : {};

interface Rig {
  dir: string;
  app: string;
  log: string;
  result: string;
  run(opts?: { dittoFails?: boolean; noApp?: boolean; attachFails?: boolean; pid?: number }): { status: number | null; log: string[] };
}

function rig(): Rig {
  const dir = fs.realpathSync(tmpDir());
  const bin = path.join(dir, "bin");
  fs.mkdirSync(bin);
  const log = path.join(dir, "log");
  const app = path.join(dir, "Applications", "xmesh.app");
  fs.mkdirSync(path.join(app, "Contents"), { recursive: true });
  fs.writeFileSync(path.join(app, "Contents", "version"), "old");
  const stub = (name: string, body: string): void => fs.writeFileSync(path.join(bin, name), `#!/bin/sh\n${body}\n`, { mode: 0o755 });
  // hdiutil attach fills the mountpoint like a mounted image would; detach empties it again.
  stub(
    "hdiutil",
    `echo "hdiutil $*" >> "${log}"
if [ "$1" = attach ]; then
  [ -n "$STUB_ATTACH_FAILS" ] && exit 1
  while [ $# -gt 0 ]; do [ "$1" = -mountpoint ] && mp="$2"; shift; done
  [ -n "$STUB_NO_APP" ] || { mkdir -p "$mp/xmesh.app/Contents"; echo new > "$mp/xmesh.app/Contents/version"; }
fi
if [ "$1" = detach ]; then rm -rf "$2"/*; fi
exit 0`,
  );
  stub("ditto", `echo "ditto" >> "${log}"; [ -n "$STUB_DITTO_FAILS" ] && { mkdir -p "$2"; exit 1; }; cp -R "$1" "$2"`);
  stub("xattr", `echo "xattr $*" >> "${log}"`);
  stub("open", `echo "open $*" >> "${log}"`);
  const result = path.join(dir, "result.txt");
  const script = path.join(dir, "apply.sh");
  fs.writeFileSync(script, MAC_SCRIPT);
  return {
    dir,
    app,
    log,
    result,
    run: (o = {}) => {
      // A process that has already ended: its pid is not alive any more.
      const gone = o.pid ?? spawnSync("true").pid!;
      const r = spawnSync("/bin/sh", [script, String(gone), path.join(dir, "i.dmg"), app, result], {
        env: {
          PATH: `${bin}:${process.env.PATH}`,
          TMPDIR: dir,
          ...(o.dittoFails && { STUB_DITTO_FAILS: "1" }),
          ...(o.noApp && { STUB_NO_APP: "1" }),
          ...(o.attachFails && { STUB_ATTACH_FAILS: "1" }),
        },
        encoding: "utf8",
      });
      return { status: r.status, log: fs.existsSync(log) ? fs.readFileSync(log, "utf8").trim().split("\n") : [] };
    },
  };
}

test("macOS script: swaps the bundle, clears quarantine, detaches and opens the new app", sh, () => {
  const r = rig();
  const out = r.run();
  assert.equal(out.status, 0);
  assert.equal(fs.readFileSync(path.join(r.app, "Contents", "version"), "utf8").trim(), "new");
  assert.equal(fs.existsSync(`${r.app}.update-old`), false);
  assert.equal(fs.existsSync(r.result), false);
  const cmds = out.log.map((l) => l.split(" ")[0]);
  assert.deepEqual(cmds, ["hdiutil", "ditto", "xattr", "hdiutil", "open"]);
  assert.match(out.log[0]!, /attach -nobrowse -readonly/);
  assert.match(out.log[2]!, /-dr com\.apple\.quarantine /);
  assert.match(out.log[3]!, /detach/);
  assert.equal(out.log[4], `open ${r.app}`);
  assert.deepEqual(fs.readdirSync(r.dir).filter((n) => n.startsWith("xmesh-update.")), [], "the mount point folder is removed");
});

test("macOS script: a failed copy puts the old app back, says why and opens the old app", sh, () => {
  const r = rig();
  const out = r.run({ dittoFails: true });
  assert.equal(out.status, 1);
  assert.equal(fs.readFileSync(path.join(r.app, "Contents", "version"), "utf8").trim(), "old");
  assert.equal(fs.existsSync(`${r.app}.update-old`), false);
  assert.match(fs.readFileSync(r.result, "utf8"), /could not be copied/);
  assert.equal(out.log.at(-1), `open ${r.app}`);
});

test("macOS script: an image that cannot be opened or holds no app leaves the old app alone", sh, () => {
  for (const [opt, why] of [[{ attachFails: true }, /could not be opened/], [{ noApp: true }, /no app/]] as const) {
    const r = rig();
    const out = r.run(opt);
    assert.equal(out.status, 1);
    assert.equal(fs.readFileSync(path.join(r.app, "Contents", "version"), "utf8").trim(), "old");
    assert.match(fs.readFileSync(r.result, "utf8"), why);
    assert.equal(out.log.at(-1), `open ${r.app}`);
  }
});

test("macOS script: paths with spaces and quotes survive", sh, () => {
  const r = rig();
  const odd = path.join(r.dir, "Apps 'x' y", "xmesh.app");
  fs.mkdirSync(path.dirname(odd), { recursive: true });
  fs.renameSync(r.app, odd);
  const script = path.join(r.dir, "apply.sh");
  const gone = spawnSync("true").pid!;
  const env = { PATH: `${path.join(r.dir, "bin")}:${process.env.PATH}`, TMPDIR: r.dir };
  const s = spawnSync("/bin/sh", [script, String(gone), path.join(r.dir, "i.dmg"), odd, r.result], { env });
  assert.equal(s.status, 0);
  assert.equal(fs.readFileSync(path.join(odd, "Contents", "version"), "utf8").trim(), "new");
});
