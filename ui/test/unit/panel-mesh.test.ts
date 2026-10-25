import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { test } from "node:test";
import { fakeMeshRunner, freshFakeMesh } from "../../src/main/mesh-fake";
import { MeshService, buildMeshArgs, meshError } from "../../src/main/mesh-service";
import { clipboardText } from "../../src/main/connect";
import { COPY_KINDS, PICK_KINDS } from "../../src/shared/ipc";
import {
  MESH_OPS,
  meshLoginCommands,
  normalizeResult,
  normalizeStatus,
  parseAdmissionUrl,
  parseDerpRegion,
  parseMeshRequest,
  realTime,
  type MeshRequest,
} from "../../src/shared/mesh";
import { regionsFromForms, EMPTY_REGION, parseTagText, splitList } from "../../src/renderer/lib/mesh";
import { meshIssueText } from "../../src/renderer/lib/mesh-issues";
import { deviceNeedsAttention, filterMeshDevices } from "../../src/renderer/pages/module-model";

const REGION = { code: "lab", name: "Lab", host: "derp.example.com" };

function ok(v: unknown): MeshRequest {
  const r = parseMeshRequest(v);
  assert.ok(r.ok, `expected ${JSON.stringify(v)} to be accepted: ${r.ok ? "" : r.field}`);
  return r.value;
}
function field(v: unknown): string {
  const r = parseMeshRequest(v);
  assert.ok(!r.ok, `expected ${JSON.stringify(v)} to be refused`);
  return r.field;
}

test("every op is accepted in its documented shape", () => {
  const good: unknown[] = [
    { op: "login", kind: "tailscale", access: "read", secret: "tskey-api-example" },
    { op: "login", kind: "headscale", access: "write", url: "https://headscale.example.com", secret: "hskey-example" },
    { op: "status" },
    { op: "share.tailnet", device: "laptop" },
    { op: "share.tailnet", device: `dev-${"ab".repeat(32)}`, user: "alice" },
    { op: "cloudflare.share", device: "laptop" },
    { op: "cloudflare.status" },
    { op: "cloudflare.discover", secret: "cf-token-123" },
    { op: "cloudflare.setup", name: "laptop", apply: false },
    { op: "cloudflare.remove", name: "laptop" },
    { op: "cloudflare.remove", name: "My Laptop" },
    { op: "cloudflare.login", account: "a".repeat(32), zone: "b".repeat(32), domain: "mesh.example.com", secret: "cf-token-123" },
    { op: "tags", device: "n100", tags: ["tag:device"], apply: false },
    { op: "tags", device: "n100", tags: [], apply: true },
    { op: "onboard", tags: ["tag:device"], ttl: "1h", ephemeral: false, file: "/home/u/key.txt", overwrite: true },
    { op: "onboard", tags: ["tag:device"], ttl: "24h", ephemeral: true, user: "alice", file: "C:\\Users\\u\\key.txt", overwrite: false },
    { op: "policy.show" },
    { op: "policy.document", edit: { text: "{}" } },
    { op: "policy.publish", text: "{}", expectVersion: "v1", apply: false },
    { op: "policy.snapshots" },
    { op: "policy.template", template: "team", owner: "autogroup:admin", apply: false },
    { op: "policy.template", template: "team", owner: "tag:admins", apply: false },
    { op: "policy.template", template: "personal", owner: "alice@", port: 7433, apply: true, expectVersion: "v3" },
    { op: "policy.rollback", snapshot: "20261016T083000Z-1", apply: true, expectVersion: "sha256:abc" },
    { op: "matrix", mismatchOnly: true, port: 7433 },
    { op: "derp.plan", regions: [REGION], omitDefault: false, apply: false, skipStun: true },
    { op: "derp.probe", regions: [{ ...REGION, ipv4: "203.0.113.7", derpPort: 443, stunPort: 3478, id: 901 }], skipStun: false, compareOfficial: true },
    { op: "derp.headscale", regions: [REGION], omitDefault: true },
    { op: "derp.sync", target: { allowlist: "/etc/derp/allow.json" }, allDevices: false },
    { op: "derp.sync", target: { url: "https://derp.example.com:8480", tokenFile: "/etc/derp/token" }, allDevices: true },
  ];
  for (const g of good) ok(g);
  const ops = new Set(good.map((g) => (g as { op: string }).op));
  assert.deepEqual([...ops].sort(), [...MESH_OPS].sort());
});

test("requests are refused for stray keys, bad types and injection attempts", () => {
  assert.equal(field(null), "op");
  assert.equal(field({ op: "shell" }), "op");
  assert.equal(field({ op: "status", extra: 1 }), "keys");
  assert.equal(field({ op: "share.tailnet", device: "x;rm", }), "device");
  assert.equal(field({ op: "share.tailnet", device: "x", key: "tskey" }), "keys");
  assert.equal(field({ op: "cloudflare.share", device: "--apply" }), "device");
  assert.equal(field({ op: "cloudflare.setup", name: "--apply", apply: true }), "name");
  assert.equal(field({ op: "cloudflare.setup", name: "laptop" }), "apply");
  assert.equal(field({ op: "cloudflare.remove", name: "a\nb" }), "name");
  assert.equal(field({ op: "cloudflare.login", account: "short", zone: "b".repeat(32), domain: "mesh.example.com", secret: "t" }), "account");
  assert.equal(field({ op: "cloudflare.login", account: "a".repeat(32), zone: "b".repeat(32), domain: "not a domain", secret: "t" }), "domain");
  assert.equal(field({ op: "tags", device: "n1", tags: ["tag:a"], apply: false, extra: 1 }), "keys");
  assert.equal(field({ op: "tags", device: "--apply", tags: [], apply: false }), "device");
  assert.equal(field({ op: "tags", device: "n1 --x", tags: [], apply: false }), "device");
  assert.equal(field({ op: "tags", device: "n1", tags: ["tag:a,tag:b"], apply: false }), "tags");
  assert.equal(field({ op: "tags", device: "n1", tags: ["admin"], apply: false }), "tags");
  assert.equal(field({ op: "tags", device: "n1", tags: ["tag:a", "tag:a"], apply: false }), "tags");
  assert.equal(field({ op: "tags", device: "n1", tags: ["tag:a"], apply: "yes" }), "apply");
  ok({ op: "onboard", tags: [], ttl: "1h", ephemeral: false, file: "/k", overwrite: false });
  assert.equal(field({ op: "onboard", tags: ["tag:a"], ttl: "1d", ephemeral: false, file: "/k", overwrite: false }), "ttl");
  assert.equal(field({ op: "onboard", tags: ["tag:a"], ttl: "0h", ephemeral: false, file: "/k", overwrite: false }), "ttl");
  assert.equal(field({ op: "onboard", tags: ["tag:a"], ttl: "1h", ephemeral: false, file: "relative/key", overwrite: false }), "file");
  assert.equal(field({ op: "onboard", tags: ["tag:a"], ttl: "1h", ephemeral: false, file: "-", overwrite: false }), "file");
  assert.equal(field({ op: "onboard", tags: ["tag:a"], ttl: "1h", ephemeral: false, user: "a b", file: "/k", overwrite: false }), "user");
  assert.equal(field({ op: "policy.template", template: "admin", owner: "a@", apply: false }), "template");
  assert.equal(field({ op: "policy.template", template: "team", owner: "a b", apply: false }), "owner");
  assert.equal(field({ op: "policy.template", template: "team", owner: "tag:", apply: false }), "owner");
  assert.equal(field({ op: "policy.template", template: "team", owner: "autogroup:admin", port: 70000, apply: false }), "port");
  assert.equal(field({ op: "policy.template", template: "team", owner: "autogroup:admin", port: 80.5, apply: false }), "port");
  assert.equal(field({ op: "policy.template", template: "team", owner: "autogroup:admin", apply: false, expectVersion: "a b" }), "expectVersion");
  assert.equal(field({ op: "policy.rollback", snapshot: "../../etc/passwd", apply: false }), "snapshot");
  assert.equal(field({ op: "policy.rollback", snapshot: "--yes", apply: false }), "snapshot");
  assert.equal(field({ op: "matrix", mismatchOnly: "no" }), "mismatchOnly");
  assert.equal(field({ op: "derp.plan", regions: [], omitDefault: false, apply: false, skipStun: false }), "regions");
  assert.equal(field({ op: "derp.plan", regions: Array(9).fill(REGION).map((r, i) => ({ ...r, code: `r${i}` })), omitDefault: false, apply: false, skipStun: false }), "regions");
  assert.equal(field({ op: "derp.plan", regions: [REGION, REGION], omitDefault: false, apply: false, skipStun: false }), "regions");
  assert.equal(field({ op: "derp.sync", target: { allowlist: "rel" }, allDevices: false }), "allowlist");
  assert.equal(field({ op: "derp.sync", target: { url: "ftp://x", tokenFile: "/t" }, allDevices: false }), "url");
  assert.equal(field({ op: "derp.sync", target: { url: "https://u:p@x.example", tokenFile: "/t" }, allDevices: false }), "url");
  assert.equal(field({ op: "derp.sync", target: { url: "https://x.example", tokenFile: "t" }, allDevices: false }), "tokenFile");
  assert.equal(field({ op: "derp.sync", target: { allowlist: "/a", url: "https://x.example" }, allDevices: false }), "target");
});

test("DERP regions cannot smuggle the CLI's own separators", () => {
  assert.ok(parseDerpRegion(REGION));
  for (const bad of [
    { ...REGION, name: "Lab,host=evil.example" },
    { ...REGION, name: "a=b" },
    { ...REGION, name: "line\nbreak" },
    { ...REGION, code: "Lab" },
    { ...REGION, code: "lab,id=1" },
    { ...REGION, host: "x.example,ipv4=1.1.1.1" },
    { ...REGION, host: "-x.example" },
    { ...REGION, ipv4: "999.1.1.1" },
    { ...REGION, ipv6: "no-colons" },
    { ...REGION, id: 12 },
    { ...REGION, id: 1000 },
    { ...REGION, derpPort: 0 },
    { ...REGION, stunPort: 70000 },
    { ...REGION, certName: "a b" },
    { ...REGION, extra: 1 },
  ]) {
    assert.equal(parseDerpRegion(bad), undefined, JSON.stringify(bad));
  }
  assert.equal(parseAdmissionUrl("https://derp.example.com:8480/"), "https://derp.example.com:8480/");
  assert.equal(parseAdmissionUrl("https://x.example/?token=1"), undefined);
});

test("argv: fixed subcommands, --flag=value for every value, secrets only as file paths", () => {
  const cases: [MeshRequest, string[]][] = [
    [{ op: "status" }, ["mesh", "status", "--json"]],
    [{ op: "tags", device: "n100", tags: ["tag:a", "tag:b"], apply: false }, ["mesh", "tags", "--json", "--device=n100", "--set=tag:a,tag:b"]],
    [{ op: "tags", device: "n100", tags: [], apply: true }, ["mesh", "tags", "--json", "--device=n100", "--set=", "--apply"]],
    [
      { op: "onboard", tags: ["tag:device"], ttl: "1h", ephemeral: true, user: "alice", file: "/k/key", overwrite: true },
      ["mesh", "authkey", "--json", "--tags=tag:device", "--ttl=1h", "--out=/k/key", "--ephemeral", "--user=alice", "--overwrite"],
    ],
    [{ op: "policy.show" }, ["mesh", "policy", "--json", "--show"]],
    [{ op: "policy.snapshots" }, ["mesh", "policy", "--json", "--snapshots"]],
    [
      { op: "policy.template", template: "team", owner: "alice@", port: 8000, apply: true, expectVersion: "v2" },
      ["mesh", "policy", "--json", "--template=team", "--owner=alice@", "--port=8000", "--expect-version=v2", "--apply", "--yes"],
    ],
    [{ op: "policy.rollback", snapshot: "s1", apply: false }, ["mesh", "policy", "--json", "--rollback=s1"]],
    [{ op: "matrix", mismatchOnly: true, port: 7433 }, ["mesh", "matrix", "--json", "--port=7433", "--mismatch-only"]],
    [
      { op: "derp.plan", regions: [{ ...REGION, ipv4: "203.0.113.7", id: 901, derpPort: 8443 }], omitDefault: true, apply: true, expectVersion: "v9", skipStun: true },
      ["mesh", "derp", "apply", "--json", "--region=code=lab,name=Lab,host=derp.example.com,ipv4=203.0.113.7,id=901,derp-port=8443", "--omit-default", "--skip-stun", "--expect-version=v9", "--yes"],
    ],
    [
      { op: "derp.probe", regions: [REGION], skipStun: false, compareOfficial: true },
      ["mesh", "derp", "probe", "--json", "--region=code=lab,name=Lab,host=derp.example.com", "--compare-official"],
    ],
    [
      { op: "derp.sync", target: { url: "https://d.example:8480/", tokenFile: "/t/token" }, allDevices: true },
      ["mesh", "derp", "sync", "--json", "--admission-url=https://d.example:8480/", "--admission-token-file=/t/token", "--all-devices"],
    ],
  ];
  for (const [req, argv] of cases) assert.deepEqual(buildMeshArgs(req), argv);
  // The node's own config is passed only to the commands that read roles and tool permissions.
  assert.deepEqual(buildMeshArgs({ op: "status" }, "/n/xmesh.yaml"), ["mesh", "status", "--json", "--node-config=/n/xmesh.yaml"]);
  assert.deepEqual(buildMeshArgs({ op: "matrix", mismatchOnly: false }, "/n/xmesh.yaml"), ["mesh", "matrix", "--json", "--node-config=/n/xmesh.yaml"]);
  assert.deepEqual(buildMeshArgs({ op: "policy.show" }, "/n/xmesh.yaml"), ["mesh", "policy", "--json", "--show"]);
  assert.deepEqual(buildMeshArgs({ op: "share.tailnet", device: "laptop", user: "alice" }, "/n/xmesh.yaml", "/c.json"), ["mesh", "share", "--json", "--credential-file=/c.json", "--device=laptop", "--user=alice", "--config=/n/xmesh.yaml", "--apply"]);
  assert.deepEqual(buildMeshArgs({ op: "cloudflare.share", device: "laptop" }, "/n/xmesh.yaml", "/c.json"), ["cloudflare", "share", "--json", "--device=laptop", "--config=/n/xmesh.yaml", "--apply"]);
  assert.deepEqual(buildMeshArgs({ op: "cloudflare.setup", name: "laptop", apply: false }, "/n/xmesh.yaml"), ["cloudflare", "setup", "--json", "--name=laptop", "--config=/n/xmesh.yaml"]);
  assert.deepEqual(buildMeshArgs({ op: "cloudflare.setup", name: "laptop", apply: true }, "/n/xmesh.yaml"), ["cloudflare", "setup", "--json", "--name=laptop", "--config=/n/xmesh.yaml", "--apply"]);
  assert.deepEqual(buildMeshArgs({ op: "cloudflare.remove", name: "laptop" }), ["cloudflare", "remove", "--json", "--name=laptop", "--apply"]);
  assert.deepEqual(normalizeResult("cloudflare.setup", { device: "laptop", domain: "mesh.example.com", creates: ["a"] }), { applied: false, creates: ["a"], domain: "mesh.example.com" });
  assert.deepEqual(normalizeResult("cloudflare.setup", { hostname: "h.mesh.example.com", tunnel_id: "t", config: "c", cloudflared_note: "已自动安装 cloudflared" }), { applied: true, creates: [], hostname: "h.mesh.example.com", tunnelId: "t", cloudflaredNote: "已自动安装 cloudflared" });
  assert.deepEqual(normalizeResult("cloudflare.status", { devices: { b: { hostname: "hb", tunnel_id: "tb" }, a: { hostname: "ha", tunnel_id: "ta" } }, service_token: true }), { devices: [{ name: "a", hostname: "ha", tunnelId: "ta" }, { name: "b", hostname: "hb", tunnelId: "tb" }], serviceToken: true, configuredHostname: "" });
  const cfDiscover = buildMeshArgs({ op: "cloudflare.discover", secret: "cf-token-123" });
  assert.deepEqual(cfDiscover, ["cloudflare", "discover", "--json", "--secret-file=-"]);
  assert.deepEqual(normalizeResult("cloudflare.discover", { accounts: [{ id: "a1", name: "Me" }], zones: [{ id: "z1", name: "example.com", account_id: "a1" }, { id: "", name: "x" }] }), { accounts: [{ id: "a1", name: "Me" }], zones: [{ id: "z1", name: "example.com", accountId: "a1" }] });
  const cfLogin = buildMeshArgs({ op: "cloudflare.login", account: "a".repeat(32), zone: "b".repeat(32), domain: "mesh.example.com", secret: "cf-token-123" });
  assert.equal(cfLogin.some((a) => a.includes("cf-token-123")), false, "the token goes through stdin, never argv");
  assert.ok(cfLogin.includes("--secret-file=-"));
  // No value ever starts a flag of its own: all are glued to their flag name.
  for (const [req] of cases) {
    for (const a of buildMeshArgs(req)) assert.ok(a.startsWith("--") || ["mesh", "status", "tags", "authkey", "policy", "matrix", "derp", "apply", "plan", "probe", "sync", "headscale", "share", "cloudflare", "login", "setup", "remove", "discover"].includes(a), a);
  }
});

test("the CLI's Go quirks become clean values", () => {
  assert.equal(realTime("0001-01-01T00:00:00Z"), undefined);
  assert.equal(realTime("garbage"), undefined);
  assert.equal(realTime("2026-10-16T08:00:00Z"), "2026-10-16T08:00:00Z");
  const s = normalizeStatus({
    provider: "tailscale",
    capabilities: { policy_write: true },
    devices: [{ device: { id: "n1", name: "a", tags: null, addresses: null, online: true, authorized: true, last_seen: "0001-01-01T00:00:00Z" }, roles: null, issues: null, tags: null }],
  });
  assert.deepEqual(s.devices[0]!.device.tags, []);
  assert.deepEqual(s.devices[0]!.roles, []);
  assert.equal(s.devices[0]!.device.lastSeen, undefined);
  assert.equal(s.capabilities.policyWrite, true);
  assert.equal(s.capabilities.derpViaPolicy, false);
  // Nonsense in, empty values out; never an exception.
  for (const op of MESH_OPS) {
    if (op === "policy.document") assert.throws(() => normalizeResult(op, "not an object"));
    else assert.doesNotThrow(() => normalizeResult(op, "not an object"));
  }
  assert.deepEqual(normalizeResult("matrix", undefined), { port: 0, cells: [] });
});

test("Go warning diagnostics remain actionable after status normalization", () => {
  const devices = ["warning", "warn", "error", "info", "future"].map((severity) => ({
    device: { id: severity, authorized: true },
    issues: [{ severity, code: "no-tags", message: "Review tags" }],
  }));
  const status = normalizeStatus({ devices });
  assert.deepEqual(status.devices.map((report) => report.issues[0]!.severity), ["warn", "warn", "error", "info", "info"]);
  assert.equal(deviceNeedsAttention(status.devices[0]!), true);
  assert.deepEqual(filterMeshDevices(status.devices, "", "attention").map((report) => report.device.id), ["warning", "warn", "error"]);
});

test("mesh diagnostics localize known messages and preserve unknown details", () => {
  const issue = { severity: "error" as const, code: "missing-role-tag", message: 'xmesh.yaml declares role "gateway" but the node lacks tag:peer or tag:cloud (it will not appear in discovery or will be denied)' };
  assert.match(meshIssueText(issue, "zh"), /gateway.*tag:peer 或 tag:cloud/);
  assert.equal(meshIssueText(issue, "en"), issue.message);
  for (const code of ["no-tags", "unauthorized", "unexpected-device-tag"]) assert.notEqual(meshIssueText({ ...issue, code }, "zh"), issue.message);
  const date = "2026-10-21T08:00:00Z";
  assert.equal(meshIssueText({ ...issue, code: "key-expired", message: `node key expired ${date}` }, "zh"), `设备密钥已过期（${date}），请重新认证。`);
  assert.equal(meshIssueText({ ...issue, code: "key-expiring", message: `node key expires ${date}` }, "zh"), `设备密钥即将过期（${date}），请及时重新认证。`);
  assert.equal(meshIssueText({ ...issue, code: "future" }, "zh"), issue.message);
  assert.equal(meshIssueText({ ...issue, message: "Custom role details" }, "zh"), "Custom role details");
});

test("login commands: fixed text, copied by key", () => {
  assert.ok((COPY_KINDS as readonly string[]).includes("meshLoginRead"));
  const cmds = meshLoginCommands();
  assert.equal(clipboardText("meshLoginRead", undefined), cmds.read);
  assert.equal(clipboardText("meshLoginWrite", undefined), cmds.write);
  assert.equal(clipboardText("meshLoginReadHeadscale", undefined), cmds.readHeadscale);
  assert.equal(clipboardText("meshLoginWriteHeadscale", undefined), cmds.writeHeadscale);
  // The text is pasted into a terminal: no pipes, redirects or substitutions.
  for (const c of Object.values(cmds)) assert.doesNotMatch(c, /[|&;<>$`\\(){}]/);
  assert.equal(clipboardText("gatewayUrl", undefined), undefined);
  for (const k of ["authkeySave", "allowlistFile", "tokenFile"]) assert.ok((PICK_KINDS as readonly string[]).includes(k));
});

function service(mutate?: (s: ReturnType<typeof freshFakeMesh>) => void): { svc: MeshService; fake: ReturnType<typeof freshFakeMesh> } {
  const fake = freshFakeMesh();
  mutate?.(fake);
  // The panel always names a 0600 credential file; tests use a fixed path.
  return { svc: new MeshService(fakeMeshRunner(fake), () => "/n/xmesh.yaml", () => "/state/mesh/mesh-credential.json"), fake };
}

test("service: invalid requests never reach the CLI", async () => {
  const { svc, fake } = service();
  const r = await svc.request({ op: "tags", device: "--apply", tags: [], apply: true });
  assert.equal(r.ok, false);
  if (!r.ok) assert.equal(r.error.code, "invalid_args");
  assert.deepEqual(fake.calls, []);
});

test("service: status joins devices with roles and the node config", async () => {
  const { svc, fake } = service();
  const r = await svc.request({ op: "status" });
  assert.ok(r.ok);
  if (!r.ok) return;
  const s = r.data as ReturnType<typeof normalizeStatus>;
  assert.equal(s.devices.length, 3);
  assert.deepEqual(s.devices[0]!.roles, ["agent", "gateway"]);
  assert.equal(s.devices[2]!.issues[0]!.severity, "warn");
  assert.deepEqual(fake.calls[0], ["mesh", "status", "--json", "--credential-file=/state/mesh/mesh-credential.json", "--node-config=/n/xmesh.yaml"]);
});

test("service: without a saved credential every call says so", async () => {
  const { svc } = service((f) => (f.credential = false));
  const r = await svc.request({ op: "status" });
  assert.ok(!r.ok);
  if (!r.ok) assert.equal(r.error.code, "no_credential");
  const { svc: ro } = service((f) => (f.writeCredential = false));
  assert.ok((await ro.request({ op: "status" })).ok);
  const w = await ro.request({ op: "tags", device: "n100", tags: ["tag:device"], apply: true });
  assert.ok(!w.ok);
  if (!w.ok) assert.equal(w.error.code, "insufficient_scope");
});

test("service: a tag change previews first and refuses tags the policy does not own", async () => {
  const { svc, fake } = service();
  const prev = await svc.request({ op: "tags", device: "n300", tags: ["tag:device"], apply: false });
  assert.ok(prev.ok);
  if (prev.ok) {
    const p = prev.data as { applied: boolean; missingTagOwners: string[]; to: string[] };
    assert.equal(p.applied, false);
    assert.deepEqual(p.missingTagOwners, []);
  }
  assert.deepEqual(fake.devices[2]!.tags, []);
  const missing = await svc.request({ op: "tags", device: "n300", tags: ["tag:ghost"], apply: false });
  assert.ok(missing.ok);
  if (missing.ok) assert.deepEqual((missing.data as { missingTagOwners: string[] }).missingTagOwners, ["tag:ghost"]);
  const refused = await svc.request({ op: "tags", device: "n300", tags: ["tag:ghost"], apply: true });
  assert.ok(!refused.ok);
  const applied = await svc.request({ op: "tags", device: "n300", tags: ["tag:device"], apply: true });
  assert.ok(applied.ok);
  assert.deepEqual(fake.devices[2]!.tags, ["tag:device"]);
});

test("service: onboarding returns the file path and nothing secret", async () => {
  const { svc, fake } = service();
  const r = await svc.request({ op: "onboard", tags: ["tag:device"], ttl: "1h", ephemeral: false, file: "/home/u/key.txt", overwrite: true });
  assert.ok(r.ok);
  if (r.ok) {
    assert.deepEqual(r.data, { id: "k123", expires: "2026-10-16T10:00:00Z", tags: ["tag:device"], file: "/home/u/key.txt" });
    assert.ok(!JSON.stringify(r.data).includes("secret"));
  }
  assert.deepEqual(fake.keyFiles, ["/home/u/key.txt"]);
});

test("service: policy template write is preview -> apply with the previewed version, then rollback", async () => {
  const { svc, fake } = service();
  const prev = await svc.request({ op: "policy.template", template: "team", owner: "autogroup:admin", apply: false });
  assert.ok(prev.ok);
  if (!prev.ok) return;
  const plan = prev.data as { plan: { changed: boolean; baseVersion: string; writable: boolean }; applied: boolean };
  assert.equal(plan.applied, false);
  assert.ok(plan.plan.changed && plan.plan.writable);
  assert.equal(fake.version, 1);

  const done = await svc.request({ op: "policy.template", template: "team", owner: "autogroup:admin", apply: true, expectVersion: plan.plan.baseVersion });
  assert.ok(done.ok);
  if (done.ok) {
    const d = done.data as { applied: boolean; snapshotId?: string; version?: string };
    assert.equal(d.applied, true);
    assert.equal(d.snapshotId, "snap-1");
    assert.equal(d.version, "v2");
  }

  // The same preview is stale now.
  const stale = await svc.request({ op: "policy.template", template: "team", owner: "autogroup:admin", apply: true, expectVersion: plan.plan.baseVersion });
  assert.ok(!stale.ok);
  if (!stale.ok) assert.equal(stale.error.code, "conflict");

  const snaps = await svc.request({ op: "policy.snapshots" });
  assert.ok(snaps.ok);
  if (snaps.ok) assert.deepEqual((snaps.data as { snapshots: { id: string }[] }).snapshots.map((x) => x.id), ["snap-1"]);
  const back = await svc.request({ op: "policy.rollback", snapshot: "snap-1", apply: true, expectVersion: "v2" });
  assert.ok(back.ok);
  assert.equal(fake.version, 3);
  assert.ok(fake.policyText.includes('"acls": []'));
});

test("service: a Headscale policy is previewed but not writable", async () => {
  const { svc, fake } = service((f) => (f.provider = "headscale"));
  const st = await svc.request({ op: "status" });
  assert.ok(st.ok);
  if (st.ok) assert.equal((st.data as ReturnType<typeof normalizeStatus>).capabilities.policyWrite, false);
  const prev = await svc.request({ op: "policy.template", template: "personal", owner: "alice@", apply: false });
  assert.ok(prev.ok);
  if (prev.ok) {
    const p = prev.data as { plan: { writable: boolean; after: string } };
    assert.equal(p.plan.writable, false);
    assert.ok(p.plan.after.includes("alice@"));
  }
  assert.equal(fake.version, 1);
});

test("service: DERP write probes and rolls back on failure, and the failure still carries the report", async () => {
  const { svc, fake } = service();
  const bad = await svc.request({ op: "derp.plan", regions: [{ code: "x", name: "X", host: "bad.example.com" }], omitDefault: false, apply: true, skipStun: true, expectVersion: "v1" });
  assert.ok(bad.ok);
  if (bad.ok) {
    const d = bad.data as { rolledBack: boolean; probes: { ok: boolean }[]; applied: boolean };
    assert.equal(d.rolledBack, true);
    assert.equal(d.probes[0]!.ok, false);
  }
  assert.equal(fake.version, 1);
  const good = await svc.request({ op: "derp.plan", regions: [{ code: "lab", name: "Lab", host: "derp.example.com" }], omitDefault: false, apply: true, skipStun: true, expectVersion: "v1" });
  assert.ok(good.ok);
  if (good.ok) {
    const d = good.data as { rolledBack: boolean; probes: { ok: boolean }[]; applied: boolean };
    assert.equal(d.rolledBack, false);
    assert.equal(d.applied, true);
    assert.equal(d.probes[0]!.ok, true);
  }
  assert.equal(fake.version, 2);
});

test("service: DERP probe, Headscale files and admission sync", async () => {
  const { svc } = service();
  const p = await svc.request({ op: "derp.probe", regions: [{ code: "x", name: "X", host: "bad.example.com" }], skipStun: true, compareOfficial: true });
  assert.ok(p.ok);
  if (p.ok) {
    const d = p.data as { probes: { ok: boolean }[]; official: { code: string; error?: string }[]; warnings: string[] };
    assert.equal(d.probes[0]!.ok, false);
    assert.equal(d.official.length, 2);
    assert.equal(d.official[1]!.error, "timeout");
    assert.equal(d.warnings.length, 1);
  }
  const hs = await svc.request({ op: "derp.headscale", regions: [REGION], omitDefault: false });
  assert.ok(hs.ok);
  if (hs.ok) assert.ok((hs.data as { file: string }).file.includes("regioncode: lab"));
  const sync = await svc.request({ op: "derp.sync", target: { allowlist: "/a/allow.json" }, allDevices: false });
  assert.ok(sync.ok);
  if (sync.ok) assert.deepEqual(sync.data, { added: 2, removed: 1, total: 3 });
});

test("service: the Headscale DERP plan explains itself instead of writing", async () => {
  const { svc } = service((f) => (f.provider = "headscale"));
  const r = await svc.request({ op: "derp.plan", regions: [REGION], omitDefault: false, apply: false, skipStun: false });
  assert.ok(!r.ok);
  if (!r.ok) assert.equal(r.error.code, "mesh_failed");
});

test("service: the matrix reports both layers and mismatches", async () => {
  const { svc } = service();
  const all = await svc.request({ op: "matrix", mismatchOnly: false });
  assert.ok(all.ok);
  if (all.ok) {
    const m = all.data as { cells: { src: string; mismatch?: string; allowedTools: string[] }[] };
    assert.equal(m.cells.length, 3);
    assert.deepEqual(m.cells[0]!.allowedTools, ["exec.run", "fs.read"]);
  }
  const only = await svc.request({ op: "matrix", mismatchOnly: true });
  assert.ok(only.ok);
  if (only.ok) assert.deepEqual((only.data as { cells: { src: string }[] }).cells.map((c) => c.src), ["stray"]);
});

test("meshError maps the CLI's failures", () => {
  assert.equal(meshError({ code: 1, stdout: "", stderr: "没有保存的凭据（profile \"default\"）" }).code, "no_credential");
  assert.equal(meshError({ code: 1, stdout: "", stderr: "conflict: 预览时的策略版本与现在不一致，请重新预览" }).code, "conflict");
  assert.equal(meshError({ code: 127, stdout: "", stderr: "找不到 xmesh 可执行文件", missing: true }).code, "unavailable");
  assert.equal(meshError({ code: 1, stdout: "", stderr: "meshadmin: read system keychain: exec: \"dbus-launch\": executable file not found in $PATH" }).code, "keychain_unavailable");
  assert.equal(meshError({ code: 124, stdout: "", stderr: "操作超时" }).reason, "timeout");
  const other = meshError({ code: 1, stdout: "", stderr: `boom\n${"x".repeat(2000)}` });
  assert.equal(other.code, "mesh_failed");
  assert.ok(other.message.length <= 600);
});

test("service: non-JSON output and a crashing runner become errors, not exceptions", async () => {
  const garbled = new MeshService(async () => ({ code: 0, stdout: "not json", stderr: "" }));
  const g = await garbled.request({ op: "status" });
  assert.ok(!g.ok);
  const boom = new MeshService(async () => {
    throw new Error("spawn failed");
  });
  const b = await boom.request({ op: "status" });
  assert.ok(!b.ok);
  if (!b.ok) assert.equal(b.error.code, "unavailable");
});

test("login: the secret is checked, Headscale needs https, and the secret never reaches argv", () => {
  const key = "tskey-api-k1234567890ABCDEF";
  assert.deepEqual(buildMeshArgs(ok({ op: "login", kind: "tailscale", access: "read", secret: key })), [
    "mesh",
    "login",
    "--json",
    "--kind=tailscale",
    "--access=read",
    "--secret-file=-",
  ]);
  assert.deepEqual(
    buildMeshArgs(ok({ op: "login", kind: "tailscale", access: "write", secret: key, tailnet: "example.com", clientId: "kCLIENT1CNTRL" })),
    ["mesh", "login", "--json", "--kind=tailscale", "--access=write", "--secret-file=-", "--tailnet=example.com", "--client-id=kCLIENT1CNTRL"],
  );
  assert.deepEqual(buildMeshArgs(ok({ op: "login", kind: "headscale", access: "write", url: "https://headscale.example.com", secret: key })), [
    "mesh",
    "login",
    "--json",
    "--kind=headscale",
    "--access=write",
    "--secret-file=-",
    "--url=https://headscale.example.com/",
  ]);
  for (const req of [
    { op: "login", kind: "tailscale", access: "read", secret: key },
    { op: "login", kind: "headscale", access: "read", url: "https://hs.example.com", secret: key },
  ] as const) {
    assert.ok(!buildMeshArgs(ok(req)).join(" ").includes(key));
  }

  const base = { op: "login", kind: "tailscale", access: "read", secret: key };
  assert.equal(field({ ...base, kind: "github" }), "kind");
  assert.equal(field({ ...base, access: "admin" }), "access");
  assert.equal(field({ ...base, secret: "" }), "secret");
  assert.equal(field({ ...base, secret: "tskey with space" }), "secret");
  assert.equal(field({ ...base, secret: `${key}\n--apply` }), "secret");
  assert.equal(field({ ...base, secret: "k".repeat(4097) }), "secret");
  assert.equal(field({ ...base, url: "https://api.example.com" }), "url");
  assert.equal(field({ ...base, tailnet: "bad name" }), "tailnet");
  assert.equal(field({ ...base, clientId: "id with space" }), "clientId");
  assert.equal(field({ ...base, args: ["--apply"] }), "keys");
  assert.equal(field({ op: "login", kind: "headscale", access: "read", secret: key }), "url");
  assert.equal(field({ op: "login", kind: "headscale", access: "read", url: "http://hs.example.com", secret: key }), "url");
  assert.equal(field({ op: "login", kind: "headscale", access: "read", url: "https://hs.example.com", tailnet: "example.com", secret: key }), "tailnet");
});

test("service: the secret goes to the CLI's stdin only, and a rejected key is not echoed", async () => {
  const key = "tskey-api-SECRETVALUE123";
  const seen: { args: string[]; input?: string }[] = [];
  const recording = new MeshService(async (args, input) => {
    seen.push({ args, input });
    return { code: 0, stdout: JSON.stringify({ profile: "default", access: "read", kind: "tailscale", stored: true }), stderr: "" };
  });
  const saved = await recording.request({ op: "login", kind: "tailscale", access: "read", secret: key });
  assert.deepEqual(saved, { ok: true, data: { kind: "tailscale", access: "read", stored: true } });
  assert.equal(seen[0]!.input, key);
  assert.ok(!seen[0]!.args.join(" ").includes(key));

  const { svc, fake } = service((f) => (f.acceptKeys = false));
  const rejected = await svc.request({ op: "login", kind: "tailscale", access: "write", secret: key });
  assert.ok(!rejected.ok);
  if (!rejected.ok) assert.equal(rejected.error.code, "mesh_failed");
  assert.ok(!JSON.stringify(rejected).includes(key));
  assert.ok(!JSON.stringify(fake.calls).includes(key));
});

test("service: a saved login brings the credential back through the fake CLI", async () => {
  const key = "hskey-example-KEY";
  const { svc, fake } = service((f) => (f.credential = false));
  assert.ok(!(await svc.request({ op: "status" })).ok);
  const r = await svc.request({ op: "login", kind: "headscale", access: "write", url: "https://headscale.example.com", secret: key });
  assert.deepEqual(r, { ok: true, data: { kind: "headscale", access: "write", stored: true } });
  assert.equal(fake.credentialFile, "/state/mesh/mesh-credential.json");
  assert.equal(fake.provider, "headscale");
  assert.ok((await svc.request({ op: "status" })).ok);
  assert.ok(!JSON.stringify(fake.calls).includes(key));
});

test("form helpers", () => {
  assert.deepEqual(splitList(" tag:a, tag:b\ttag:c "), ["tag:a", "tag:b", "tag:c"]);
  assert.deepEqual(parseTagText("", true), { ok: true, tags: [] });
  assert.deepEqual(parseTagText("", false), { ok: false });
  assert.deepEqual(parseTagText("tag:a tag:a", true), { ok: false });
  assert.deepEqual(parseTagText("tag:a, nope", true), { ok: false });

  const good = { ...EMPTY_REGION, code: "lab", name: "Lab", host: "derp.example.com", derpPort: "8443" };
  const r = regionsFromForms([good]);
  assert.ok(r.ok);
  if (r.ok) assert.deepEqual(r.regions, [{ code: "lab", name: "Lab", host: "derp.example.com", derpPort: 8443 }]);
  assert.deepEqual(regionsFromForms([]), { ok: false, problem: "empty" });
  assert.deepEqual(regionsFromForms([good, { ...good, host: "other.example.com" }]), { ok: false, problem: "duplicate", row: 1 });
  assert.deepEqual(regionsFromForms([{ ...good, derpPort: "99999" }]), { ok: false, problem: "invalid", row: 0 });
  assert.deepEqual(regionsFromForms([{ ...good, derpPort: "8x" }]), { ok: false, problem: "invalid", row: 0 });
  assert.deepEqual(regionsFromForms([{ ...good, name: "a,b" }]), { ok: false, problem: "invalid", row: 0 });
});

test("the credential is a 0600 file, passed to every command", () => {
  const file = "/state/mesh/mesh-credential.json";
  for (const req of [
    { op: "status" },
    { op: "login", kind: "tailscale", access: "write", secret: "tskey-api-x" },
    { op: "tags", device: "n1", tags: [], apply: false },
    { op: "policy.show" },
    { op: "derp.sync", target: { allowlist: "/a.json" }, allDevices: false },
  ] as const) {
    const argv = buildMeshArgs(req as MeshRequest, undefined, file);
    assert.ok(argv.includes(`--credential-file=${file}`), req.op);
  }
  // The secret itself is still only on stdin.
  const login = buildMeshArgs({ op: "login", kind: "tailscale", access: "write", secret: "tskey-api-x" }, undefined, file);
  assert.ok(!login.join(" ").includes("tskey-api-x"));
});

test("a read-only credential is reported as a permission problem, not a broken call", () => {
  const err = meshError({ code: 1, stdout: "", stderr: 'meshadmin: set tags: credential rejected (HTTP 403); it needs scope "devices:core"' });
  assert.equal(err.code, "insufficient_scope");
  const missing = meshError({ code: 1, stdout: "", stderr: '没有保存的凭据（profile "default"）' });
  assert.equal(missing.code, "no_credential");
});

test("status says which credential answered", async () => {
  const { svc } = service();
  const r = await svc.request({ op: "status" });
  assert.ok(r.ok);
  if (r.ok) {
    const s = r.data as ReturnType<typeof normalizeStatus>;
    assert.equal(s.credential?.access, "write");
    assert.equal(s.credential?.source, "file");
    assert.equal(s.credential?.file, "/state/mesh/mesh-credential.json");
  }
  const ro = service((f) => (f.writeCredential = false));
  const roStatus = await ro.svc.request({ op: "status" });
  assert.ok(roStatus.ok);
  if (roStatus.ok) assert.equal((roStatus.data as ReturnType<typeof normalizeStatus>).credential?.access, "read");
});

test("a pasted admission token becomes a 0600 file for the call and is deleted after", async () => {
  const seen: string[][] = [];
  const svc = new MeshService(
    async (args) => {
      seen.push(args);
      const file = args.find((a) => a.startsWith("--admission-token-file="))!.slice("--admission-token-file=".length);
      if (process.platform !== "win32") assert.equal(fs.statSync(file).mode & 0o777, 0o600);
      assert.equal(fs.readFileSync(file, "utf8"), "admin-TOKEN-value");
      return { code: 0, stdout: JSON.stringify({ added: 1, removed: 0, total: 1 }), stderr: "" };
    },
    () => undefined,
    () => "/state/mesh/mesh-credential.json",
  );
  const r = await svc.request({ op: "derp.sync", target: { url: "https://derp.example.com:8480", token: "admin-TOKEN-value" }, allDevices: false });
  assert.ok(r.ok);
  assert.ok(!seen[0]!.join(" ").includes("admin-TOKEN-value"), "the token is never in argv");
  const file = seen[0]!.find((a) => a.startsWith("--admission-token-file="))!.slice("--admission-token-file=".length);
  assert.equal(fs.existsSync(path.dirname(file)), false, "the temp directory is removed after the call");
});

test("a token and a token file cannot both be sent", () => {
  assert.equal(field({ op: "derp.sync", target: { url: "https://d.example", token: "t", tokenFile: "/t" }, allDevices: false }), "token");
  assert.equal(field({ op: "derp.sync", target: { url: "https://d.example", token: "bad token" }, allDevices: false }), "token");
  assert.ok(parseMeshRequest({ op: "derp.sync", target: { url: "https://d.example", token: "good-token" }, allDevices: false }).ok);
});
