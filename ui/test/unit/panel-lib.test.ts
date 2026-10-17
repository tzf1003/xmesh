import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { test } from "node:test";
import { type ConfigChange, type Peer, type StatusResponse } from "../../src/shared/control";
import { en, zh } from "../../src/renderer/messages";
import { EMPTY_AUDIT_FORM, auditTarget, buildAuditQuery, toTimestamp } from "../../src/renderer/lib/audit";
import { authzRows, cwdPolicy, diffRows, lineRange, needsRestart, weakening } from "../../src/renderer/lib/config";
import { deviceInfoState, lacksDeviceTag, relayStats, sortPeers, worstWarning } from "../../src/renderer/lib/devices";
import { ago, formatDuration, formatIdle, formatTime, formatValue, fraction } from "../../src/renderer/lib/format";
import { fingerprintMatches, groupFingerprint, normalizeFingerprint, pairFailure, pairPasswordValid, parsePairText } from "../../src/renderer/lib/pair";
import { checkScopes, isBroadScope, splitScopes } from "../../src/renderer/lib/scopes";
import { INITIAL_FORM, checkForm } from "../../src/renderer/lib/setup";

const FP = "ab".repeat(32);
const PEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n";

test("zh and en have the same keys and placeholders, and no empty strings", () => {
  const holders = (s: string): string[] => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]!).sort();
  assert.deepEqual(Object.keys(en).sort(), Object.keys(zh).sort());
  for (const [k, v] of Object.entries(zh)) {
    const e = (en as Record<string, string>)[k]!;
    assert.ok(String(v).length > 0 && e.length > 0, k);
    assert.deepEqual(holders(e), holders(v), k);
  }
});

test("the English table contains no Chinese text", () => {
  for (const [k, v] of Object.entries(en)) assert.doesNotMatch(v, /[一-鿿]/, k);
});

test("scopes are split on commas and whitespace, deduplicated and validated", () => {
  assert.deepEqual(splitScopes("devices:read, exec:*\nexec:*  pty:*，fs:*"), ["devices:read", "exec:*", "pty:*", "fs:*"]);
  assert.deepEqual(checkScopes("devices:read root exec:*").invalid, ["root"]);
  assert.equal(isBroadScope("exec:*"), true);
  assert.equal(isBroadScope("backend:*:*"), true);
  assert.equal(isBroadScope("devices:read"), false);
  assert.equal(isBroadScope("exec:shell"), false);
});

test("pair text parsing accepts the bare document and the export response, nothing else", () => {
  const doc = { name: "p1", fingerprint: FP, certificate_pem: PEM, tags: ["tag:peer"] };
  assert.ok(parsePairText(JSON.stringify(doc)).ok);
  assert.ok(parsePairText(JSON.stringify({ pair: doc })).ok);
  for (const t of ["", "not json", "[]", "null", JSON.stringify({ ...doc, certificate_pem: "x" }), JSON.stringify({ ...doc, admin: 1 })]) {
    assert.equal(parsePairText(t).ok, false, t);
  }
});

test("fingerprints compare ignoring case, colons and spaces, and must be 32 bytes", () => {
  assert.equal(normalizeFingerprint("AB:cd ef"), "abcdef");
  assert.equal(groupFingerprint("ABCD"), "ab:cd");
  assert.equal(fingerprintMatches(groupFingerprint(FP).toUpperCase(), FP), true);
  assert.equal(fingerprintMatches(FP.slice(0, 62), FP.slice(0, 62)), false, "too short even if equal");
  assert.equal(fingerprintMatches("cd".repeat(32), FP), false);
});

test("audit forms become queries; blanks are dropped and times are UTC", () => {
  assert.deepEqual(buildAuditQuery(EMPTY_AUDIT_FORM), { limit: "100" });
  const q = buildAuditQuery({ ...EMPTY_AUDIT_FORM, since: "2026-10-16T08:00", event: "tool_call", tool: " git ", status: "error" }, 50, "cur");
  assert.equal(q.since, new Date("2026-10-16T08:00").toISOString());
  assert.deepEqual({ ...q, since: undefined }, { limit: "50", cursor: "cur", event: "tool_call", tool: "git", status: "error", since: undefined });
  assert.equal(toTimestamp("garbage"), undefined);
  assert.equal(auditTarget({ args_digest: { cmd: "ls", resolved_path: "/w" } } as never), "ls  /w");
});

test("config diff rows, weakening and restart flags", () => {
  const changes: ConfigChange[] = [
    { pointer: "/agent/allow_dangerous", op: "replace", before: false as never, after: true as never, restart_required: false, weakens_security: true },
    { pointer: "/gateway/listen", op: "add", after: "0.0.0.0:8080" as never, restart_required: true, weakens_security: false },
    { pointer: "", op: "remove", before: { a: 1 }, restart_required: false, weakens_security: false },
  ];
  const rows = diffRows(changes);
  assert.deepEqual(rows[0], { pointer: "/agent/allow_dangerous", op: "replace", before: "false", after: "true", restartRequired: false, weakens: true });
  assert.equal(rows[1]!.before, "");
  assert.equal(rows[2]!.pointer, "/");
  assert.equal(rows[2]!.after, "");
  assert.equal(weakening(changes).length, 1);
  assert.equal(needsRestart(changes), true);
  assert.equal(needsRestart([]), false);
});

test("authz and cwd policy rows tolerate odd shapes", () => {
  const parsed = { agent: { authz: { "tag:peer": { tools: ["*"], backends: ["git", 3] } }, cwd_policy: { allow: ["/w"], deny: "x" } } };
  assert.deepEqual(authzRows(parsed), [{ tag: "tag:peer", tools: ["*"], backends: ["git"] }]);
  assert.deepEqual(cwdPolicy(parsed), { allow: ["/w"], deny: [] });
  for (const odd of [null, undefined, 3, {}, { agent: { authz: [] } }, { agent: { authz: "x" } }]) {
    assert.deepEqual(authzRows(odd), []);
  }
  assert.deepEqual(cwdPolicy(null), { allow: [], deny: [] });
});

test("lineRange locates 1-based lines for diagnostics", () => {
  const text = "a\nbb\nccc";
  assert.deepEqual(lineRange(text, 1), { start: 0, end: 1 });
  assert.deepEqual(lineRange(text, 2), { start: 2, end: 4 });
  assert.deepEqual(lineRange(text, 3), { start: 5, end: 8 });
  assert.equal(lineRange(text, 4), undefined);
  assert.equal(lineRange(text, 0), undefined);
  assert.equal(lineRange(text, 1.5), undefined);
});

test("formatting helpers", () => {
  assert.equal(formatDuration(842), "842 ms");
  assert.equal(formatDuration(1200), "1.2 s");
  assert.equal(formatDuration(184_000), "3 m 04 s");
  assert.equal(formatDuration(-1), "—");
  assert.equal(formatIdle(12), "12 s");
  assert.equal(formatIdle(300), "5 m");
  assert.equal(formatIdle(7300), "2 h");
  assert.equal(fraction(5, 10), 0.5);
  assert.equal(fraction(50, 10), 1);
  assert.equal(fraction(1, 0), 0);
  assert.equal(formatTime(null), "—");
  assert.equal(formatTime("nope"), "nope");
  assert.match(formatTime("2026-10-16T01:02:03Z"), /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
  const now = Date.parse("2026-10-16T12:00:00Z");
  assert.deepEqual(ago("2026-10-16T11:59:30Z", now), { n: 30, unit: "s" });
  assert.deepEqual(ago("2026-10-16T10:00:00Z", now), { n: 2, unit: "h" });
  assert.deepEqual(ago("2026-10-13T12:00:00Z", now), { n: 3, unit: "d" });
  assert.equal(ago("bad", now), undefined);
  assert.equal(formatValue("x".repeat(500)).length, 120);
  assert.equal(formatValue(undefined), "");
});

test("peer ordering and statistics", () => {
  const p = (name: string, o: Partial<Peer>): Peer => ({ name, self: false, online: true, tags: ["tag:device"], connection: "direct", ...o }) as Peer;
  const peers = [p("b", { online: false }), p("c", { connection: "derp" }), p("self", { self: true }), p("a", { connection: "unknown" as never })];
  assert.deepEqual(sortPeers(peers).map((x) => x.name), ["self", "a", "c", "b"]);
  assert.deepEqual(relayStats(peers), { direct: 1, derp: 1, unknown: 1 });
  assert.equal(lacksDeviceTag(p("x", { tags: ["tag:peer"] })), true);
  assert.equal(lacksDeviceTag(p("x", { self: true, tags: [] })), false);
  const st = { warnings: [{ severity: "info", code: "i" }, { severity: "error", code: "e" }, { severity: "warning", code: "w" }] } as unknown as StatusResponse;
  assert.equal(worstWarning(st)?.code, "e");
});

test("device info state: pending before the first read, failed when the latest read failed, untracked otherwise", () => {
  const p = (o: Partial<Peer>): Peer => ({ name: "d", self: false, online: true, tags: ["tag:device"], connection: "direct", ...o }) as Peer;
  assert.equal(deviceInfoState(p({ info: { refreshed_at: null, error: null } })), "pending");
  assert.equal(deviceInfoState(p({ info: { refreshed_at: null, error: "拨号失败" } })), "failed");
  assert.equal(deviceInfoState(p({ info: { refreshed_at: "2026-10-17T06:00:00.000Z", error: "sys.info 暂不可用" } })), "failed");
  assert.equal(deviceInfoState(p({ info: { refreshed_at: "2026-10-17T06:00:00.000Z", error: null } })), "ok");
  // not tracked: no info key (an older kernel, a node without a gateway), this node, or an offline device
  assert.equal(deviceInfoState(p({})), undefined);
  assert.equal(deviceInfoState(p({ self: true, info: { refreshed_at: null, error: null } })), undefined);
  assert.equal(deviceInfoState(p({ online: false, info: { refreshed_at: null, error: null } })), undefined);
});

test("wizard form check mirrors the main-process validation and previews the exact file", () => {
  const bad = checkForm(INITIAL_FORM);
  assert.deepEqual(bad, { ok: false, problem: "node_name" });
  const good = checkForm({ ...INITIAL_FORM, nodeName: " dev-1 ", workspace: "/w" });
  assert.ok(good.ok);
  assert.equal(good.params.nodeName, "dev-1");
  assert.match(good.preview, /name: "dev-1"/);
  assert.deepEqual(checkForm({ ...INITIAL_FORM, nodeName: "n", agent: false, gateway: false }), { ok: false, problem: "roles" });
  assert.deepEqual(checkForm({ ...INITIAL_FORM, nodeName: "n", workspace: "/w", mode: "tailnet" }), { ok: false, problem: "authkey_file" });
});

test("the renderer bundle contains no dynamic code, raw HTML sinks or network APIs", () => {
  const file = path.join(__dirname, "..", "..", "..", "dist", "renderer", "app.js");
  const js = fs.readFileSync(file, "utf8");
  for (const [name, re] of [
    ["eval", /\beval\s*\(/],
    ["new Function", /new\s+Function\s*\(/],
    ["document.write", /document\.write\s*\(/],
    ["XMLHttpRequest", /XMLHttpRequest/],
    ["WebSocket", /new\s+WebSocket/],
    ["fetch", /(?<![.\w])fetch\s*\(/],
    ["importScripts", /importScripts/],
    ["sendBeacon", /sendBeacon/],
  ] as const) {
    assert.doesNotMatch(js, re, name);
  }
  // Preact has one innerHTML sink for dangerouslySetInnerHTML; our sources must not use it.
  const srcDir = path.join(__dirname, "..", "..", "..", "src", "renderer");
  const sources = (dir: string): string[] =>
    fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? sources(path.join(dir, e.name)) : [path.join(dir, e.name)]));
  for (const f of sources(srcDir).filter((x) => /\.tsx?$/.test(x))) {
    const text = fs.readFileSync(f, "utf8");
    assert.doesNotMatch(text, /dangerouslySetInnerHTML|innerHTML|outerHTML|insertAdjacentHTML/, f);
    assert.doesNotMatch(text, /localStorage|sessionStorage|document\.cookie/, f);
  }
});

test("pairing failures are worded by reason; a rate limit says how long to wait", () => {
  assert.deepEqual(pairFailure({ code: "pair_rejected", message: "x", reason: "wrong_password" }), { key: "pair.err.wrongPassword" });
  assert.deepEqual(pairFailure({ code: "pair_rejected", message: "x", reason: "unreachable" }), { key: "pair.err.unreachable" });
  assert.equal(pairFailure({ code: "pair_rejected", message: "x", reason: "something_new" }), undefined);
  assert.deepEqual(pairFailure({ code: "rate_limited", message: "x", retry_after_ms: 30000 }), { key: "pair.err.rateLimited", params: { seconds: 30 } });
  assert.deepEqual(pairFailure({ code: "rate_limited", message: "x", retry_after_ms: 1 }), { key: "pair.err.rateLimited", params: { seconds: 1 } });
  assert.deepEqual(pairFailure({ code: "invalid_args", message: "x", field: "password" }), { key: "pair.err.passwordRules" });
  assert.deepEqual(pairFailure({ code: "invalid_args", message: "x", field: "fingerprint", reason: "fingerprint_mismatch" }), { key: "pair.err.fingerprint" });
  assert.deepEqual(pairFailure({ code: "not_found", message: "x" }), { key: "pair.err.notFound" });
  assert.equal(pairFailure({ code: "internal", message: "x" }), undefined);
});

test("the connection password the user types is checked before it is sent", () => {
  assert.ok(pairPasswordValid("correct horse"));
  assert.ok(!pairPasswordValid("too short"));
  assert.ok(!pairPasswordValid(" correct horse "));
});
