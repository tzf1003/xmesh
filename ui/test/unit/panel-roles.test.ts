import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { test } from "node:test";
import Ajv2020 from "ajv/dist/2020";
import { parse } from "yaml";
import { applyRolesForm, checkRolesForm, formFromConfig, isChanged, toggleRole, type RolesForm } from "../../src/renderer/lib/roles";
import { DEFAULT_CONTROL_URL } from "../../src/shared/setup";
import { schemaDir } from "../../testserver/fixtures";

// The wizard's lan config, with a comment the edit must keep.
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
  "  authz:",
  '    "tag:peer":',
  '      tools: ["*"]',
  '      backends: ["*"]',
  "",
].join("\n");

const lan: RolesForm = {
  mode: "lan",
  controlUrl: DEFAULT_CONTROL_URL,
  authkeyFile: "",
  roles: ["agent", "gateway"],
  listen: "tunnel",
  issuer: "",
  domain: "",
  tls: "autocert",
  certFile: "",
  keyFile: "",
};
const tailnetIngress: RolesForm = {
  ...lan,
  mode: "tailnet",
  authkeyFile: "/home/me/authkey",
  roles: ["agent", "gateway", "ingress"],
  issuer: "https://xmesh.example.com",
};

const configSchema = JSON.parse(fs.readFileSync(path.join(schemaDir(), "config.schema.json"), "utf8")) as object;
const validateConfig = new Ajv2020({ strict: false, allErrors: true }).compile(configSchema);

test("the form reads the roles, mode and ingress of a parsed config, with the page's defaults", () => {
  const parsed = parse(LAN_CONFIG);
  assert.deepEqual(formFromConfig(parsed), { ...lan, roles: ["agent", "gateway"] });
  assert.deepEqual(formFromConfig(null), { ...lan, roles: [] });
  const withIngress = formFromConfig({ ...parsed, mesh: { mode: "tailnet", control_url: "https://c.example", authkey_file: "/k" }, node: { roles: ["ingress", "agent"] }, ingress: { listen: "public:443", domain: "x.example.com", tls: "file", cert_file: "/c", key_file: "/k", oauth: { issuer: "https://x.example.com" } } });
  assert.deepEqual(withIngress, {
    mode: "tailnet",
    controlUrl: "https://c.example",
    authkeyFile: "/k",
    roles: ["agent", "ingress"],
    listen: "public:443",
    issuer: "https://x.example.com",
    domain: "x.example.com",
    tls: "file",
    certFile: "/c",
    keyFile: "/k",
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
});

test("the check names the first thing the kernel would refuse, without asking it", () => {
  assert.equal(checkRolesForm({ ...lan, roles: [] }), "roles");
  assert.equal(checkRolesForm({ ...tailnetIngress, mode: "lan" }), "tailnet");
  assert.equal(checkRolesForm({ ...lan, controlUrl: "http://c.example" }), undefined, "the control URL is only checked in tailnet mode");
  assert.equal(checkRolesForm({ ...tailnetIngress, controlUrl: "http://c.example" }), "control_url");
  assert.equal(checkRolesForm({ ...tailnetIngress, authkeyFile: "authkey" }), "authkey_file");
  assert.equal(checkRolesForm({ ...tailnetIngress, issuer: "" }), "issuer");
  assert.equal(checkRolesForm({ ...tailnetIngress, issuer: "http://xmesh.example.com" }), "issuer");
  assert.equal(checkRolesForm({ ...tailnetIngress, issuer: "https://xmesh.example.com/oauth" }), "issuer");
  assert.equal(checkRolesForm({ ...tailnetIngress, listen: "public:443", domain: " " }), "domain");
  assert.equal(checkRolesForm({ ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "file", certFile: "cert.pem", keyFile: "/k" }), "cert_files");
  assert.equal(checkRolesForm({ ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "file", certFile: "/c", keyFile: "/k" }), undefined);
  assert.equal(checkRolesForm(tailnetIngress), undefined);
  assert.equal(checkRolesForm(lan), undefined);
});

test("applying the form changes only the keys it owns and keeps comments and other sections", () => {
  const next = applyRolesForm(LAN_CONFIG, tailnetIngress);
  assert.ok(next !== undefined);
  assert.match(next, /^# 由 xmesh 首次运行向导生成。/);
  assert.match(next, /roles: \[agent, gateway, ingress\]/, "roles stay on one line");
  const parsed = parse(next);
  assert.equal(parsed.mesh.mode, "tailnet");
  assert.equal(parsed.mesh.control_url, DEFAULT_CONTROL_URL);
  assert.equal(parsed.mesh.authkey_file, "/home/me/authkey");
  assert.equal(parsed.mesh.lan.mdns, true, "the lan block is not the form's to touch");
  assert.deepEqual(parsed.ingress, { listen: "tunnel", oauth: { issuer: "https://xmesh.example.com" } });
  assert.deepEqual(parsed.agent, parse(LAN_CONFIG).agent);
});

test("only public:443 keeps a domain and a certificate; the other listen modes drop them", () => {
  const public443 = applyRolesForm(LAN_CONFIG, {
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

  const autocert = applyRolesForm(public443, { ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "autocert" });
  assert.ok(autocert !== undefined);
  assert.deepEqual(parse(autocert).ingress, { listen: "public:443", domain: "xmesh.example.com", tls: "autocert", oauth: { issuer: "https://xmesh.example.com" } });

  const funnel = applyRolesForm(autocert, { ...tailnetIngress, listen: "funnel", issuer: "https://node.tail1234.ts.net" });
  assert.ok(funnel !== undefined);
  assert.deepEqual(parse(funnel).ingress, { listen: "funnel", oauth: { issuer: "https://node.tail1234.ts.net" } });
});

test("a gateway added by the form gets the loopback listen address; an existing one is kept", () => {
  const withoutGateway = LAN_CONFIG.replace('gateway:\n  listen: "127.0.0.1:8080"\n', "");
  const added = applyRolesForm(withoutGateway, lan);
  assert.ok(added !== undefined);
  assert.equal(parse(added).gateway.listen, "127.0.0.1:8080");
  const kept = applyRolesForm(LAN_CONFIG.replace('listen: "127.0.0.1:8080"', 'listen: "127.0.0.1:9090"'), lan);
  assert.ok(kept !== undefined);
  assert.equal(parse(kept).gateway.listen, "127.0.0.1:9090");
});

test("a role removed from the form leaves its section in the file", () => {
  const next = applyRolesForm(applyRolesForm(LAN_CONFIG, tailnetIngress) ?? "", { ...tailnetIngress, roles: ["agent"] });
  assert.ok(next !== undefined);
  assert.deepEqual(parse(next).node.roles, ["agent"]);
  assert.equal(parse(next).ingress.listen, "tunnel", "the ingress section stays, only its role is off");
});

test("the form round-trips through the file for every listen mode", () => {
  const forms: RolesForm[] = [
    tailnetIngress,
    { ...tailnetIngress, listen: "funnel", issuer: "https://node.tail1234.ts.net" },
    { ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "autocert" },
    { ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "file", certFile: "/c.pem", keyFile: "/k.pem" },
  ];
  for (const f of forms) {
    const next = applyRolesForm(LAN_CONFIG, f);
    assert.ok(next !== undefined);
    assert.deepEqual(formFromConfig(parse(next)), f, `${f.listen} ${f.tls}`);
  }
});

test("the text the form writes is valid under the config schema", () => {
  const forms: RolesForm[] = [
    lan,
    tailnetIngress,
    { ...tailnetIngress, listen: "funnel", issuer: "https://node.tail1234.ts.net" },
    { ...tailnetIngress, listen: "public:443", domain: "xmesh.example.com", tls: "file", certFile: "/c.pem", keyFile: "/k.pem", issuer: "https://xmesh.example.com" },
  ];
  for (const f of forms) {
    const next = applyRolesForm(LAN_CONFIG, f);
    assert.ok(next !== undefined);
    assert.ok(validateConfig(parse(next)), `${f.listen}: ${JSON.stringify(validateConfig.errors)}`);
  }
});

test("text that is not YAML cannot be edited", () => {
  assert.equal(applyRolesForm("node: [", lan), undefined);
});
