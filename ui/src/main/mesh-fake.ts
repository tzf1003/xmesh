// An in-process stand-in for `xmesh mesh ... --json`, used by the unit tests
// and by the Playwright specs (XMESH_UI_TEST=1 only; main.ts never wires it
// into a packaged app). It follows the CLI's argument handling and JSON shapes
// closely enough that the real service code runs unchanged on top of it.

import type { MeshRun, MeshRunner } from "./mesh-service";

export interface FakeMeshState {
  provider: "tailscale" | "headscale";
  /** false: every command fails like the CLI does without a saved profile. */
  credential: boolean;
  /** false: commands that need write access fail like a read-only profile. */
  writeCredential: boolean;
  policyText: string;
  version: number;
  snapshots: { id: string; text: string; version: string; label: string }[];
  /** Region hosts the fake DERP probe refuses. */
  failingHosts: string[];
  /** Every argv the service ran, for assertions. */
  calls: string[][];
  /** Written by `authkey --out`, by path. The panel only ever sees the path. */
  keyFiles: string[];
  devices: FakeDevice[];
  /** Extra latency so tests can observe a busy state. */
  delayMs: number;
}

interface FakeDevice {
  id: string;
  name: string;
  tags: string[];
  online: boolean;
  roles: string[];
  issues: { severity: string; code: string; message: string }[];
}

export function freshFakeMesh(): FakeMeshState {
  return {
    provider: "tailscale",
    credential: true,
    writeCredential: true,
    policyText: '{\n  "tagOwners": { "tag:device": ["autogroup:admin"] },\n  "acls": []\n}\n',
    version: 1,
    snapshots: [],
    failingHosts: ["bad.example.com"],
    calls: [],
    keyFiles: [],
    delayMs: 0,
    devices: [
      { id: "n100", name: "laptop", tags: ["tag:device"], online: true, roles: ["agent", "gateway"], issues: [] },
      { id: "n200", name: "nas", tags: ["tag:peer"], online: true, roles: ["agent"], issues: [] },
      {
        id: "n300",
        name: "stray",
        tags: [],
        online: false,
        roles: [],
        issues: [{ severity: "warn", code: "no_tag", message: "设备没有 tag，策略无法按 tag 授权它" }],
      },
    ],
  };
}

const flag = (args: string[], name: string): string | undefined => {
  const prefix = `--${name}=`;
  return args.find((a) => a.startsWith(prefix))?.slice(prefix.length);
};
const has = (args: string[], name: string): boolean => args.includes(`--${name}`);
const all = (args: string[], name: string): string[] => args.filter((a) => a.startsWith(`--${name}=`)).map((a) => a.slice(name.length + 3));

const ok = (doc: unknown): MeshRun => ({ code: 0, stdout: `${JSON.stringify(doc)}\n`, stderr: "" });
const fail = (stderr: string, code = 1, stdout = ""): MeshRun => ({ code, stdout, stderr });

function noCredential(): MeshRun {
  return fail('没有保存的凭据（profile "default"）。先运行: xmesh mesh login --kind tailscale|headscale --url ... --access read|write');
}

function planDoc(s: FakeMeshState, label: string, after: string): Record<string, unknown> {
  const changed = after !== s.policyText;
  return {
    provider: s.provider,
    label,
    base_version: `v${s.version}`,
    base_exists: true,
    before: s.policyText,
    after,
    diff: changed ? `--- policy (before)\n+++ policy (after)\n-${s.policyText.split("\n")[1] ?? ""}\n+${after.split("\n")[1] ?? ""}\n` : "",
    changed,
    warnings: changed ? null : undefined,
    writable: s.provider === "tailscale",
  };
}

function writePolicy(s: FakeMeshState, label: string, after: string): { snapshot_id?: string; version: string; changed: boolean } {
  const changed = after !== s.policyText;
  let snapshot_id: string | undefined;
  if (changed) {
    snapshot_id = `snap-${s.snapshots.length + 1}`;
    s.snapshots.push({ id: snapshot_id, text: s.policyText, version: `v${s.version}`, label });
    s.policyText = after;
    s.version += 1;
  }
  return { ...(snapshot_id !== undefined && { snapshot_id }), version: `v${s.version}`, changed };
}

function needsWrite(s: FakeMeshState): MeshRun | undefined {
  if (!s.credential || !s.writeCredential) return noCredential();
  return undefined;
}

function stale(args: string[], s: FakeMeshState): MeshRun | undefined {
  const expect = flag(args, "expect-version");
  return expect !== undefined && expect !== `v${s.version}`
    ? fail("conflict: 预览时的策略版本与现在不一致，请重新预览")
    : undefined;
}

function spec(arg: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const kv of arg.split(",")) {
    const i = kv.indexOf("=");
    out[kv.slice(0, i)] = kv.slice(i + 1);
  }
  return out;
}

function probesFor(s: FakeMeshState, regions: string[]): { host: string; ok: boolean; checks: unknown[] }[] {
  return regions.map((r) => {
    const host = spec(r).host ?? "";
    const good = !s.failingHosts.includes(host);
    return {
      host,
      ok: good,
      checks: [
        { name: "tcp", ok: good, detail: good ? "connected to 443" : "connection refused", latency_ns: good ? 12_000_000 : 0 },
        { name: "tls", ok: good, detail: good ? "certificate valid" : "no TLS" },
      ],
    };
  });
}

function derpAfter(s: FakeMeshState, regions: string[]): string {
  const names = regions.map((r) => spec(r).code).join(",");
  return s.policyText.replace(/\n\}\n$/, `,\n  "derpMap": { "regions": "${names}" }\n}\n`);
}

/** Interprets one `xmesh mesh ...` command line. */
export function runFakeMesh(s: FakeMeshState, argv: string[]): MeshRun {
  s.calls.push([...argv]);
  const [group, sub, ...rest] = argv;
  if (group !== "mesh" || sub === undefined) return fail("unexpected command");
  const args = rest;
  if (!s.credential) return noCredential();

  switch (sub) {
    case "status":
      return ok({
        provider: s.provider,
        capabilities: { policy_write: s.provider === "tailscale", atomic_policy_write: s.provider === "tailscale", derp_via_policy: s.provider === "tailscale" },
        devices: s.devices.map((d) => ({
          device: { id: d.id, name: d.name, hostname: d.name, addresses: [`100.64.0.${d.id.slice(1, 2)}`], tags: d.tags.length === 0 ? null : d.tags, online: d.online, authorized: true, os: "linux", last_seen: d.online ? "2026-10-16T08:00:00Z" : "0001-01-01T00:00:00Z" },
          roles: d.roles.length === 0 ? null : d.roles,
          issues: d.issues,
          tags: d.tags,
        })),
      });
    case "tags": {
      const device = flag(args, "device");
      const dev = s.devices.find((d) => d.id === device);
      if (!dev) return fail(`找不到设备 "${device}"`);
      const to = (flag(args, "set") ?? "").split(",").filter(Boolean);
      const missing = to.filter((t) => !s.policyText.includes(`"${t}"`));
      const doc: Record<string, unknown> = { device: dev.id, from: dev.tags, to, missing_tag_owners: missing.length === 0 ? null : missing, applied: false };
      if (missing.length > 0) doc.error = `policy tagOwners lacks: ${missing.join(",")}`;
      if (has(args, "apply")) {
        const w = needsWrite(s);
        if (w) return w;
        if (missing.length > 0) return fail(`策略的 tagOwners 里没有 ${missing.join(",")}；先用 \`xmesh mesh policy --template ...\` 或手工补上`);
        dev.tags = to;
        doc.applied = true;
        doc.now = to;
      }
      return ok(doc);
    }
    case "authkey": {
      const w = needsWrite(s);
      if (w) return w;
      const file = flag(args, "out");
      if (!file) return fail("需要 --out 文件");
      s.keyFiles.push(file);
      return ok({ key: { id: "k123", expires: "2026-10-16T10:00:00Z", tags: (flag(args, "tags") ?? "").split(",") }, file });
    }
    case "policy": {
      if (has(args, "show")) return ok({ policy: { text: s.policyText, version: `v${s.version}`, exists: true }, managed_sections: null });
      if (has(args, "snapshots")) {
        return ok({ snapshots: s.snapshots.map((x) => ({ id: x.id, provider: s.provider, time: "2026-10-16T08:30:00Z", label: x.label, version: x.version })) });
      }
      const w = needsWrite(s);
      if (w) return w;
      let label: string;
      let after: string;
      const template = flag(args, "template");
      const rollback = flag(args, "rollback");
      if (rollback !== undefined) {
        const snap = s.snapshots.find((x) => x.id === rollback);
        if (!snap) return fail(`snapshot ${rollback} not found`);
        label = `rollback to ${snap.id}`;
        after = snap.text;
      } else if (template !== undefined) {
        const owner = flag(args, "owner");
        if (!owner) return fail("需要 --owner");
        label = `template ${template}`;
        after = `{\n  "tagOwners": { "tag:device": ["${owner}"], "tag:peer": ["${owner}"] },\n  "acls": [{ "action": "accept", "src": ["tag:device"], "dst": ["tag:device:7433"] }]\n}\n`;
      } else return fail("需要 --show、--snapshots、--rollback 或 --template 之一");
      const doc: Record<string, unknown> = { plan: planDoc(s, label, after), applied: false };
      if (has(args, "apply")) {
        const c = stale(args, s);
        if (c) return c;
        if (s.provider !== "tailscale") return fail("该控制面不允许远程写策略");
        doc.applied = true;
        doc.result = writePolicy(s, label, after);
      }
      return ok(doc);
    }
    case "matrix": {
      const cells = [
        { src: "laptop", dst: "nas", network: "allow", tools: "allow", allowed_tools: ["exec.run", "fs.read"], backends: null },
        { src: "nas", dst: "laptop", network: "allow", tools: "deny" },
        { src: "stray", dst: "laptop", network: "deny", network_note: "stray 没有 tag", tools: "allow", mismatch: "工具层放行但网络层拒绝" },
      ];
      const only = has(args, "mismatch-only");
      return ok({ port: Number(flag(args, "port") ?? 7433), cells: only ? cells.filter((c) => "mismatch" in c) : cells });
    }
    case "derp":
      return runFakeDerp(s, args);
  }
  return fail(`未知子命令: mesh ${sub}`);
}

function runFakeDerp(s: FakeMeshState, argv: string[]): MeshRun {
  const [verb, ...args] = argv;
  const regions = all(args, "region");
  switch (verb) {
    case "plan":
    case "apply": {
      const w = needsWrite(s);
      if (w) return w;
      if (s.provider !== "tailscale") return fail("headscale 的 DERP 是服务器配置，不能经 API 写入。运行 `xmesh mesh derp headscale ...` 生成 derp.paths 文件和配置片段");
      const after = derpAfter(s, regions);
      const plan = planDoc(s, "derp regions", after);
      if (verb === "plan") return ok({ plan, applied: false });
      const c = stale(args, s);
      if (c) return c;
      const before = { text: s.policyText, version: s.version };
      const result = writePolicy(s, "derp regions", after);
      const probes = probesFor(s, regions);
      if (probes.some((p) => !p.ok)) {
        s.policyText = before.text;
        s.version = before.version;
        return fail("DERP 探测未通过，已恢复写入前的策略", 1, `${JSON.stringify({ plan, result: { ...result, probes, rolled_back: true }, regions: [] })}\n`);
      }
      return ok({ plan, result: { ...result, probes, rolled_back: false }, regions: [] });
    }
    case "probe": {
      const probes = probesFor(s, regions);
      const doc: Record<string, unknown> = { probes };
      if (has(args, "compare-official")) doc.official = [{ region_id: 1, code: "nyc", host: "derp1.tailscale.com", latency_ns: 80_000_000 }, { region_id: 9, code: "sin", host: "derp9.tailscale.com", latency_ns: 0, error: "timeout" }];
      if (regions.length === 1) doc.warnings = ["只有一个自建 region：它是单点，节点无法中继时没有备选"];
      return probes.every((p) => p.ok) ? ok(doc) : fail("DERP 探测未通过", 1, `${JSON.stringify(doc)}\n`);
    }
    case "headscale": {
      const first = spec(regions[0] ?? "code=x,host=x");
      return ok({ file: `regions:\n  900:\n    regionid: 900\n    regioncode: ${first.code}\n`, snippet: "derp:\n  paths:\n    - /etc/headscale/xmesh-derp.yaml\n", warnings: null, written: "" });
    }
    case "sync": {
      if (!flag(args, "allowlist") && !flag(args, "admission-url")) return fail("--allowlist 与 --admission-url 二选一");
      return ok({ added: 2, removed: 1, total: 3 });
    }
  }
  return fail(`未知子命令: mesh derp ${verb ?? ""}`);
}

/** A runner over `state`, with `state.delayMs` of latency. */
export function fakeMeshRunner(state: FakeMeshState): MeshRunner {
  return async (argv) => {
    if (state.delayMs > 0) await new Promise((r) => setTimeout(r, state.delayMs));
    return runFakeMesh(state, argv);
  };
}
