import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { runServiceCommand } from "../../src/web/main";
import { loadKey } from "../../src/web/auth";
import { SERVICE_NAME, findXmesh, installService, renderUnit, unitDir, unitWord, uninstallService, type Deps, type Run } from "../../src/web/service";

function tmp(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-websvc-"));
}

interface Fake {
  deps: Deps;
  calls: string[];
  out: string[];
  fail: (match: string, run?: Partial<Run>) => void;
}

function fake(home: string): Fake {
  const calls: string[] = [];
  const out: string[] = [];
  const failures = new Map<string, Run>();
  return {
    calls,
    out,
    fail: (match, run = {}) => { failures.set(match, { status: 1, stdout: "", stderr: "", ...run }); },
    deps: {
      home,
      env: { HOME: home },
      out: (line) => out.push(line),
      run: (cmd, args) => {
        const line = `${cmd} ${args.join(" ")}`;
        calls.push(line);
        for (const [m, r] of failures) if (line.includes(m)) return r;
        return { status: 0, stdout: "", stderr: "" };
      },
    },
  };
}

const base = (dir: string) => ({
  node: "/usr/bin/node", script: "/opt/xmesh/webui/xmesh-webui.js", listen: "127.0.0.1:7480",
  dataDir: path.join(dir, "data"), keyFile: path.join(dir, "data", "webui.key"), unitDir: path.join(dir, "units"),
});

test("unit words are quoted for systemd and never expand", () => {
  assert.equal(unitWord("/usr/bin/node"), "/usr/bin/node");
  assert.equal(unitWord("127.0.0.1:7480"), "127.0.0.1:7480");
  assert.equal(unitWord("/home/a b/x"), '"/home/a b/x"');
  assert.equal(unitWord("100%"), "100%%");
  assert.equal(unitWord("$HOME/x"), "$$HOME/x");
  assert.equal(unitWord('say "hi"'), '"say \\"hi\\""');
  assert.equal(unitWord("a\\b c"), '"a\\\\b c"');
  for (const bad of ["", "a\nb", "a\0b", "a\rb"]) assert.throws(() => unitWord(bad));
});

test("the unit runs the script as a restartable user service with its own settings", () => {
  const unit = renderUnit({ node: "/usr/bin/node", script: "/opt/x/xmesh-webui.js", listen: "127.0.0.1:7480", dataDir: "/home/me/.config/xmesh-webui", keyFile: "/home/me/.config/xmesh-webui/webui.key", xmeshBin: "/home/me/.local/bin/xmesh", lang: "en" });
  assert.match(unit, /^ExecStart=\/usr\/bin\/node \/opt\/x\/xmesh-webui\.js --listen 127\.0\.0\.1:7480 --data-dir \/home\/me\/\.config\/xmesh-webui --key-file \/home\/me\/\.config\/xmesh-webui\/webui\.key --lang en$/m);
  assert.match(unit, /^Environment=XMESH_BIN=\/home\/me\/\.local\/bin\/xmesh$/m);
  assert.match(unit, /^ExecReload=\/bin\/kill -HUP \$MAINPID$/m);
  assert.match(unit, /^Restart=on-failure$/m);
  assert.match(unit, /^WantedBy=default\.target$/m);
  assert.doesNotMatch(unit, /management key|[^_]KEY=/i, "the key itself never goes into the unit");
  const tls = renderUnit({ node: "/n", script: "/s", listen: "0.0.0.0:7480", dataDir: "/d", keyFile: "/k", tlsCert: "/c.pem", tlsKey: "/c.key" });
  assert.match(tls, /--tls-cert \/c\.pem --tls-key \/c\.key/);
  assert.doesNotMatch(tls, /XMESH_BIN/);
  assert.throws(() => renderUnit({ node: "node", script: "/s", listen: "x", dataDir: "/d", keyFile: "/k" }), /absolute/);
  assert.throws(() => renderUnit({ node: "/n", script: "/s\ninjected", listen: "x", dataDir: "/d", keyFile: "/k" }));
});

test("install writes the key and unit, then reloads, enables and restarts", { skip: process.platform === "win32" }, () => {
  const dir = tmp();
  const f = fake(dir);
  const r = installService({ ...base(dir), xmeshBin: "/usr/local/bin/xmesh" }, f.deps);
  assert.deepEqual(f.calls, [`systemctl --user daemon-reload`, `systemctl --user enable ${SERVICE_NAME}`, `systemctl --user restart ${SERVICE_NAME}`]);
  assert.equal(r.unit, path.join(dir, "units", `${SERVICE_NAME}.service`));
  assert.match(fs.readFileSync(r.unit, "utf8"), /ExecStart=\/usr\/bin\/node /);
  assert.equal(r.url, "http://127.0.0.1:7480/");
  assert.ok(r.key && r.key.length >= 40, "a generated key is returned once");
  assert.equal(fs.statSync(r.keyFile).mode & 0o777, 0o600);
  assert.equal(loadKey(r.keyFile).key, r.key);

  const again = installService(base(dir), f.deps);
  assert.equal(again.key, undefined, "the key is not shown again");
  assert.equal(loadKey(r.keyFile).key, r.key);
});

test("a key the user typed is stored as given, and only shown when it changes", { skip: process.platform === "win32" }, () => {
  const dir = tmp();
  const f = fake(dir);
  assert.throws(() => installService({ ...base(dir), key: "short" }, f.deps), /16/);
  assert.throws(() => installService({ ...base(dir), key: "has a space in it ok" }, f.deps), /空白/);
  const r = installService({ ...base(dir), key: "my-own-long-management-key" }, f.deps);
  assert.equal(r.key, "my-own-long-management-key");
  assert.equal(fs.readFileSync(r.keyFile, "utf8").trim(), "my-own-long-management-key");
  assert.equal(fs.statSync(r.keyFile).mode & 0o777, 0o600);
  assert.equal(installService({ ...base(dir), key: "my-own-long-management-key" }, f.deps).key, undefined);
  const rotated = installService({ ...base(dir), rotateKey: true }, f.deps);
  assert.ok(rotated.key && rotated.key !== "my-own-long-management-key");
});

test("a non-local address needs TLS, or an explicit --insecure", () => {
  const dir = tmp();
  const f = fake(dir);
  assert.throws(() => installService({ ...base(dir), listen: "0.0.0.0:7480" }, f.deps), /TLS/);
  assert.throws(() => installService({ ...base(dir), listen: ":7480" }, f.deps), /TLS/);
  assert.throws(() => installService({ ...base(dir), listen: "192.168.1.5:7480" }, f.deps), /--insecure/);
  assert.equal(f.calls.length, 0, "nothing was touched");
  assert.equal(fs.existsSync(path.join(dir, "data")), false);
  installService({ ...base(dir), listen: "0.0.0.0:7480", insecure: true }, f.deps);
  for (const listen of ["127.0.0.1:1", "[::1]:1", "localhost:1"]) installService({ ...base(dir), listen }, f.deps);
  const cert = path.join(dir, "c.pem");
  const key = path.join(dir, "c.key");
  fs.writeFileSync(cert, "x");
  fs.writeFileSync(key, "x");
  const tls = installService({ ...base(dir), listen: "0.0.0.0:7480", tlsCert: cert, tlsKey: key }, f.deps);
  assert.equal(tls.url, "https://0.0.0.0:7480/");
  assert.throws(() => installService({ ...base(dir), tlsCert: cert }, f.deps), /together/);
  assert.throws(() => installService({ ...base(dir), tlsCert: path.join(dir, "missing.pem"), tlsKey: key }, f.deps), /ENOENT/);
});

test("--no-start enables without starting, --print changes nothing, --linger failing only warns", () => {
  const dir = tmp();
  const f = fake(dir);
  installService({ ...base(dir), noStart: true }, f.deps);
  assert.deepEqual(f.calls, ["systemctl --user daemon-reload", `systemctl --user enable ${SERVICE_NAME}`]);

  const p = fake(tmp());
  const dir2 = tmp();
  const r = installService({ ...base(dir2), print: true }, p.deps);
  assert.equal(p.calls.length, 0);
  assert.equal(r.unit, "");
  assert.match(p.out.join("\n"), /ExecStart=/);
  assert.equal(fs.existsSync(path.join(dir2, "data")), false);

  const l = fake(tmp());
  l.fail("loginctl", { stderr: "Access denied" });
  installService({ ...base(tmp()), linger: true }, l.deps);
  assert.ok(l.calls.includes("loginctl enable-linger"));
  assert.match(l.out.join("\n"), /enable-linger 失败（Access denied）/);
});

test("without a systemd user session the error says what to do", () => {
  const dir = tmp();
  const f = fake(dir);
  f.fail("daemon-reload", { stderr: "Failed to connect to bus: No medium found" });
  assert.throws(() => installService(base(dir), f.deps), /enable-linger[\s\S]*XDG_RUNTIME_DIR/);
  const g = fake(tmp());
  g.fail("restart", { stderr: "Unit xmesh-webui.service failed to load" });
  assert.throws(() => installService(base(tmp()), g.deps), /systemctl restart failed: Unit xmesh-webui\.service failed to load$/);
});

test("uninstall stops and removes the unit; the key stays unless purged", { skip: process.platform === "win32" }, () => {
  const dir = tmp();
  const f = fake(dir);
  const r = installService(base(dir), f.deps);
  f.calls.length = 0;
  uninstallService({ dataDir: base(dir).dataDir, keyFile: r.keyFile, unitDir: base(dir).unitDir }, f.deps);
  assert.deepEqual(f.calls, [`systemctl --user disable --now ${SERVICE_NAME}`, "systemctl --user daemon-reload"]);
  assert.equal(fs.existsSync(r.unit), false);
  assert.equal(fs.existsSync(r.keyFile), true);
  uninstallService({ dataDir: base(dir).dataDir, keyFile: r.keyFile, unitDir: base(dir).unitDir, purge: true }, f.deps);
  assert.equal(fs.existsSync(r.keyFile), false);
  assert.equal(fs.existsSync(base(dir).dataDir), false);
  // a service that was never installed is not an error
  const h = fake(dir);
  h.fail("disable", { stderr: "Unit file xmesh-webui.service does not exist" });
  uninstallService({ dataDir: base(dir).dataDir, keyFile: r.keyFile, unitDir: base(dir).unitDir }, h.deps);
});

test("xmesh is found from XMESH_BIN or PATH, as absolute paths that exist", () => {
  const exists = (p: string): boolean => p === "/home/me/.local/bin/xmesh" || p === "/opt/bin/xmesh";
  assert.equal(findXmesh({ XMESH_BIN: "/opt/bin/xmesh", PATH: "/usr/bin" }, exists), "/opt/bin/xmesh");
  assert.equal(findXmesh({ PATH: "/usr/bin:/home/me/.local/bin" }, exists), "/home/me/.local/bin/xmesh");
  assert.equal(findXmesh({ XMESH_BIN: "xmesh", PATH: "bin:/usr/bin" }, exists), undefined);
  assert.equal(findXmesh({ XMESH_BIN: "/nope/xmesh", PATH: "/usr/bin" }, exists), undefined);
  assert.equal(unitDir({ XDG_CONFIG_HOME: "/x" }, "/h"), "/x/systemd/user");
  assert.equal(unitDir({}, "/h"), "/h/.config/systemd/user");
});

test("the install command wires the options through, reads the key from stdin and shows only a generated key", { skip: process.platform === "win32" }, () => {
  const dir = tmp();
  const f = fake(dir);
  const env = { HOME: dir, XDG_CONFIG_HOME: path.join(dir, "cfg"), PATH: "" };
  const code = runServiceCommand("install", ["--key-stdin", "--linger", "--lang", "en", "--xmesh-bin", "/opt/bin/xmesh"], env, f.deps, () => "typed-by-the-user-123", "/opt/xmesh/xmesh-webui.js");
  assert.equal(code, 0);
  const unit = fs.readFileSync(path.join(dir, ".config", "systemd", "user", `${SERVICE_NAME}.service`), "utf8");
  assert.match(unit, new RegExp(`--key-file ${path.join(dir, "cfg", "xmesh-webui", "webui.key").replaceAll(".", "\\.")} --lang en`));
  assert.match(unit, /XMESH_BIN=\/opt\/bin\/xmesh/);
  assert.doesNotMatch(f.out.join("\n"), /typed-by-the-user-123|management key/, "a key the user supplied is not echoed");
  assert.ok(f.calls.includes("loginctl enable-linger"));
  assert.equal(fs.readFileSync(path.join(dir, "cfg", "xmesh-webui", "webui.key"), "utf8").trim(), "typed-by-the-user-123");

  const generated = fake(tmp());
  const env2 = { HOME: dir, XDG_CONFIG_HOME: path.join(dir, "cfg2"), PATH: "" };
  assert.equal(runServiceCommand("install", ["--xmesh-bin", "/opt/bin/xmesh"], env2, generated.deps, () => "", "/opt/xmesh/xmesh-webui.js"), 0);
  assert.match(generated.out.join("\n"), /management key: [A-Za-z0-9_-]{43}/, "a generated key is shown once");

  const bad = fake(dir);
  assert.equal(runServiceCommand("install", ["--listen", "nope"], env, bad.deps, () => "", "/s.js"), 1);
  assert.equal(runServiceCommand("install", ["--key-stdin", "--rotate-key"], env, bad.deps, () => "x".repeat(20), "/s.js"), 1);
  assert.equal(bad.calls.length, 0);
  assert.equal(runServiceCommand("uninstall", ["--purge"], env, f.deps, () => "", "/s.js"), 0);
  assert.equal(fs.existsSync(path.join(dir, "cfg", "xmesh-webui")), false);
});
