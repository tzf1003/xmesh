import assert from "node:assert/strict";
import { test } from "node:test";
import type { GatewayInfo, PatInfo } from "../../src/shared/control";
import {
  ADDRESS_KIND_KEY,
  addressRows,
  checkAccessEntry,
  checkHostEntry,
  draftFrom,
  localhostUrl,
  requestFrom,
  sameReach,
  withScope,
} from "../../src/renderer/lib/gateway";
import { NOTICE_KEY_PREFIX, noticeDue, noticeKey, noticeStorage, readSeen, writeSeen, type NoticeStorage } from "../../src/renderer/lib/token-notice";
import { en, zh } from "../../src/renderer/messages";

const gw: GatewayInfo = {
  listen: ":8080",
  url: "http://127.0.0.1:8080/mcp",
  auth: "pat",
  access: ["localhost", "lan", "tailnet", "10.0.0.0/8"],
  hosts: ["xmesh.home.example"],
  addresses: [
    { kind: "localhost", host: "127.0.0.1", url: "http://127.0.0.1:8080/mcp" },
    { kind: "lan", host: "192.168.1.20", url: "http://192.168.1.20:8080/mcp" },
    { kind: "tailnet_ip", host: "100.101.102.103", url: "http://100.101.102.103:8080/mcp" },
    { kind: "tailnet_name", host: "win.tail.ts.net", url: "http://win.tail.ts.net:8080/mcp" },
    { kind: "host", host: "xmesh.home.example", url: "http://xmesh.home.example:8080/mcp" },
  ],
};

test("every address kind has a label in both tables", () => {
  for (const key of Object.values(ADDRESS_KIND_KEY)) {
    assert.ok(zh[key] && en[key], key);
  }
  assert.deepEqual(Object.keys(ADDRESS_KIND_KEY).sort(), ["host", "lan", "localhost", "tailnet_ip", "tailnet_name"]);
});

test("the rows are the kernel's addresses; an older kernel without them still shows localhost", () => {
  assert.equal(addressRows(gw).length, 5);
  assert.equal(localhostUrl(gw), "http://127.0.0.1:8080/mcp");
  const old = { ...gw, addresses: [] };
  assert.deepEqual(addressRows(old), [{ kind: "localhost", host: "127.0.0.1", url: gw.url }]);
  assert.equal(localhostUrl(old), gw.url);
});

test("the draft splits access into the boxes and the custom entries, and the request puts localhost back", () => {
  const d = draftFrom(gw);
  assert.deepEqual(d, { lan: true, tailnet: true, custom: ["10.0.0.0/8"], hosts: ["xmesh.home.example"], listen: ":8080" });
  assert.deepEqual(requestFrom(d), { listen: ":8080", access: ["localhost", "lan", "tailnet", "10.0.0.0/8"], hosts: ["xmesh.home.example"] });
  assert.deepEqual(requestFrom({ ...d, lan: false, tailnet: false, custom: [] }).access, ["localhost"], "localhost cannot be switched off");
  assert.equal(sameReach(d, draftFrom(gw)), true);
  assert.equal(sameReach(d, { ...d, tailnet: false }), false);
});

test("the listen toggle keeps the kernel's string until the scope actually changes", () => {
  const same = draftFrom({ ...gw, listen: "0.0.0.0:8080" });
  assert.equal(withScope(same, "all"), same, "already all interfaces: the string is kept");
  assert.equal(withScope(same, "loopback").listen, "127.0.0.1:8080");
  assert.equal(withScope(draftFrom({ ...gw, listen: "[::1]:9000" }), "all").listen, ":9000");
});

test("typed access entries: single addresses and CIDR ranges, no keywords, no duplicates", () => {
  assert.deepEqual(checkAccessEntry(" 192.168.1.5 ", []), { ok: true, value: "192.168.1.5" });
  assert.deepEqual(checkAccessEntry("fd00::/8", []), { ok: true, value: "fd00::/8" });
  assert.equal(checkAccessEntry("", []).ok, false);
  assert.deepEqual(checkAccessEntry("", []), { ok: false, problem: "empty" });
  assert.deepEqual(checkAccessEntry("192.168.1.256", []), { ok: false, problem: "invalid" });
  assert.deepEqual(checkAccessEntry("10.0.0.0/33", []), { ok: false, problem: "invalid" });
  assert.deepEqual(checkAccessEntry("lan", []), { ok: false, problem: "invalid" }, "keywords are ticked boxes");
  assert.deepEqual(checkAccessEntry("FD00::1", ["fd00::1"]), { ok: false, problem: "duplicate" });
});

test("typed host names: DNS names only, IP addresses refused", () => {
  assert.deepEqual(checkHostEntry("xmesh.home.example", []), { ok: true, value: "xmesh.home.example" });
  assert.deepEqual(checkHostEntry("192.168.1.1", []), { ok: false, problem: "invalid" });
  assert.deepEqual(checkHostEntry("fe80::1", []), { ok: false, problem: "invalid" });
  assert.deepEqual(checkHostEntry("Xmesh.Home.Example", ["xmesh.home.example"]), { ok: false, problem: "duplicate" });
  assert.equal(checkHostEntry("   ", []).ok, false);
});

// --- the one-time notice, with storage that works, is absent, or throws ---

function memoryStorage(initial: Record<string, string> = {}): NoticeStorage & { data: Record<string, string> } {
  const data = { ...initial };
  return {
    data,
    getItem: (k) => (k in data ? data[k]! : null),
    setItem: (k, v) => {
      data[k] = v;
    },
  };
}

const throwing: NoticeStorage = {
  getItem: () => {
    throw new Error("SecurityError: storage disabled");
  },
  setItem: () => {
    throw new Error("QuotaExceededError");
  },
};

const pat = (modified_at: string | null): PatInfo => ({ name: "default", scopes: ["devices:read"], token_file: "/x/default.pat", modified_at });

test("the notice is due once per modified_at of the default PAT", () => {
  const at = "2026-10-17T01:00:00.000Z";
  assert.equal(noticeDue(pat(at), undefined), true, "first launch after the upgrade");
  assert.equal(noticeDue(pat(at), at), false, "dismissed at this modified_at");
  assert.equal(noticeDue(pat("2026-10-18T09:00:00.000Z"), at), true, "rotated since: due again");
  assert.equal(noticeDue(pat(null), undefined), false, "no token file yet");
  assert.equal(noticeDue(undefined, undefined), false, "no default PAT");
});

test("dismissing stores the modified_at under a per-PAT key, and it is read back", () => {
  const store = memoryStorage();
  const at = "2026-10-17T01:00:00.000Z";
  assert.equal(writeSeen(store, "default", at), true);
  assert.equal(store.data[`${NOTICE_KEY_PREFIX}default`], at);
  assert.equal(noticeKey("default"), "xmesh.gatewayTokenNotice.default");
  assert.equal(readSeen(store, "default"), at);
  assert.equal(readSeen(store, "other"), undefined);
});

test("with storage that throws or is missing, nothing crashes and the notice is due until dismissed", () => {
  assert.equal(readSeen(throwing, "default"), undefined);
  assert.equal(writeSeen(throwing, "default", "t"), false);
  assert.equal(readSeen(undefined, "default"), undefined);
  assert.equal(writeSeen(undefined, "default", "t"), false);
  assert.equal(noticeDue(pat("t"), readSeen(throwing, "default")), true);
});

test("the storage accessor itself may throw: the page then has no storage at all", () => {
  const g = globalThis as { localStorage?: unknown };
  const had = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    get() {
      throw new Error("SecurityError: access is denied for this document");
    },
  });
  try {
    assert.equal(noticeStorage(), undefined);
  } finally {
    if (had) Object.defineProperty(globalThis, "localStorage", had);
    else delete g.localStorage;
  }
  assert.equal(noticeStorage(), undefined, "no localStorage in node at all");
});
