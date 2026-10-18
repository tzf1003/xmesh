// The fake kernel is only useful if it speaks the contract. Everything it sends is validated against the
// JSON Schemas in docs/contracts/schemas, and the generated TypeScript copy must match the contract's.

import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as http from "node:http";
import * as path from "node:path";
import { after, before, test } from "node:test";
import Ajv2020 from "ajv/dist/2020";
import { CONTROL_API_VERSION, CONTROL_EVENT_TYPES, CONTROL_ROUTES } from "../../src/shared/control";
import { SseParser } from "../../src/shared/sse";
import { fixture, peerNamed, repoRoot, schemaDir } from "../../testserver/fixtures";
import { FakeKernel } from "../../testserver/server";

const ajv = new Ajv2020({ strict: false, allErrors: true });
for (const f of fs.readdirSync(schemaDir()).filter((n) => n.endsWith(".schema.json"))) {
  ajv.addSchema(JSON.parse(fs.readFileSync(path.join(schemaDir(), f), "utf8")));
}

function validate(schemaId: string, def: string, value: unknown): void {
  const v = ajv.getSchema(`urn:xmesh:schema:${schemaId}:1#/$defs/${def}`);
  assert.ok(v, `schema ${schemaId}#${def}`);
  assert.ok(v(value), `${def} invalid: ${ajv.errorsText(v.errors)}\n${JSON.stringify(value).slice(0, 300)}`);
}

interface Reply {
  status: number;
  headers: http.IncomingHttpHeaders;
  body: unknown;
}

function call(endpoint: string, method: string, url: string, body?: unknown, headers: http.OutgoingHttpHeaders = {}): Promise<Reply> {
  return new Promise((resolve, reject) => {
    const payload = body === undefined ? undefined : JSON.stringify(body);
    const req = http.request(
      { socketPath: endpoint, path: url, method, headers: { ...(payload ? { "Content-Type": "application/json" } : {}), ...headers } },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (c: Buffer) => chunks.push(c));
        res.on("end", () => {
          const text = Buffer.concat(chunks).toString("utf8");
          resolve({ status: res.statusCode ?? 0, headers: res.headers, body: text === "" ? null : JSON.parse(text) });
        });
      },
    );
    req.on("error", reject);
    if (payload) req.write(payload);
    req.end();
  });
}

/** Opens the event stream, collects messages for `ms`, closes it. */
function collectStream(endpoint: string, lastEventId?: string, ms = 150): Promise<{ id?: string; event: string; data: string }[]> {
  return new Promise((resolve, reject) => {
    const parser = new SseParser();
    const got: { id?: string; event: string; data: string }[] = [];
    const req = http.request(
      { socketPath: endpoint, path: "/v1/events", headers: lastEventId ? { "Last-Event-ID": lastEventId } : {} },
      (res) => {
        res.setEncoding("utf8");
        res.on("data", (c: string) => got.push(...parser.push(c)));
        setTimeout(() => {
          req.destroy();
          resolve(got);
        }, ms);
      },
    );
    req.on("error", (e) => {
      if ((e as NodeJS.ErrnoException).code !== "ECONNRESET") reject(e);
    });
    req.end();
  });
}

let k: FakeKernel;
before(async () => {
  k = await FakeKernel.start({ pingMs: 50 });
});
after(async () => {
  await k.stop();
});

test("the copied TypeScript types are the contract's generated file", () => {
  const upstream = fs.readFileSync(path.join(repoRoot(), "api", "control", "v1", "ts", "control.ts"), "utf8");
  const copy = fs.readFileSync(path.join(repoRoot(), "ui", "src", "shared", "generated", "control.ts"), "utf8");
  assert.ok(copy.endsWith(upstream), "run `npm run sync-types` in ui/");
});

test("the UI speaks the contract's API version", () => {
  assert.equal(CONTROL_API_VERSION, "1.3");
});

test("status, peers, sessions and control.json from the fake validate against the schemas", async () => {
  const status = await call(k.endpoint, "GET", "/v1/status");
  assert.equal(status.status, 200);
  assert.equal(status.headers["x-xmesh-control-version"], "1.3");
  validate("control-status", "StatusResponse", status.body);
  validate("control-peers", "PeersResponse", (await call(k.endpoint, "GET", "/v1/peers")).body);
  validate("control-sessions", "SessionsResponse", (await call(k.endpoint, "GET", "/v1/sessions")).body);
  validate("control-endpoint", "ControlEndpointFile", k.manifest());
});

test("drain follows RUL-16 and every answer validates", async () => {
  const keep = await call(k.endpoint, "POST", "/v1/drain", { mode: "keep" });
  validate("control-drain", "DrainResponse", keep.body);
  const again = await call(k.endpoint, "POST", "/v1/drain", {});
  assert.deepEqual((again.body as { drain: unknown }).drain, (keep.body as { drain: unknown }).drain, "repeat keep is a no-op");
  const kill = await call(k.endpoint, "POST", "/v1/drain", { mode: "kill" });
  assert.equal((kill.body as { drain: { mode: string; since: string } }).drain.mode, "kill");
  assert.equal((kill.body as { drain: { since: string } }).drain.since, (keep.body as { drain: { since: string } }).drain.since, "since is kept on upgrade");
  const down = await call(k.endpoint, "POST", "/v1/drain", { mode: "keep" });
  assert.equal(down.status, 409);
  validate("control-error", "ControlError", down.body);
  assert.equal((down.body as { reason: string }).reason, "drain_downgrade");
  const resumed = await call(k.endpoint, "POST", "/v1/resume");
  validate("control-drain", "DrainResponse", resumed.body);
  assert.equal((resumed.body as { drain: { draining: boolean } }).drain.draining, false);
  assert.equal((await call(k.endpoint, "POST", "/v1/resume")).status, 200, "resume when not draining is a no-op");
});

test("errors are ControlError with the contract's HTTP status", async () => {
  for (const [method, url, body, headers, want] of [
    ["POST", "/v1/drain", { mode: "nope" }, {}, 400],
    ["POST", "/v1/drain", { bogus: 1 }, {}, 400],
    ["GET", "/v1/status", undefined, { Origin: "http://evil.example" }, 403],
    ["GET", "/v1/nothing", undefined, {}, 404],
  ] as const) {
    const r = await call(k.endpoint, method, url, body, headers);
    assert.equal(r.status, want, `${method} ${url}`);
    validate("control-error", "ControlError", r.body);
  }
});

test("a capability the kernel does not advertise answers 501 not_implemented", async () => {
  const partial = await FakeKernel.start({ capabilities: ["status"] });
  try {
    const r = await call(partial.endpoint, "GET", "/v1/peers");
    assert.equal(r.status, 501);
    assert.equal((r.body as { error: string }).error, "not_implemented");
    validate("control-error", "ControlError", r.body);
    assert.equal((await call(partial.endpoint, "GET", "/v1/status")).status, 200);
  } finally {
    await partial.stop();
  }
});

test("the event stream starts with hello, then replays from Last-Event-ID, and every event validates", async () => {
  k.emit("peer", { change: "offline", peer: peerNamed("linux-box") });
  k.emit("warning", { change: "raised", warning: { code: "peer_offline", severity: "warning", message: "对端 linux-box 离线" } });
  const first = await collectStream(k.endpoint);
  assert.equal(first[0]!.event, "hello");
  assert.equal(first[0]!.id, undefined, "hello has no id");
  for (const m of first) validate("control-events", "ControlEvent", JSON.parse(m.data));
  const hello = JSON.parse(first[0]!.data) as { data: { boot_id: string; head: string | null; heartbeat_s: number } };
  assert.equal(hello.data.heartbeat_s, 1);
  assert.match(hello.data.head ?? "", /^[0-9a-f]+\.\d+$/);

  const head = hello.data.head!;
  k.emit("session", { change: "closed", session: fixture<{ data: { session: unknown } }>("ControlEvent.session-pty").data.session });
  const resumed = await collectStream(k.endpoint, head);
  assert.deepEqual(resumed.map((m) => m.event), ["hello", "session"]);
  assert.match(resumed[1]!.id ?? "", new RegExp(`^${head.split(".")[0]}\\.`));
});

test("a Last-Event-ID from another boot or from before the retained history gets reset", async () => {
  const other = await collectStream(k.endpoint, "deadbeef.7");
  const reset = other.find((m) => m.event === "reset");
  assert.ok(reset);
  validate("control-events", "ControlEvent", JSON.parse(reset.data));
  assert.equal((JSON.parse(reset.data) as { data: { reason: string } }).data.reason, "boot_changed");

  const fresh = await FakeKernel.start({ pingMs: 50 });
  try {
    fresh.emit("status", fresh.currentStatus);
    fresh.emit("status", fresh.currentStatus);
    fresh.emit("status", fresh.currentStatus);
    fresh.forgetEvents();
    const boot = fresh.manifest().boot_id;
    const gap = await collectStream(fresh.endpoint, `${boot}.1`);
    assert.equal((JSON.parse(gap.find((m) => m.event === "reset")!.data) as { data: { reason: string } }).data.reason, "gap");
  } finally {
    await fresh.stop();
  }
});

test("a ping comment keeps an idle stream alive", async () => {
  const raw = await new Promise<string>((resolve) => {
    let text = "";
    const req = http.request({ socketPath: k.endpoint, path: "/v1/events" }, (res) => {
      res.setEncoding("utf8");
      res.on("data", (c: string) => (text += c));
      setTimeout(() => {
        req.destroy();
        resolve(text);
      }, 200);
    });
    req.on("error", () => {});
    req.end();
  });
  assert.match(raw, /: ping\n/);
});

test("routes the UI calls exist in the contract route table with the capability the UI checks", () => {
  const find = (m: string, p: string) => CONTROL_ROUTES.find((r) => r.method === m && r.path === p);
  assert.equal(find("GET", "/v1/status")?.capability, "status");
  assert.equal(find("GET", "/v1/peers")?.capability, "peers");
  assert.equal(find("GET", "/v1/events")?.capability, "events");
  assert.equal(find("POST", "/v1/drain")?.capability, "drain");
  assert.equal(find("POST", "/v1/resume")?.capability, "drain");
  assert.equal(find("POST", "/v1/resume")?.body, null, "resume takes no body");
  assert.equal(find("PUT", "/v1/gateway")?.capability, "gateway");
  assert.equal(find("PUT", "/v1/gateway")?.body, "GatewayUpdateRequest");
  assert.equal(find("PUT", "/v1/gateway")?.response, "GatewayUpdateResponse");
  for (const t of ["hello", "reset", "status", "peer", "call", "session", "warning", "config", "lifecycle"]) {
    assert.ok((CONTROL_EVENT_TYPES as readonly string[]).includes(t), t);
  }
});

test("PUT /v1/gateway: the reach replacement validates, answers restart_required for a listen change only, and the status follows", async () => {
  const gw = await FakeKernel.start({ pingMs: 50, capabilities: ["status", "events", "peers", "sessions", "drain", "pats", "gateway"] });
  try {
    const ok = await call(gw.endpoint, "PUT", "/v1/gateway", { listen: ":8080", access: ["localhost", "lan", "10.0.0.0/8"], hosts: ["xmesh.home.example"] });
    assert.equal(ok.status, 200);
    validate("control-gateway", "GatewayUpdateResponse", ok.body);
    assert.equal((ok.body as { restart_required: boolean }).restart_required, false, "access and hosts apply at once");
    const status = await call(gw.endpoint, "GET", "/v1/status");
    validate("control-status", "StatusResponse", status.body);
    const info = (status.body as { gateway: { access: string[]; hosts: string[]; addresses: { kind: string; url: string }[] } }).gateway;
    assert.deepEqual(info.access, ["localhost", "lan", "10.0.0.0/8"]);
    assert.deepEqual(info.hosts, ["xmesh.home.example"]);
    assert.ok(info.addresses.some((a) => a.kind === "host" && a.url === "http://xmesh.home.example:8080/mcp"));

    const loopback = await call(gw.endpoint, "PUT", "/v1/gateway", { listen: "127.0.0.1:8080", access: ["localhost"], hosts: [] });
    assert.equal((loopback.body as { restart_required: boolean }).restart_required, true, "a listen change needs a restart");

    const bad = await call(gw.endpoint, "PUT", "/v1/gateway", { listen: ":8080", access: ["192.168.1.999"], hosts: [] });
    assert.equal(bad.status, 422);
    validate("control-error", "ControlError", bad.body);
    assert.equal((bad.body as { error: string }).error, "config_invalid");
    const hostIp = await call(gw.endpoint, "PUT", "/v1/gateway", { listen: ":8080", access: ["localhost"], hosts: ["192.168.1.5"] });
    assert.equal(hostIp.status, 422, "an IP address is not a host name");
  } finally {
    await gw.stop();
  }
});
