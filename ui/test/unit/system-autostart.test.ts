import assert from "node:assert/strict";
import { test } from "node:test";
import { Autostart, type LoginItemApi } from "../../src/main/system/autostart";

type Settings = ReturnType<LoginItemApi["getLoginItemSettings"]>;

function fakeApi(initial: Settings): { api: LoginItemApi; calls: unknown[] } {
  let cur = initial;
  const calls: unknown[] = [];
  const api: LoginItemApi = {
    getLoginItemSettings: (o) => {
      calls.push({ get: o });
      return cur;
    },
    setLoginItemSettings: (s) => {
      calls.push({ set: s });
      cur = { ...cur, openAtLogin: s.openAtLogin, ...(cur.status !== undefined && { status: s.openAtLogin ? "enabled" : "not-registered" }) };
    },
  };
  return { api, calls };
}

const win = { platform: "win32" as const, packaged: true, execPath: "C:\\Program Files\\xmesh-ui\\xmesh-ui.exe" };
const mac = { platform: "darwin" as const, packaged: true, execPath: "/Applications/xmesh.app/Contents/MacOS/xmesh" };

test("Windows registers the exe path and reads it back with the same options", () => {
  const { api, calls } = fakeApi({ openAtLogin: false, executableWillLaunchAtLogin: false });
  const a = new Autostart(api, win);
  assert.deepEqual(a.get(), { enabled: false });
  assert.deepEqual(a.set(true), { enabled: false, hint: "disabled-by-user" });
  const opts = { path: win.execPath, args: [] };
  assert.deepEqual(calls[0], { get: opts });
  assert.deepEqual(calls[1], { set: { openAtLogin: true, ...opts } });
});

test("Windows: an entry the user switched off in Task Manager is reported as such", () => {
  const { api } = fakeApi({ openAtLogin: true, executableWillLaunchAtLogin: false });
  assert.deepEqual(new Autostart(api, win).get(), { enabled: false, hint: "disabled-by-user" });
  const ok = fakeApi({ openAtLogin: true, executableWillLaunchAtLogin: true });
  assert.deepEqual(new Autostart(ok.api, win).get(), { enabled: true });
});

test("macOS: SMAppService status maps to enabled / needs approval / off", () => {
  for (const [status, want] of [
    ["enabled", { enabled: true }],
    ["requires-approval", { enabled: false, hint: "requires-approval" }],
    ["not-registered", { enabled: false }],
    ["not-found", { enabled: false }],
  ] as const) {
    const { api } = fakeApi({ openAtLogin: status === "enabled", status });
    assert.deepEqual(new Autostart(api, mac).get(), want, status);
  }
  const { api, calls } = fakeApi({ openAtLogin: false, status: "not-registered" });
  assert.deepEqual(new Autostart(api, mac).set(true), { enabled: true });
  assert.deepEqual(calls.at(-2), { set: { openAtLogin: true } });
});

test("macOS: older systems without a status fall back to openAtLogin", () => {
  const { api } = fakeApi({ openAtLogin: true });
  assert.deepEqual(new Autostart(api, mac).get(), { enabled: true });
});

test("development runs and Linux never touch the system's login items", () => {
  for (const env of [{ ...win, packaged: false }, { ...mac, packaged: false }, { platform: "linux" as const, packaged: true, execPath: "/x" }]) {
    const { api, calls } = fakeApi({ openAtLogin: true });
    const a = new Autostart(api, env);
    assert.equal(a.get().enabled, false);
    assert.equal(a.set(true).enabled, false);
    assert.deepEqual(calls, []);
  }
  assert.equal(new Autostart(fakeApi({ openAtLogin: false }).api, { ...win, packaged: false }).get().hint, "unpackaged");
  assert.equal(new Autostart(fakeApi({ openAtLogin: false }).api, { platform: "linux", packaged: true, execPath: "/x" }).get().hint, "unsupported");
});
