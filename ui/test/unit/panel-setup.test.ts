import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { CliError } from "../../src/main/cli";
import { parse } from "yaml";
import { type SetupHost, fsSetupHost, previewSetup, runSetup } from "../../src/main/setup";
import { DEFAULT_CONTROL_URL, DEFAULT_DENY, DENY_FILE_NAME, denyListText, generateConfig, generateDenyFile, isAbsolutePath, parseSetup } from "../../src/shared/setup";

const lan = { nodeName: "dev-1", mode: "lan", roles: ["agent", "gateway"], workspace: "/home/me/work" };
const tailnet = { ...lan, mode: "tailnet", authkeyFile: "/home/me/authkey" };
/** The deny list file the first run writes next to `dev-1`'s config. */
const deny = path.join("/state", "dev-1", DENY_FILE_NAME);

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

test("a gateway-only node still gets the agent section the config schema requires", () => {
  const bad = parseSetup({ nodeName: "gw", mode: "lan", roles: ["gateway"], workspace: "" });
  assert.ok(!bad.ok && bad.problem === "workspace");
  const text = generateConfig(must({ nodeName: "gw", mode: "lan", roles: ["gateway"], workspace: "/home/me/work" }), deny);
  assert.match(text, /roles: \[gateway\]/);
  assert.match(text, /gateway:/);
  assert.match(text, /^agent:\n/m);
  assert.match(text, /allow: \["\/home\/me\/work"\]/);
});

test("trusted callers: LAN peers by pairing tag, tailnet devices by the tag the control plane grants", () => {
  assert.match(generateConfig(must(lan), deny), /^ {4}"tag:peer":/m);
  assert.doesNotMatch(generateConfig(must(lan), deny), /tag:device/);
  const tn = generateConfig(must({ ...lan, mode: "tailnet", authkeyFile: "/k" }), deny);
  assert.match(tn, /^ {4}"tag:device":/m);
  assert.doesNotMatch(tn, /tag:peer/);
});

test("generated config carries the values and the safe defaults", () => {
  const text = generateConfig(must(lan), deny);
  assert.match(text, /^node:\n {2}name: "dev-1"\n {2}roles: \[agent, gateway\]\n/m);
  assert.match(text, /mdns: true/);
  assert.match(text, /default_cwd: "\/home\/me\/work"/);
  assert.match(text, /listen: "127\.0\.0\.1:8080"/);
  const tn = generateConfig(must(tailnet), deny);
  assert.match(tn, /control_url: "https:\/\/controlplane\.tailscale\.com"/);
  assert.match(tn, /authkey_file: "\/home\/me\/authkey"/);
  assert.doesNotMatch(tn, /mdns/);
});

test("quoting keeps hostile paths inside their scalar", () => {
  const evil = '/home/me/", deny: [] # \\n x: !!python/object';
  assert.equal(isAbsolutePath(evil), true, "a path with quotes is still a path");
  const text = generateConfig(must({ ...lan, workspace: evil }), deny);
  // Each user value sits on one line, as a JSON string.
  const line = text.split("\n").find((l) => l.startsWith("  default_cwd:"))!;
  assert.equal(JSON.parse(line.slice("  default_cwd:".length)), evil);
  assert.equal(text.split("\n").filter((l) => /^\s*deny_file:/.test(l)).length, 1);
  assert.equal(parseSetup({ ...lan, workspace: "/w\ndeny: []" }).ok, false, "a newline never reaches the generator");
});

function recordingHost(stateRoot: string, opts: { exists?: boolean; existing?: string; failOn?: string } = {}) {
  const calls: string[] = [];
  const written: Record<string, string> = {};
  const host: SetupHost = {
    stateRoot,
    readText: () => opts.existing,
    writeNew: (file, content) => {
      if (opts.exists || opts.existing !== undefined) throw Object.assign(new Error("exists"), { code: "EEXIST" });
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
  assert.equal(written[cfg], generateConfig(must(lan), deny));
  assert.equal(written[deny], generateDenyFile(), "the default deny list is written beside the config");
});

test("the default deny list file is the defaults, in a form that reads back to them", () => {
  assert.deepEqual(parse(generateDenyFile()), DEFAULT_DENY);
  assert.deepEqual(parse(denyListText([])), []);
  // An entry with quotes and a backslash stays one entry, and cannot end its line early.
  const tricky = 'C:\\Users\\me\\"x": 1';
  assert.deepEqual(parse(denyListText([tricky])), [tricky]);
});

test("runSetup keeps a deny list that is already there and still writes the config", async () => {
  const { host, calls, written } = recordingHost("/state");
  const create = host.writeNew;
  host.writeNew = (file, content) => {
    if (file === deny) throw Object.assign(new Error("exists"), { code: "EEXIST" });
    create(file, content);
  };
  const r = await runSetup(lan, host);
  assert.ok(r.ok);
  assert.deepEqual(r.data.steps.map((s) => [s.id, s.ok]), [["write-config", true], ["install", true], ["start", true]]);
  assert.equal(written[deny], undefined, "the user's list is not replaced");
  assert.equal(calls.length, 2);
});

test("the review text is what runSetup writes, and asking for it writes nothing", async () => {
  const asked = recordingHost("/state");
  const preview = previewSetup(lan, asked.host);
  assert.ok(preview.ok);
  assert.deepEqual(asked.written, {});
  assert.equal(preview.data.denyPath, deny);
  const ran = recordingHost("/state");
  await runSetup(lan, ran.host);
  assert.equal(preview.data.config, ran.written[preview.data.configPath]);
  assert.deepEqual(previewSetup({ ...lan, nodeName: "../../etc" }, asked.host), {
    ok: false,
    error: { code: "invalid_args", message: "节点名只能含字母、数字和连字符，且以字母或数字开头", field: "node_name" },
  });
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

test("runSetup retries after a failed install: the identical file it wrote before is reused, anything else is not", async () => {
  const same = recordingHost("/state", { existing: generateConfig(must(lan), deny) });
  const r = await runSetup(lan, same.host);
  assert.ok(r.ok);
  const cfg = path.join("/state", "dev-1", "xmesh.yaml");
  assert.deepEqual(r.data.steps.map((s) => [s.id, s.ok]), [["write-config", true], ["install", true], ["start", true]]);
  assert.deepEqual(same.calls, [`install --config ${cfg}`, `start --config ${cfg}`]);
  assert.deepEqual(same.written, {}, "nothing was rewritten");

  const other = recordingHost("/state", { existing: "node: {name: dev-1}\n# edited by hand\n" });
  const c = await runSetup(lan, other.host);
  assert.ok(!c.ok);
  assert.equal(c.error.code, "conflict");
  assert.deepEqual(other.calls, []);
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
    assert.equal(host.readText(file), "a: 1\n");
    assert.equal(host.readText(path.join(root, "missing")), undefined);
    assert.equal(fs.readFileSync(file, "utf8"), "a: 1\n");
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
