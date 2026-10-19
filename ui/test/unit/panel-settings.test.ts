import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { test } from "node:test";
import Ajv2020 from "ajv/dist/2020";
import { parse } from "yaml";
import {
  applyDenyFile,
  applySettingsForm,
  checkDenyList,
  checkSettingsForm,
  formFromConfig,
  isChanged,
  parseDenyList,
  toggleRole,
  type SettingsForm,
} from "../../src/renderer/lib/settings";
import { DEFAULT_CONTROL_URL, DEFAULT_DENY, denyListText } from "../../src/shared/setup";
import { schemaDir } from "../../testserver/fixtures";

// The wizard's lan config, with a comment the edit must keep and a deny list it must not touch.
const LAN_CONFIG = [
  "# 由 xmesh 首次运行向导生成。",
  "node:",
  '  name: "dev-1"',
  "  roles: [agent, gateway]",
  "mesh:",
  "  mode: lan",
  "  lan:",
  "    mdns: true",
  "gateway:",
  '  listen: "127.0.0.1:8080"',
  "agent:",
  '  default_cwd: "/home/me/work"',
  "  cwd_policy:",
  '    allow: ["/home/me/work"]',
  '    deny: ["~/.ssh"]',
  '    deny_file: "/home/me/.local/state/xmesh/dev-1/cwd-deny.yaml"',
  "  authz:",
  '    "tag:peer":',
  '      tools: ["*"]',
  '      backends: ["*"]',
  "",
].join("\n");

const lan: SettingsForm = {
  nodeName: "dev-1",
  mode: "lan",
  controlUrl: DEFAULT_CONTROL_URL,
  authkeyFile: "",
  browserLogin: false,
  roles: ["agent", "gateway"],
  listen: "tunnel",
  issuer: "",
  domain: "",
  tls: "autocert",
  certFile: "",
  keyFile: "",
  defaultCwd: "/home/me/work",
  allowDirs: ["/home/me/work"],
  maxFileSize: "1GiB",
};
const tailnetIngress: SettingsForm = {
  ...lan,
  mode: "tailnet",
  authkeyFile: "/home/me/authkey",
  roles: ["agent", "gateway", "ingress"],
  issuer: "https://xmesh.example.com",
};

const configSchema = JSON.parse(fs.readFileSync(path.join(schemaDir(), "config.schema.json"), "utf8")) as object;
const validateConfig = new Ajv2020({ strict: false, allErrors: true }).compile(configSchema);

test("the form reads what the first run set, the ingress and the working directories", () => {
  assert.deepEqual(formFromConfig(parse(LAN_CONFIG)), lan);
  assert.deepEqual(formFromConfig(null), {
    ...lan,
    nodeName: "",
    roles: [],
    defaultCwd: "",
    allowDirs: [],
  });
  const withIngress = formFromConfig({
    node: { name: "dev-1", roles: ["ingress", "agent"] },
    mesh: { mode: "tailnet", control_url: "https://c.example", authkey_file: "/k" },
    agent: { default_cwd: "/w", cwd_policy: { allow: ["/w", "/x"], deny: [] } },
    ingress: { listen: "public:443", domain: "x.example.com", tls: "file", cert_file: "/c", key_file: "/k", oauth: { issuer: "https://x.example.com" } },
  });
  assert.deepEqual(withIngress, {
    nodeName: "dev-1",
    mode: "tailnet",
    controlUrl: "https://c.example",
    authkeyFile: "/k",
    browserLogin: false,
    roles: ["agent", "ingress"],
    listen: "public:443",
    issuer: "https://x.example.com",
    domain: "x.example.com",
    tls: "file",
    certFile: "/c",
    keyFile: "/k",
    defaultCwd: "/w",
    allowDirs: ["/w", "/x"],
    maxFileSize: "1GiB",
  });
});

test("toggling a role keeps the roles in their listed order", () => {
  assert.deepEqual(toggleRole(["gateway"], "agent", true), ["agent", "gateway"]);
  assert.deepEqual(toggleRole(["agent", "ingress"], "gateway", true), ["agent", "gateway", "ingress"]);
  assert.deepEqual(toggleRole(["agent", "gateway"], "gateway", false), ["agent"]);
});

test("isChanged compares the form with what the file says", () => {
  assert.equal(isChanged(lan, { ...lan, roles: ["agent", "gateway"] }), false);
  assert.equal(isChanged(lan, tailnetIngress), true);
  assert.equal(isChanged(lan, { ...lan, allowDirs: [] }), true);
});

test("the check names the first thing the kernel would refuse, without asking it", () => {
  assert.equal(checkSettingsForm({ ...lan, nodeName: "bad name" }), "node_name");
  assert.equal(checkSettingsForm({ ...lan, roles: [] }), "roles");
  assert.equal(checkSettingsForm({ ...lan, defaultCwd: "work" }), "default_cwd");
  assert.equal(checkSettingsForm({ ...lan, allowDirs: ["/a", "relative"] }), "allow_dirs");
  assert.equal(checkSettingsForm({ ...lan, allowDirs: [" "] }), "allow_dirs", "a blank line is not an empty list");
  assert.equal(checkSettingsForm({ ...tailnetIngress, mode: "lan" }), "tailnet");
  assert.equal(checkSettingsForm({ ...lan, controlUrl: "http://c.example" }), undefined, "the control URL is only checked in tailnet mode");
  assert.equal(checkSettingsForm({ ...tailnetIngress, controlUrl: "http://c.example" }), "control_url");
  assert.equal(checkSettingsForm({ ...tailnetIngress, authkeyFile: "authkey" }), "authkey_file");
  assert.equal(checkSettingsForm({ ...tailnetIngress, issuer: "" }), "issuer");
  assert.equal(checkSettingsForm({ ...tailnetIngress, issuer: "http://xmesh.example.com" }), "issuer");
  assert.equal(checkSettingsForm({ ...tailnetIngress, issuer: "https://xmesh.example.com/oauth" }), "issuer");
  assert.equal(checkSettingsForm({ ...tailnetIngress, listen: "public:443", domain: " " }), "domain");
  assert.equal(checkSettingsForm({ ...tailnetIngress, listen: "public:443", domain: "x.example.com", tls: "file", certFile: "cert.pem", keyFile: "/k" }), "cert_files");
  assert.equal(checkSettingsForm({ ...tailnetIngress, listen: "public:443", domain: "x.example.com", tls: "file", certFile: "/c", keyFile: "/k" }), undefined);
  assert.equal(checkSettingsForm(tailnetIngress), undefined);
  assert.equal(checkSettingsForm(lan), undefined);
});

test("an empty allow list is written as an empty list, and several directories as several entries", () => {
  const empty = applySettingsForm(LAN_CONFIG, { ...lan, allowDirs: [] });
  assert.ok(empty !== undefined);
  assert.match(empty, /allow: \[\]/);
  assert.deepEqual(parse(empty).agent.cwd_policy.allow, []);

  const several = applySettingsForm(LAN_CONFIG, { ...lan, allowDirs: ["/home/me/work", "/srv/data"] });
  assert.ok(several !== undefined);
  assert.deepEqual(parse(several).agent.cwd_policy.allow, ["/home/me/work", "/srv/data"]);
  assert.ok(validateConfig(parse(several)), JSON.stringify(validateConfig.errors));
  assert.ok(validateConfig(parse(empty)), JSON.stringify(validateConfig.errors));
});

test("applying the form keeps comments, the deny list and the other sections", () => {
  const next = applySettingsForm(LAN_CONFIG, { ...tailnetIngress, nodeName: "dev-2", defaultCwd: "/home/me/code" });
  assert.ok(next !== undefined);
  assert.match(next, /^# 由 xmesh 首次运行向导生成。/);
  assert.match(next, /roles: \[agent, gateway, ingress\]/, "roles stay on one line");
  const parsed = parse(next);
  assert.equal(parsed.node.name, "dev-2");
  assert.equal(parsed.agent.default_cwd, "/home/me/code");
  assert.equal(parsed.mesh.mode, "tailnet");
  assert.equal(parsed.mesh.control_url, DEFAULT_CONTROL_URL);
  assert.equal(parsed.mesh.authkey_file, "/home/me/authkey");
  assert.equal(parsed.mesh.lan.mdns, true, "the lan block is not the form's to touch");
  assert.deepEqual(parsed.ingress, { listen: "tunnel", oauth: { issuer: "https://xmesh.example.com" } });
  assert.deepEqual(parsed.agent.cwd_policy.deny, ["~/.ssh"]);
  assert.equal(parsed.agent.cwd_policy.deny_file, "/home/me/.local/state/xmesh/dev-1/cwd-deny.yaml");
  assert.deepEqual(parsed.agent.authz, parse(LAN_CONFIG).agent.authz);
});

test("only public:443 keeps a domain and a certificate; the other listen modes drop them", () => {
  const public443 = applySettingsForm(LAN_CONFIG, {
    ...tailnetIngress,
    listen: "public:443",
    domain: "xmesh.example.com",
    tls: "file",
    certFile: "/etc/xmesh/cert.pem",
    keyFile: "/etc/xmesh/key.pem",
  });
  assert.ok(public443 !== undefined);
  assert.deepEqual(parse(public443).ingress, {
    listen: "public:443",
    domain: "xmesh.example.com",
    tls: "file",
    cert_file: "/etc/xmesh/cert.pem",
    key_file: "/etc/xmesh/key.pem",
    oauth: { issuer: "https://xmesh.example.com" },
  });

  const autocert = applySettingsForm(public443, { ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "autocert" });
  assert.ok(autocert !== undefined);
  assert.deepEqual(parse(autocert).ingress, { listen: "public:443", domain: "xmesh.example.com", tls: "autocert", oauth: { issuer: "https://xmesh.example.com" } });

  const funnel = applySettingsForm(autocert, { ...tailnetIngress, listen: "funnel", issuer: "https://node.tail1234.ts.net" });
  assert.ok(funnel !== undefined);
  assert.deepEqual(parse(funnel).ingress, { listen: "funnel", oauth: { issuer: "https://node.tail1234.ts.net" } });
});

test("a gateway added by the form gets the loopback listen address; an existing one is kept", () => {
  const withoutGateway = LAN_CONFIG.replace('gateway:\n  listen: "127.0.0.1:8080"\n', "");
  const added = applySettingsForm(withoutGateway, lan);
  assert.ok(added !== undefined);
  assert.equal(parse(added).gateway.listen, "127.0.0.1:8080");
  const kept = applySettingsForm(LAN_CONFIG.replace('listen: "127.0.0.1:8080"', 'listen: "127.0.0.1:9090"'), lan);
  assert.ok(kept !== undefined);
  assert.equal(parse(kept).gateway.listen, "127.0.0.1:9090");
});

test("a role removed from the form leaves its section in the file", () => {
  const next = applySettingsForm(applySettingsForm(LAN_CONFIG, tailnetIngress) ?? "", { ...tailnetIngress, roles: ["agent"] });
  assert.ok(next !== undefined);
  assert.deepEqual(parse(next).node.roles, ["agent"]);
  assert.equal(parse(next).ingress.listen, "tunnel", "the ingress section stays, only its role is off");
});

test("the form round-trips through the file for every listen mode", () => {
  const forms: SettingsForm[] = [
    tailnetIngress,
    { ...tailnetIngress, listen: "funnel", issuer: "https://node.tail1234.ts.net" },
    { ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "autocert" },
    { ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "file", certFile: "/c.pem", keyFile: "/k.pem" },
    { ...tailnetIngress, allowDirs: [] },
  ];
  for (const f of forms) {
    const next = applySettingsForm(LAN_CONFIG, f);
    assert.ok(next !== undefined);
    assert.deepEqual(formFromConfig(parse(next)), f, `${f.listen} ${f.tls} ${f.allowDirs.length}`);
  }
});

test("the text the form writes is valid under the config schema", () => {
  const forms: SettingsForm[] = [
    lan,
    { ...lan, allowDirs: [] },
    tailnetIngress,
    { ...tailnetIngress, listen: "funnel", issuer: "https://node.tail1234.ts.net" },
    { ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "file", certFile: "/c.pem", keyFile: "/k.pem", issuer: "https://xmesh.example.com" },
  ];
  for (const f of forms) {
    const next = applySettingsForm(LAN_CONFIG, f);
    assert.ok(next !== undefined);
    assert.ok(validateConfig(parse(next)), `${f.listen}: ${JSON.stringify(validateConfig.errors)}`);
  }
});

test("text that is not YAML cannot be edited", () => {
  assert.equal(applySettingsForm("node: [", lan), undefined);
});

test("moving the deny list into a file names that file and drops the inline list, and keeps the rest", () => {
  const before = '# 保留这一行\nagent:\n  cwd_policy:\n    allow: ["/w"]\n    deny: ["~/.ssh"]\n  authz: {}\n';
  const after = applyDenyFile(before, "/cfg/n1/cwd-deny.yaml");
  assert.ok(after !== undefined);
  assert.match(after, /^# 保留这一行$/m);
  const cfg = parse(after) as { agent: { cwd_policy: Record<string, unknown> } };
  assert.equal(cfg.agent.cwd_policy.deny_file, "/cfg/n1/cwd-deny.yaml");
  assert.equal("deny" in cfg.agent.cwd_policy, false, "the file holds the entries now");
  assert.deepEqual(cfg.agent.cwd_policy.allow, ["/w"]);
  assert.equal(applyDenyFile("agent: [\n", "/cfg/n1/cwd-deny.yaml"), undefined);
});

test("the deny list file is a YAML list of strings and round-trips", () => {
  const text = denyListText(DEFAULT_DENY);
  assert.match(text, /^# 禁止访问的目录/);
  assert.deepEqual(parseDenyList(text), DEFAULT_DENY);
  assert.deepEqual(parseDenyList(denyListText([])), []);
  assert.deepEqual(parseDenyList(""), []);
  assert.equal(parseDenyList("key: value\n"), undefined, "a mapping is not a list");
  assert.equal(parseDenyList("- 1\n- x\n"), undefined, "a number is not a path");
  assert.equal(parseDenyList("- [\n"), undefined, "unparseable text is refused");
});

test("deny entries that are blank or carry control characters are refused before saving", () => {
  assert.equal(checkDenyList(["~/.ssh", ""]), "blank");
  assert.equal(checkDenyList(["~/.ssh", "  "]), "blank");
  assert.equal(checkDenyList(["bad\nline"]), "control");
  assert.equal(checkDenyList(["x".repeat(1025)]), "control");
  assert.equal(checkDenyList(DEFAULT_DENY), undefined);
});


test("settings switch between browser identity and key files with a valid config", () => {
  const keyConfig = applySettingsForm(LAN_CONFIG, tailnetIngress)!;
  const browserForm: SettingsForm = { ...tailnetIngress, browserLogin: true, authkeyFile: "" };
  const browserConfig = applySettingsForm(keyConfig, browserForm)!;
  const parsed = parse(browserConfig);
  assert.equal(parsed.mesh.auth_method, "browser");
  assert.equal(parsed.mesh.authkey_file, undefined);
  assert.ok(validateConfig(parsed), JSON.stringify(validateConfig.errors));
  assert.deepEqual(formFromConfig(parsed), browserForm);

  const edited = applySettingsForm(browserConfig, { ...browserForm, defaultCwd: "/code" })!;
  assert.equal(parse(edited).mesh.auth_method, "browser");
  assert.ok(validateConfig(parse(edited)), JSON.stringify(validateConfig.errors));

  const restored = applySettingsForm(edited, tailnetIngress)!;
  assert.equal(parse(restored).mesh.auth_method, undefined);
  assert.equal(parse(restored).mesh.authkey_file, tailnetIngress.authkeyFile);
  assert.ok(validateConfig(parse(restored)), JSON.stringify(validateConfig.errors));
  assert.deepEqual(formFromConfig(parse(restored)), tailnetIngress);
});


test("file transfer limits default to 1GiB and persist custom sizes or unlimited", () => {
  for (const maxFileSize of ["512MiB", "2GiB", "unlimited", "9223372036854775807B"]) {
    const f = { ...lan, maxFileSize };
    assert.equal(checkSettingsForm(f), undefined);
    const next = applySettingsForm(LAN_CONFIG, f)!;
    assert.equal(formFromConfig(parse(next)).maxFileSize, maxFileSize);
    assert.ok(validateConfig(parse(next)), JSON.stringify(validateConfig.errors));
  }
  for (const maxFileSize of ["0", "-1", "2GB", "1.5GiB", "9223372036854775808B", "99TiB garbage"]) {
    assert.equal(checkSettingsForm({ ...lan, maxFileSize }), "max_file_size");
  }
});
