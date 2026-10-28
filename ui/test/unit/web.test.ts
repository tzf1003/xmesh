import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import * as fs from "node:fs";
import * as http from "node:http";
import type { AddressInfo } from "node:net";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { CHANNELS, PANEL_API_METHODS } from "../../src/shared/ipc";
import { EMPTY_SNAPSHOT } from "../../src/shared/state";
import { WEB_PATHS, WEB_REQUEST_HEADER, parseWebAnswer, type WebAsk } from "../../src/shared/web";
import { createHandlers, type HandlerDeps, type Handler } from "../../src/main/handlers";
import { generateKey, keyMatches, loadKey, LoginLimiter, Sessions } from "../../src/web/auth";
import { WebBridge } from "../../src/web/bridge";
import { isLoopback, parseListen, parseWebArgs } from "../../src/web/main";
import { WEB_CSP, WebServer, parseCookies, sameOrigin } from "../../src/web/server";

function tmp(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-web-"));
}

test("the key file is created 0600 once, reused, and rotated on request", { skip: process.platform === "win32" }, () => {
  const dir = tmp();
  const file = path.join(dir, "sub", "webui.key");
  const first = loadKey(file);
  assert.equal(first.created, true);
  assert.ok(first.key.length >= 40);
  assert.equal(fs.statSync(file).mode & 0o777, 0o600);
  assert.deepEqual(loadKey(file), { key: first.key, created: false });
  const rotated = loadKey(file, true);
  assert.equal(rotated.created, true);
  assert.notEqual(rotated.key, first.key);
  assert.equal(loadKey(file).key, rotated.key);
});

test("a key file others can read, or a short key, is refused", { skip: process.platform === "win32" }, () => {
  const dir = tmp();
  const file = path.join(dir, "webui.key");
  fs.writeFileSync(file, `${generateKey()}\n`, { mode: 0o644 });
  fs.chmodSync(file, 0o644);
  assert.throws(() => loadKey(file), /chmod 600/);
  fs.writeFileSync(file, "short\n");
  fs.chmodSync(file, 0o600);
  assert.throws(() => loadKey(file), /characters/);
});

test("keys compare exactly", () => {
  const key = generateKey();
  assert.equal(keyMatches(key, key), true);
  for (const bad of [key.slice(1), `${key} `, "", undefined, null, 1, { toString: () => key }, key.toUpperCase()]) {
    assert.equal(keyMatches(key, bad), false);
  }
});

test("sessions end after idling, after their maximum age, and beyond the limit", () => {
  let now = 0;
  const s = new Sessions({ idleMs: 100, maxMs: 1000, limit: 2, now: () => now });
  const a = s.create();
  assert.equal(s.touch(a), true);
  now = 90;
  assert.equal(s.touch(a), true);
  now = 191;
  assert.equal(s.touch(a), false, "idle");
  const b = s.create();
  for (now = 250; now < 1250; now += 90) assert.equal(s.touch(b), now - 191 <= 1000, `at ${now}`);
  const c = s.create();
  const d = s.create();
  const e = s.create();
  assert.equal(s.touch(c), false, "the oldest is dropped at the limit");
  assert.equal(s.touch(d) && s.touch(e), true);
  assert.equal(s.touch(undefined), false);
  assert.equal(s.touch("forged"), false);
});

test("login failures back off per address and a success clears them", () => {
  let now = 0;
  const l = new LoginLimiter({ free: 2, baseMs: 1000, maxMs: 4000, now: () => now });
  l.fail("a");
  l.fail("a");
  assert.equal(l.wait("a"), 0);
  l.fail("a");
  assert.equal(l.wait("a"), 1000);
  l.fail("a");
  assert.equal(l.wait("a"), 2000);
  l.fail("a");
  l.fail("a");
  assert.equal(l.wait("a"), 4000, "capped");
  assert.equal(l.wait("b"), 0);
  now = 4000;
  assert.equal(l.wait("a"), 0);
  l.succeed("a");
  l.fail("a");
  assert.equal(l.wait("a"), 0);
});

test("listen addresses and options", () => {
  assert.deepEqual(parseListen("127.0.0.1:7480"), { host: "127.0.0.1", port: 7480 });
  assert.deepEqual(parseListen(":8443"), { host: "0.0.0.0", port: 8443 });
  assert.deepEqual(parseListen("[::1]:7480"), { host: "::1", port: 7480 });
  for (const bad of ["7480", "host:", "host:0", "host:70000", "::1:80", "a b:80"]) assert.equal(parseListen(bad), undefined, bad);
  assert.equal(isLoopback("127.0.0.1") && isLoopback("::1") && isLoopback("localhost"), true);
  assert.equal(isLoopback("0.0.0.0") || isLoopback("10.0.0.1"), false);

  const o = parseWebArgs(["--listen", "0.0.0.0:9000", "--data-dir", "/srv/x"], {});
  assert.equal(o.host, "0.0.0.0");
  assert.equal(o.port, 9000);
  assert.equal(o.keyFile, path.resolve("/srv/x/webui.key"));
  assert.throws(() => parseWebArgs(["--tls-cert", "a.pem"], {}), /together/);
  assert.deepEqual(parseWebArgs(["--allowed-origin", "https://P.example.com:8088/"], { XMESH_WEBUI_ALLOWED_ORIGINS: "http://a, https://p.example.com:8088" }).allowedOrigins, ["https://p.example.com:8088", "http://a"]);
  assert.deepEqual(parseWebArgs([], {}).allowedOrigins, []);
  assert.throws(() => parseWebArgs(["--allowed-origin", ""], {}), /needs a value/);
  assert.throws(() => parseWebArgs(["--allowed-origin", "https://h/path"], {}), /scheme:\/\/host/);
  assert.throws(() => parseWebArgs(["--listen", "nope"], {}), /host:port/);
  assert.throws(() => parseWebArgs(["--bogus"], {}));
});

test("every channel has a handler, and every panel method reaches a channel the shim can name", () => {
  const handlers = createHandlers({} as HandlerDeps);
  assert.deepEqual([...handlers.keys()].sort(), Object.values(CHANNELS).sort());
  // src/renderer/web/boot.ts: a method calls the CHANNELS key of the same name, except checkForUpdate.
  for (const m of PANEL_API_METHODS) {
    if (/^on[A-Z]/.test(m)) continue;
    const key = m === "checkForUpdate" ? "checkUpdate" : m;
    assert.ok(key in CHANNELS, m);
  }
});

test("answers are validated", () => {
  assert.deepEqual(parseWebAnswer({ id: "abcdefgh12", value: true }), { id: "abcdefgh12", value: true });
  assert.deepEqual(parseWebAnswer({ id: "abcdefgh12", value: null }), { id: "abcdefgh12", value: null });
  for (const bad of [null, [], { id: "x", value: true }, { id: "abcdefgh12", value: 1 }, { id: "abcdefgh12", value: "a\0b" }, { id: "../../etc/pa", value: true }]) {
    assert.equal(parseWebAnswer(bad), undefined);
  }
});

test("the web CSP only adds same-origin connections to the panel CSP", () => {
  assert.match(WEB_CSP, /connect-src 'self'/);
  assert.doesNotMatch(WEB_CSP, /unsafe|connect-src 'none'|https?:/);
  assert.match(WEB_CSP, /frame-ancestors 'none'/);
});

test("cookie and origin parsing", () => {
  assert.equal(parseCookies("a=1; xmesh_webui=abc; b=2").get("xmesh_webui"), "abc");
  const req = (h: Record<string, string>) => ({ headers: h }) as unknown as http.IncomingMessage;
  assert.equal(sameOrigin(req({ host: "h:1" }), false), true);
  assert.equal(sameOrigin(req({ host: "h:1", origin: "http://h:1" }), false), true);
  assert.equal(sameOrigin(req({ host: "h:1", origin: "https://h:1" }), false), false);
  assert.equal(sameOrigin(req({ host: "h:1", origin: "https://h:1" }), true), true);
  assert.equal(sameOrigin(req({ host: "h:1", origin: "http://evil" }), false), false);
  assert.equal(sameOrigin(req({ host: "h:1", "sec-fetch-site": "cross-site" }), false), false);
  // A reverse proxy: the backend is plain HTTP but the browser's origin is the public https one.
  const proxied = { host: "h", origin: "https://h:8088", "sec-fetch-site": "same-origin" };
  assert.equal(sameOrigin(req(proxied), false), false);
  assert.equal(sameOrigin(req(proxied), false, ["https://h:8088"]), true);
  assert.equal(sameOrigin(req({ ...proxied, origin: "https://evil" }), false, ["https://h:8088"]), false);
  assert.equal(sameOrigin(req({ ...proxied, "sec-fetch-site": "cross-site" }), false, ["https://h:8088"]), false);
});

class FakeController extends EventEmitter {
  snapshot = EMPTY_SNAPSHOT;
}

async function startServer(key: string): Promise<{ base: string; close: () => Promise<void>; bridge: WebBridge; server: WebServer; calls: unknown[][] }> {
  const pub = tmp();
  fs.writeFileSync(path.join(pub, "index.html"), "<html><meta content=\"__XMESH_STYLE_NONCE__\"></html>");
  fs.writeFileSync(path.join(pub, "web.js"), "1");
  let server: WebServer | undefined;
  const bridge = new WebBridge((s, a: WebAsk) => server?.sendAsk(s, a) ?? false, 2000);
  const calls: unknown[][] = [];
  const handlers = new Map<string, Handler>([
    [CHANNELS.getSnapshot, () => EMPTY_SNAPSHOT],
    [CHANNELS.copy, (...args) => { calls.push(args); bridge.effect({ type: "clipboard", text: "secret-connect-text" }); return { ok: true }; }],
    [CHANNELS.pickPath, async () => ({ ok: true, data: { path: (await bridge.pickPath("workspace")) ?? null } })],
    [CHANNELS.drain, (...args) => { calls.push(args); throw new Error("boom"); }],
  ]);
  server = new WebServer({ key: () => key, handlers, controller: new FakeController(), bridge, publicRoot: pub, secure: false, pingMs: 60_000 });
  const listener = http.createServer(server.handle);
  await new Promise<void>((r) => listener.listen(0, "127.0.0.1", r));
  const { port } = listener.address() as AddressInfo;
  return {
    base: `http://127.0.0.1:${port}`, bridge, server, calls,
    close: async () => { server!.close(); listener.closeAllConnections(); await new Promise<void>((r) => listener.close(() => r())); },
  };
}

test("the session cookie is Secure for TLS and for https allowed origins only", () => {
  const cookie = (secure: boolean, allowedOrigins: string[]) =>
    (new WebServer({ key: () => "k", handlers: new Map(), controller: new FakeController(), bridge: new WebBridge(() => false, 10), publicRoot: tmp(), secure, allowedOrigins }) as unknown as { cookie(v: string, m: number): string }).cookie("v", 1);
  assert.doesNotMatch(cookie(false, []), /Secure/);
  assert.doesNotMatch(cookie(false, ["http://h"]), /Secure/);
  assert.match(cookie(false, ["https://h"]), /; Secure/);
  assert.match(cookie(true, []), /; Secure/);
});

const json = { "Content-Type": "application/json", [WEB_REQUEST_HEADER]: "1" };

async function login(base: string, key: string): Promise<string> {
  const res = await fetch(`${base}${WEB_PATHS.login}`, { method: "POST", headers: json, body: JSON.stringify({ key }) });
  assert.equal(res.status, 200);
  const cookie = res.headers.get("set-cookie") ?? "";
  assert.match(cookie, /HttpOnly/);
  assert.match(cookie, /SameSite=Strict/);
  assert.doesNotMatch(cookie, /Secure/);
  return cookie.split(";")[0]!;
}

test("web server: static files, login, calls with effects, and logout", async () => {
  const key = generateKey();
  const s = await startServer(key);
  try {
    const page = await fetch(`${s.base}/`);
    assert.equal(page.status, 200);
    const nonce = /nonce-([^']+)'/.exec(page.headers.get("content-security-policy") ?? "")?.[1];
    assert.ok(nonce);
    assert.equal(await page.text(), `<html><meta content="${nonce}"></html>`);
    assert.equal(page.headers.get("x-frame-options"), "DENY");
    for (const p of ["/%2e%2e/etc/passwd", "/%2e%2e%2fpackage.json", "/nope.js", "/a%5cb.js"]) assert.equal((await fetch(`${s.base}${p}`)).status, 404, p);

    assert.deepEqual(await (await fetch(`${s.base}${WEB_PATHS.session}`)).json(), { authenticated: false });
    const anon = await fetch(`${s.base}${WEB_PATHS.invoke}getSnapshot`, { method: "POST", headers: json, body: "{}" });
    assert.equal(anon.status, 401);

    const wrong = await fetch(`${s.base}${WEB_PATHS.login}`, { method: "POST", headers: json, body: JSON.stringify({ key: "wrong" }) });
    assert.equal(wrong.status, 401);
    assert.equal(wrong.headers.get("set-cookie"), null);

    const noHeader = await fetch(`${s.base}${WEB_PATHS.login}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ key }) });
    assert.equal(noHeader.status, 403);
    const form = await fetch(`${s.base}${WEB_PATHS.login}`, { method: "POST", headers: { [WEB_REQUEST_HEADER]: "1", "Content-Type": "application/x-www-form-urlencoded" }, body: `key=${key}` });
    assert.equal(form.status, 415);
    const cross = await fetch(`${s.base}${WEB_PATHS.login}`, { method: "POST", headers: { ...json, Origin: "http://evil.example" }, body: JSON.stringify({ key }) });
    assert.equal(cross.status, 403);

    const cookie = await login(s.base, key);
    const authed = { ...json, Cookie: cookie };
    assert.deepEqual(await (await fetch(`${s.base}${WEB_PATHS.session}`, { headers: { Cookie: cookie } })).json(), { authenticated: true });

    const copy = await fetch(`${s.base}${WEB_PATHS.invoke}copy`, { method: "POST", headers: authed, body: JSON.stringify({ args: ["gatewayUrl"] }) });
    assert.deepEqual(await copy.json(), { result: { ok: true }, effects: [{ type: "clipboard", text: "secret-connect-text" }] });
    assert.deepEqual(s.calls, [["gatewayUrl"]]);

    for (const k of ["__proto__", "constructor", "toString", "nope", "xmesh:copy"]) {
      assert.equal((await fetch(`${s.base}${WEB_PATHS.invoke}${k}`, { method: "POST", headers: authed, body: "{}" })).status, 404, k);
    }
    assert.equal((await fetch(`${s.base}${WEB_PATHS.invoke}copy`, { method: "POST", headers: authed, body: JSON.stringify({ args: "x" }) })).status, 400);
    assert.equal((await fetch(`${s.base}${WEB_PATHS.invoke}copy`, { method: "POST", headers: authed, body: "{" })).status, 400);
    const failed = await fetch(`${s.base}${WEB_PATHS.invoke}drain`, { method: "POST", headers: authed, body: "{}" });
    assert.equal(failed.status, 500);
    assert.deepEqual(await failed.json(), { error: "internal error" });

    const out = await fetch(`${s.base}${WEB_PATHS.logout}`, { method: "POST", headers: authed, body: "{}" });
    assert.match(out.headers.get("set-cookie") ?? "", /Max-Age=0/);
    assert.equal((await fetch(`${s.base}${WEB_PATHS.invoke}getSnapshot`, { method: "POST", headers: authed, body: "{}" })).status, 401);
  } finally {
    await s.close();
  }
});

/** Reads server-sent events until `want` returns a value; the stream stays open, as a page keeps it. */
async function readEvents<T>(res: Response, want: (event: string, data: unknown) => T | undefined): Promise<T> {
  const reader = res.body!.getReader();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) throw new Error("stream ended");
    buf += new TextDecoder().decode(value);
    let i: number;
    while ((i = buf.indexOf("\n\n")) >= 0) {
      const block = buf.slice(0, i);
      buf = buf.slice(i + 2);
      const event = /^event: (.*)$/m.exec(block)?.[1];
      const data = /^data: (.*)$/m.exec(block)?.[1];
      if (event && data !== undefined) {
        const got = want(event, JSON.parse(data));
        if (got !== undefined) { reader.releaseLock(); return got; }
      }
    }
  }
}

test("web server: a question goes to the asking session's stream and only it can answer", async () => {
  const key = generateKey();
  const s = await startServer(key);
  try {
    const a = await login(s.base, key);
    const b = await login(s.base, key);
    const stream = await fetch(`${s.base}${WEB_PATHS.events}`, { headers: { Cookie: a } });
    assert.equal(stream.headers.get("content-type"), "text/event-stream; charset=utf-8");
    assert.equal(await readEvents(stream, (e) => e === "snapshot" ? true : undefined), true);

    const asked = readEvents(stream, (e, d) => e === "ask" ? d as WebAsk : undefined);
    const call = fetch(`${s.base}${WEB_PATHS.invoke}pickPath`, { method: "POST", headers: { ...json, Cookie: a }, body: JSON.stringify({ args: ["workspace"] }) });
    const ask = await asked;
    assert.equal(ask.kind, "path");

    const other = await fetch(`${s.base}${WEB_PATHS.answer}`, { method: "POST", headers: { ...json, Cookie: b }, body: JSON.stringify({ id: ask.id, value: "/evil" }) });
    assert.equal(other.status, 400, "another session cannot answer");
    const relative = await fetch(`${s.base}${WEB_PATHS.answer}`, { method: "POST", headers: { ...json, Cookie: a }, body: JSON.stringify({ id: ask.id, value: "srv" }) });
    assert.equal(relative.status, 400, "a path must be absolute");
    const ok = await fetch(`${s.base}${WEB_PATHS.answer}`, { method: "POST", headers: { ...json, Cookie: a }, body: JSON.stringify({ id: ask.id, value: "/srv/work" }) });
    assert.equal(ok.status, 200);
    assert.deepEqual(await (await call).json(), { result: { ok: true, data: { path: "/srv/work" } }, effects: [] });

    // Without an open stream the question is cancelled at once instead of hanging the call.
    const lonely = await fetch(`${s.base}${WEB_PATHS.invoke}pickPath`, { method: "POST", headers: { ...json, Cookie: b }, body: JSON.stringify({ args: ["workspace"] }) });
    assert.deepEqual(await lonely.json(), { result: { ok: true, data: { path: null } }, effects: [] });
  } finally {
    await s.close();
  }
});

test("web server: logins are throttled", async () => {
  const s = await startServer(generateKey());
  try {
    let last = 0;
    for (let i = 0; i < 7; i++) {
      last = (await fetch(`${s.base}${WEB_PATHS.login}`, { method: "POST", headers: json, body: JSON.stringify({ key: `guess-${i}` }) })).status;
    }
    assert.equal(last, 429);
    const info = await (await fetch(`${s.base}${WEB_PATHS.session}`)).json() as { retryAfter?: number };
    assert.ok((info.retryAfter ?? 0) > 0);
  } finally {
    await s.close();
  }
});
