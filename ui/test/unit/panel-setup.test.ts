import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { CliError } from "../../src/main/cli";
import { type SetupHost, fsSetupHost, runSetup } from "../../src/main/setup";
import { DEFAULT_CONTROL_URL, generateConfig, isAbsolutePath, parseSetup } from "../../src/shared/setup";

const lan = { nodeName: "dev-1", mode: "lan", roles: ["agent", "gateway"], workspace: "/home/me/work" };
const tailnet = { ...lan, mode: "tailnet", authkeyFile: "/home/me/authkey" };

function must(v: unknown) {
  const r = parseSetup(v);
  assert.ok(r.ok, JSON.stringify(r));
  return r.value;
}

test("absolute path check rejects relative paths, traversal and control characters", () => {
  for (const p of ["/a/b", "C:\\work", "C:/work", "\\\\srv\\share\\x"]) assert.equal(isAbsolutePath(p), true, p);
  for (const p of ["", "a/b", "./a", "/a/../b", "C:\\a\\..\\b", "/a\nb", "/a\0b", `/${"x".repeat(1100)}`]) {
    assert.equal(isAbsolutePath(p), false, JSON.stringify(p));
  }
});

test("parseSetup accepts the lan and tailnet shapes", () => {
  assert.deepEqual(must(lan), lan);
  const t = must(tailnet);
  assert.equal(t.controlUrl, DEFAULT_CONTROL_URL);
  assert.equal(t.authkeyFile, "/home/me/authkey");
});

test("parseSetup refuses bad input and names the first offending field", () => {
  const problem = (v: unknown): string | undefined => {
    const r = parseSetup(v);
    return r.ok ? undefined : r.problem;
  };
  assert.equal(problem(null), "node_name");
  assert.equal(problem({ ...lan, nodeName: "bad name" }), "node_name");
  assert.equal(problem({ ...lan, nodeName: "x\ny: z" }), "node_name");
  assert.equal(problem({ ...lan, nodeName: "-x" }), "node_name");
  assert.equal(problem({ ...lan, extra: 1 }), "node_name");
  assert.equal(problem({ ...lan, mode: "wan" }), "mode");
  assert.equal(problem({ ...lan, authkeyFile: "/k" }), "mode");
  assert.equal(problem({ ...lan, roles: [] }), "roles");
  assert.equal(problem({ ...lan, roles: ["root"] }), "roles");
  assert.equal(problem({ ...lan, roles: ["agent", "agent"] }), "roles");
  assert.equal(problem({ ...lan, workspace: "relative" }), "workspace");
  assert.equal(problem({ ...tailnet, controlUrl: "http://x.example" }), "control_url");
  assert.equal(problem({ ...tailnet, controlUrl: "https://u:p@x.example" }), "control_url");
  assert.equal(problem({ ...tailnet, controlUrl: 'https://x.example/"\ninject: 1' }), "control_url");
  assert.equal(problem({ ...tailnet, authkeyFile: "key" }), "authkey_file");
  assert.equal(problem({ ...tailnet, authkeyFile: undefined }), "authkey_file");
});

test("a gateway-only node needs no workspace and gets none", () => {
  const v = must({ nodeName: "gw", mode: "lan", roles: ["gateway"], workspace: "" });
  assert.equal(v.workspace, "");
  const text = generateConfig(v);
  assert.doesNotMatch(text, /agent:/);
  assert.match(text, /gateway:/);
});

test("generated config carries the values and the safe defaults", () => {
  const text = generateConfig(must(lan));
  assert.match(text, /^node:\n {2}name: "dev-1"\n {2}roles: \[agent, gateway\]\n/m);
  assert.match(text, /mdns: true/);
  assert.match(text, /default_cwd: "\/home\/me\/work"/);
  assert.match(text, /deny: \["~\/\.ssh"/);
  assert.match(text, /listen: "127\.0\.0\.1:8080"/);
  const tn = generateConfig(must(tailnet));
  assert.match(tn, /control_url: "https:\/\/controlplane\.tailscale\.com"/);
  assert.match(tn, /authkey_file: "\/home\/me\/authkey"/);
  assert.doesNotMatch(tn, /mdns/);
});

test("quoting keeps hostile paths inside their scalar", () => {
  const evil = '/home/me/", deny: [] # \\n x: !!python/object';
  assert.equal(isAbsolutePath(evil), true, "a path with quotes is still a path");
  const text = generateConfig(must({ ...lan, workspace: evil }));
  // Each user value sits on one line, as a JSON string.
  const line = text.split("\n").find((l) => l.startsWith("  default_cwd:"))!;
  assert.equal(JSON.parse(line.slice("  default_cwd:".length)), evil);
  assert.equal(text.split("\n").filter((l) => /^\s*deny:/.test(l)).length, 1);
  assert.equal(parseSetup({ ...lan, workspace: "/w\ndeny: []" }).ok, false, "a newline never reaches the generator");
});

function recordingHost(stateRoot: string, opts: { exists?: boolean; failOn?: string } = {}) {
  const calls: string[] = [];
  const written: Record<string, string> = {};
  const host: SetupHost = {
    stateRoot,
    writeNew: (file, content) => {
      if (opts.exists) throw Object.assign(new Error("exists"), { code: "EEXIST" });
      written[file] = content;
    },
    runCli: async (args) => {
      calls.push(args.join(" "));
      if (args[0] === opts.failOn) throw new CliError("install", "boom");
      return `${args[0]} ok`;
    },
  };
  return { host, calls, written };
}

test("runSetup validates, writes the regenerated config, then installs and starts", async () => {
  const { host, calls, written } = recordingHost("/state");
  const r = await runSetup({ ...lan }, host);
  assert.ok(r.ok);
  const cfg = path.join("/state", "dev-1", "xmesh.yaml");
  assert.equal(r.data.configPath, cfg);
  assert.deepEqual(r.data.steps.map((s) => [s.id, s.ok]), [["write-config", true], ["install", true], ["start", true]]);
  assert.deepEqual(calls, [`install --config ${cfg}`, `start --config ${cfg}`]);
  assert.equal(written[cfg], generateConfig(must(lan)));
});

test("runSetup stops after a failed step and reports it", async () => {
  const { host, calls } = recordingHost("/state", { failOn: "install" });
  const r = await runSetup(lan, host);
  assert.ok(r.ok);
  assert.deepEqual(r.data.steps.map((s) => [s.id, s.ok]), [["write-config", true], ["install", false]]);
  assert.equal(calls.length, 1, "start was not attempted");
});

test("runSetup never overwrites an existing config and runs nothing", async () => {
  const { host, calls } = recordingHost("/state", { exists: true });
  const r = await runSetup(lan, host);
  assert.ok(!r.ok);
  assert.equal(r.error.code, "conflict");
  assert.deepEqual(calls, []);
});

test("runSetup rejects invalid parameters before touching anything", async () => {
  const { host, calls, written } = recordingHost("/state");
  const r = await runSetup({ ...lan, nodeName: "../../etc" }, host);
  assert.ok(!r.ok);
  assert.equal(r.error.code, "invalid_args");
  assert.equal(r.error.field, "node_name");
  assert.deepEqual(calls, []);
  assert.deepEqual(written, {});
});

test("fsSetupHost writes 0600 and refuses to replace a file", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-setup-"));
  try {
    const host = fsSetupHost(root, async () => "");
    const file = path.join(root, "n", "xmesh.yaml");
    host.writeNew(file, "a: 1\n");
    assert.equal(fs.readFileSync(file, "utf8"), "a: 1\n");
    if (process.platform !== "win32") assert.equal(fs.statSync(file).mode & 0o777, 0o600);
    assert.throws(() => host.writeNew(file, "b: 2\n"), { code: "EEXIST" });
    assert.equal(fs.readFileSync(file, "utf8"), "a: 1\n");
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
