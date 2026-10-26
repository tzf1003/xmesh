import assert from "node:assert/strict";
import { test } from "node:test";
import { parsePreferredIpRequest, PREFERRED_IP_TEXT_MAX } from "../../src/shared/preferred-ip";
import { blankSource, defaultPolicy, durationSeconds, parseIps, policyProblem, policyValue, readPolicy, samePolicy } from "../../src/renderer/lib/preferred-ip";

const ID = `dev-${"a".repeat(64)}`;

test("the request parser takes only the three operations with exactly their fields", () => {
  assert.deepEqual(parsePreferredIpRequest({ op: "get" }), { op: "get" });
  assert.deepEqual(parsePreferredIpRequest({ op: "run", task: "scan" }), { op: "run", task: "scan" });
  assert.deepEqual(parsePreferredIpRequest({ op: "run", task: "refresh" }), { op: "run", task: "refresh" });
  assert.deepEqual(parsePreferredIpRequest({ op: "inspect", text: "104.16.0.1\n" }), { op: "inspect", text: "104.16.0.1\n" });
  for (const bad of [null, [], "get", { op: "get", extra: 1 }, { op: "run" }, { op: "run", task: "stop" }, { op: "inspect" }, { op: "inspect", text: 5 }, { op: "inspect", text: "x".repeat(PREFERRED_IP_TEXT_MAX + 1) }, { op: "other" }]) {
    assert.equal(parsePreferredIpRequest(bad), undefined, JSON.stringify(bad));
  }
});

test("durations read back as seconds", () => {
  assert.equal(durationSeconds("4s"), 4);
  assert.equal(durationSeconds("30m"), 1800);
  assert.equal(durationSeconds("1h30m"), 5400);
  assert.equal(durationSeconds("soon"), undefined);
  assert.equal(durationSeconds(""), undefined);
});

test("an empty configuration reads as the defaults and writes nothing", () => {
  const loaded = readPolicy({ schema_version: 2, mesh: { cloudflare: { enabled: true } } });
  assert.equal(loaded.supported, true);
  assert.equal(loaded.cloudflareEnabled, true);
  assert.deepEqual(loaded.policy, defaultPolicy());
  assert.deepEqual(policyValue(loaded.policy), {});
  assert.equal(readPolicy({ schema_version: 1 }).supported, false);
  assert.equal(readPolicy(null).cloudflareEnabled, false);
});

test("a full configuration round-trips through the draft", () => {
  const value = {
    enabled: false,
    mode: "fixed",
    fixed_ips: ["104.16.0.1"],
    attempt_timeout: "6s",
    fail_threshold: 5,
    cooldown: "45m",
    no_dns_fallback: true,
    sources: [
      { type: "manual", name: "自己常用", ips: ["104.16.0.2"] },
      { type: "file", disabled: true, path: "/etc/xmesh/ips.csv" },
      { type: "url", url: "https://example.com/ips.txt", refresh: "6h" },
    ],
    scan: { enabled: true, interval: "12h", sample: 300, keep: 4, ipv6: true, host: "win.link.example.com" },
    peers: { [ID]: { mode: "fixed", ips: ["172.64.0.9"] } },
  };
  const cfg = { schema_version: 2, mesh: { cloudflare: { enabled: true, peers: { [ID]: "win.link.example.com" }, preferred_ips: ["104.16.0.9"], preferred_ip: value } } };
  const loaded = readPolicy(cfg);
  assert.deepEqual(policyValue(loaded.policy), value);
  assert.deepEqual(loaded.legacyIps, ["104.16.0.9"]);
  assert.equal(loaded.peers[ID], "win.link.example.com");
  assert.equal(loaded.policy.cooldownMinutes, 45);
  assert.equal(loaded.policy.attemptSeconds, 6);
});

test("peers that follow the default are not written, and unknown values fall back", () => {
  const p = defaultPolicy();
  p.peers[ID] = { mode: "default", ips: [] };
  assert.deepEqual(policyValue(p), {});
  const loaded = readPolicy({ schema_version: 2, mesh: { cloudflare: { preferred_ip: { mode: "round-robin", attempt_timeout: "x", sources: [{ type: "dns" }], peers: { [ID]: { mode: "weird" } } } } } });
  assert.equal(loaded.policy.mode, "smart");
  assert.equal(loaded.policy.attemptSeconds, 4);
  assert.equal(loaded.policy.sources[0]?.type, "manual");
  assert.equal(loaded.policy.peers[ID]?.mode, "default");
});

test("a draft equal to the saved settings is not dirty, and a change is", () => {
  const a = defaultPolicy();
  const b = defaultPolicy();
  assert.equal(samePolicy(a, b), true);
  b.scan.enabled = true;
  assert.equal(samePolicy(a, b), false);
});

test("pasted addresses split on whitespace, commas and semicolons, without repeats", () => {
  assert.deepEqual(parseIps("104.16.0.1, 104.16.0.2;104.16.0.1\n\n  172.64.0.9 "), ["104.16.0.1", "104.16.0.2", "172.64.0.9"]);
  assert.deepEqual(parseIps(""), []);
});

test("what cannot be saved is named", () => {
  const p = defaultPolicy();
  assert.equal(policyProblem(p), undefined);
  p.mode = "fixed";
  assert.equal(policyProblem(p), "fixed");
  p.fixedIps = ["104.16.0.1"];
  assert.equal(policyProblem(p), undefined);
  p.sources = [{ ...blankSource("url"), url: "http://example.com/x" }];
  assert.equal(policyProblem(p), "url");
  p.sources = [{ ...blankSource("url"), url: "https://example.com/x" }, blankSource("file")];
  assert.equal(policyProblem(p), "file");
  p.sources = [];
  p.peers[ID] = { mode: "fixed", ips: [] };
  assert.equal(policyProblem(p), "peer");
  p.peers = {};
  p.sources = Array.from({ length: 9 }, () => blankSource("manual"));
  assert.equal(policyProblem(p), "sources");
});
