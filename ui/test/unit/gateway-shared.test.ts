import assert from "node:assert/strict";
import { test } from "node:test";
import {
  buildListen,
  isAccessEntry,
  isGatewayHost,
  isIPv4,
  isIPv6,
  isIp,
  isListen,
  isSafeToken,
  listenPort,
  listenScope,
  parseCidr,
} from "../../src/shared/gateway";
import { parseGatewayCopy, parseGatewayUpdate } from "../../src/shared/ipc";

test("IPv4: four decimal octets 0..255, no leading zeros", () => {
  for (const ok of ["0.0.0.0", "192.168.1.20", "255.255.255.255", "10.0.0.1"]) assert.equal(isIPv4(ok), true, ok);
  for (const bad of ["", "1.2.3", "1.2.3.4.5", "256.1.1.1", "01.2.3.4", "1.2.3.-1", "1.2.3.4 ", "a.b.c.d", "1..2.3"]) {
    assert.equal(isIPv4(bad), false, bad);
  }
});

test("IPv6: full, compressed and embedded-IPv4 forms; zones and stray colons are refused", () => {
  for (const ok of ["::", "::1", "fe80::1", "2001:db8::", "2001:0db8:0000:0000:0000:ff00:0042:8329", "::ffff:192.0.2.1", "1:2:3:4:5:6:1.2.3.4", "1:2:3:4:5:6:7::"]) {
    assert.equal(isIPv6(ok), true, ok);
  }
  for (const bad of [":", ":::", "1::2::3", "12345::", "1:2:3:4:5:6:7:8:9", "1:2:3:4:5:6:7:8::", "fe80::1%eth0", ":1::", "1:::2", "::1.2.3", "gggg::1", "1:2:3:4:5:6:7"]) {
    assert.equal(isIPv6(bad), false, bad);
  }
  assert.equal(isIp("10.0.0.1"), true);
  assert.equal(isIp("example.com"), false);
});

test("CIDR: an address and a prefix within its family's bit count", () => {
  assert.deepEqual(parseCidr("10.0.0.0/8"), { ip: "10.0.0.0", prefix: 8 });
  assert.deepEqual(parseCidr("203.0.113.0/24"), { ip: "203.0.113.0", prefix: 24 });
  assert.deepEqual(parseCidr("fd00::/8"), { ip: "fd00::", prefix: 8 });
  assert.deepEqual(parseCidr("::/0"), { ip: "::", prefix: 0 });
  for (const bad of ["10.0.0.0/33", "10.0.0.0/", "10.0.0.0", "/8", "10.0.0.0/-1", "10.0.0.0/8/8", "fd00::/129", "300.0.0.0/8", "10.0.0.0/x", "example.com/8"]) {
    assert.equal(parseCidr(bad), undefined, bad);
  }
});

test("access entries: the three keywords, a single address, or a CIDR range", () => {
  for (const ok of ["localhost", "lan", "tailnet", "192.168.1.20", "fe80::1", "10.0.0.0/8"]) assert.equal(isAccessEntry(ok), true, ok);
  for (const bad of ["LAN", "any", "0.0.0.0/0x", "10.0.0.256", "", "lan/24", "*"]) assert.equal(isAccessEntry(bad), false, bad);
});

test("host names: DNS labels, not an IP address, not a mistyped address", () => {
  for (const ok of ["xmesh.home.example", "localhost", "win-dev-01", "a.b", "x".repeat(63) + ".example"]) {
    assert.equal(isGatewayHost(ok), true, ok);
  }
  for (const bad of [
    "192.168.1.1", // an address is not a host name
    "::1",
    "2001:db8::1",
    "1.2.3", // last label is all digits: a mistyped address
    "10",
    "-bad.example",
    "bad-.example",
    "under_score.example",
    "double..dot",
    "a".repeat(64) + ".example",
    "https://example.com",
    "example.com/path",
    "",
    "x".repeat(254),
  ]) {
    assert.equal(isGatewayHost(bad), false, bad);
  }
});

test("listen: the contract's forms only, with a port from 1 to 65535", () => {
  for (const ok of [":8080", "0.0.0.0:8080", "[::]:8080", "127.0.0.1:8080", "[::1]:8080", ":1", ":65535"]) assert.equal(isListen(ok), true, ok);
  for (const bad of ["0:8080", ":0", ":65536", "10.0.0.5:8080", "localhost:8080", "127.0.0.1", ":08080", "[fe80::1]:8080", ""]) {
    assert.equal(isListen(bad), false, bad);
  }
});

test("listen scope and port: all interfaces or loopback, and the canonical form for each", () => {
  assert.equal(listenScope(":8080"), "all");
  assert.equal(listenScope("0.0.0.0:8080"), "all");
  assert.equal(listenScope("[::]:8080"), "all");
  assert.equal(listenScope("127.0.0.1:8080"), "loopback");
  assert.equal(listenScope("[::1]:8080"), "loopback");
  assert.equal(listenScope("10.0.0.5:8080"), undefined);
  assert.equal(listenPort("127.0.0.1:9443"), "9443");
  assert.equal(listenPort("bogus"), undefined);
  assert.equal(buildListen("all", "8080"), ":8080");
  assert.equal(buildListen("loopback", "8080"), "127.0.0.1:8080");
});

test("tokens: base64url-ish, 8 to 512 characters, nothing a shell or a TOML string would read", () => {
  assert.equal(isSafeToken("abcdEFGH0123_-.~+/="), true);
  assert.equal(isSafeToken("x".repeat(512)), true);
  for (const bad of ["", "short", "x".repeat(513), "has space in it", 'quo"te-token', "dollar$token1", "back`tick`tok", "new\nline-token", "back\\slash-tok"]) {
    assert.equal(isSafeToken(bad), false, JSON.stringify(bad));
  }
});

test("gateway copy request: only the two fields, a known kind, a URL of bounded length", () => {
  assert.deepEqual(parseGatewayCopy({ kind: "codexConfig", url: "http://127.0.0.1:8080/mcp" }), { kind: "codexConfig", url: "http://127.0.0.1:8080/mcp" });
  for (const bad of [
    { kind: "meshLoginRead", url: "x" },
    { kind: "gatewayUrl" },
    { kind: "gatewayUrl", url: 1 },
    { kind: "gatewayUrl", url: "x", extra: true },
    { kind: "gatewayUrl", url: "x".repeat(513) },
    null,
    "gatewayUrl",
  ]) {
    assert.equal(parseGatewayCopy(bad), undefined, JSON.stringify(bad));
  }
});

test("gateway update: a whole reach replacement, validated entry by entry, with no duplicates", () => {
  const ok = { listen: ":8080", access: ["localhost", "lan", "tailnet", "10.0.0.0/8"], hosts: ["xmesh.home.example"] };
  assert.deepEqual(parseGatewayUpdate(ok), ok);
  assert.deepEqual(parseGatewayUpdate({ listen: "127.0.0.1:8080", access: [], hosts: [] }), { listen: "127.0.0.1:8080", access: [], hosts: [] });
  for (const bad of [
    { ...ok, listen: "0.0.0.0:0" },
    { ...ok, listen: "10.0.0.5:8080" },
    { ...ok, access: ["localhost", "localhost"] },
    { ...ok, access: ["nonsense"] },
    { ...ok, access: "lan" },
    { ...ok, hosts: ["192.168.1.1"] },
    { ...ok, hosts: ["xmesh.home.example", "XMESH.home.example", "a".repeat(64)] },
    { ...ok, hosts: ["xmesh.home.example", "xmesh.home.example"] },
    { ...ok, extra: 1 },
    { listen: ":8080", access: ["localhost"] },
    { listen: ":8080", access: new Array(65).fill("lan"), hosts: [] },
    null,
  ]) {
    assert.equal(parseGatewayUpdate(bad), undefined, JSON.stringify(bad).slice(0, 120));
  }
});
