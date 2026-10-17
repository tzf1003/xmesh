import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { EventEmitter } from "node:events";
import { test } from "node:test";
import { Actions, type Platform, checkOpenablePath } from "../../src/main/actions";
import { clipboardText, isSafeGatewayUrl } from "../../src/main/connect";
import type { ShellController } from "../../src/main/controller";
import { translator } from "../../src/shared/i18n";
import { ControlRequestError } from "../../src/shared/control";
import type { StatusResponse } from "../../src/shared/control";
import { EMPTY_SNAPSHOT, type Snapshot } from "../../src/shared/state";
import { peersFixture, statusWith } from "../../testserver/fixtures";

const t = translator("zh");

function fakeController(snapshot: Snapshot, overrides: Partial<Record<"drain" | "resume", () => Promise<void>>> = {}) {
  const calls: string[] = [];
  const c = Object.assign(new EventEmitter(), {
    snapshot,
    drain: overrides.drain ?? (async () => void calls.push("drain")),
    resume: overrides.resume ?? (async () => void calls.push("resume")),
    selectInstance: (id: string) => void calls.push(`select:${id}`),
    setLoginItem: (v: boolean) => void calls.push(`login:${v}`),
  });
  return { controller: c as unknown as ShellController, calls };
}

function fakePlatform(confirm = true) {
  const log: string[] = [];
  const platform: Platform = {
    writeClipboard: (s) => void log.push(`clip:${s}`),
    openPath: async (p) => {
      log.push(`open:${p}`);
      return "";
    },
    openExternal: async (u) => void log.push(`ext:${u}`),
    confirm: async () => {
      log.push("confirm");
      return confirm;
    },
    showPanel: () => void log.push("panel"),
    quit: () => void log.push("quit"),
    runCli: async (c) => void log.push(`cli:${c}`),
    notifyError: (m) => void log.push(`err:${m}`),
    pickPath: async () => undefined,
  };
  return { platform, log };
}

const connected = (patch: Partial<StatusResponse> = {}): Snapshot => ({
  ...EMPTY_SNAPSHOT,
  instances: [{ id: "n1", label: "laptop" }],
  connection: { kind: "connected", status: statusWith(patch), peers: peersFixture() },
});

test("copy writes the gateway address and ready-made connect text", async () => {
  const { controller } = fakeController(connected({ gateway: { listen: "x", auth: "none", url: "http://127.0.0.1:8080/mcp" } }));
  const { platform, log } = fakePlatform();
  const a = new Actions(controller, platform, t);
  assert.deepEqual(await a.copy("gatewayUrl"), { ok: true });
  assert.deepEqual(await a.copy("claudeCodeCommand"), { ok: true });
  assert.deepEqual(await a.copy("codexConfig"), { ok: true });
  assert.deepEqual(log, [
    "clip:http://127.0.0.1:8080/mcp",
    "clip:claude mcp add --transport http xmesh http://127.0.0.1:8080/mcp",
    'clip:[mcp_servers.xmesh]\nurl = "http://127.0.0.1:8080/mcp"\n',
  ]);
});

test("copy of connect text leaves a token placeholder when the gateway requires a PAT", async () => {
  const { controller } = fakeController(connected({ gateway: { listen: "x", auth: "pat", url: "http://127.0.0.1:8080/mcp" } }));
  const { platform, log } = fakePlatform();
  const a = new Actions(controller, platform, t);
  assert.deepEqual(await a.copy("gatewayUrl"), { ok: true });
  assert.deepEqual(await a.copy("claudeCodeCommand"), { ok: true });
  assert.deepEqual(await a.copy("codexConfig"), { ok: true });
  assert.deepEqual(log, [
    "clip:http://127.0.0.1:8080/mcp",
    'clip:claude mcp add --transport http xmesh http://127.0.0.1:8080/mcp --header "Authorization: Bearer <PAT>"',
    'clip:[mcp_servers.xmesh]\nurl = "http://127.0.0.1:8080/mcp"\nbearer_token_env_var = "XMESH_PAT"\n',
  ]);
});

test("copy refuses when there is no gateway, when offline, or when the URL is not plain", async () => {
  const noGateway = connected();
  if (noGateway.connection.kind === "connected") delete noGateway.connection.status.gateway;
  for (const snap of [EMPTY_SNAPSHOT, noGateway, connected({ gateway: { listen: "x", auth: "none", url: 'http://x/"; rm -rf ~; "' } })]) {
    const { controller } = fakeController(snap);
    const { platform, log } = fakePlatform();
    const r = await new Actions(controller, platform, t).copy("claudeCodeCommand");
    assert.equal(r.ok, false);
    assert.deepEqual(log, []);
  }
});

test("clipboard text only embeds plain http(s) URLs", () => {
  assert.equal(isSafeGatewayUrl("https://node.tailnet.ts.net/mcp"), true);
  assert.equal(isSafeGatewayUrl("http://127.0.0.1:7433/mcp"), true);
  for (const bad of ["javascript:alert(1)", "http://x y", "http://x/$(id)", "http://x/`id`", "http://x/\"", "http://x/'", "ftp://x", "", "http://x/\n"]) {
    assert.equal(isSafeGatewayUrl(bad), false, bad);
    assert.equal(clipboardText("claudeCodeCommand", bad), undefined);
  }
});

test("openPath checks the path first", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-act-"));
  try {
    const cfg = path.join(dir, "xmesh.yaml");
    const exe = path.join(dir, "payload.exe");
    fs.writeFileSync(cfg, "");
    fs.writeFileSync(exe, "");
    assert.equal(checkOpenablePath("config", cfg), "");
    assert.equal(checkOpenablePath("logs", dir), "");
    assert.notEqual(checkOpenablePath("config", exe), "", "executables are not opened as config");
    assert.notEqual(checkOpenablePath("config", dir), "", "config must be a file");
    assert.notEqual(checkOpenablePath("logs", cfg), "", "logs must be a directory");
    assert.notEqual(checkOpenablePath("logs", "relative/dir"), "");
    assert.notEqual(checkOpenablePath("logs", path.join(dir, "missing")), "");

    const { controller } = fakeController(connected({ paths: { config_file: cfg, state_dir: dir, log_dir: dir, audit_dir: exe } }));
    const { platform, log } = fakePlatform();
    const a = new Actions(controller, platform, t);
    assert.deepEqual(await a.openPath("config"), { ok: true });
    assert.deepEqual(await a.openPath("logs"), { ok: true });
    assert.equal((await a.openPath("audit")).ok, false);
    assert.deepEqual(log, [`open:${cfg}`, `open:${dir}`]);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("stopping the kernel runs the CLI only after a yes", async () => {
  for (const [answer, expected] of [
    [false, ["confirm"]],
    [true, ["confirm", "cli:stop"]],
  ] as const) {
    const { controller } = fakeController(connected());
    const { platform, log } = fakePlatform(answer);
    await new Actions(controller, platform, t).dispatch({ type: "stop-kernel" });
    assert.deepEqual(log, expected);
  }
});

test("restart runs the CLI without asking, and a CLI failure is reported not thrown", async () => {
  const { controller } = fakeController(connected());
  const { platform, log } = fakePlatform();
  platform.runCli = async () => {
    throw new Error("exit 1");
  };
  const r = await new Actions(controller, platform, t).dispatch({ type: "restart-kernel" });
  assert.equal(r.ok, false);
  assert.deepEqual(log, ["err:命令执行失败：xmesh restart"]);
});

test("restart and stop name the selected node's service, so the CLI does not read ./xmesh.yaml", async () => {
  const seen: (string | undefined)[] = [];
  const { controller } = fakeController({ ...connected(), selectedInstance: "office-pc" });
  const { platform } = fakePlatform();
  platform.runCli = async (_command, node) => {
    seen.push(node);
  };
  const actions = new Actions(controller, platform, t);
  await actions.dispatch({ type: "restart-kernel" });
  await actions.dispatch({ type: "stop-kernel" });
  assert.deepEqual(seen, ["office-pc", "office-pc"]);
});

test("start runs the CLI for a stopped node only; any other name is refused without running anything", async () => {
  const seen: string[] = [];
  const { controller } = fakeController({ ...EMPTY_SNAPSHOT, stoppedNodes: ["beta2-mac"] });
  const { platform } = fakePlatform();
  platform.runCli = async (command, node) => {
    seen.push(`${command}:${node}`);
  };
  const actions = new Actions(controller, platform, t);
  assert.deepEqual(await actions.dispatch({ type: "start-kernel", node: "beta2-mac" }), { ok: true });
  assert.deepEqual(await actions.startKernel("office-pc"), { ok: false, error: { code: "not_found", message: "no stopped node with that name" } });
  assert.deepEqual(seen, ["start:beta2-mac"]);
});

test("kernel errors become results with the kernel's code", async () => {
  const { controller } = fakeController(connected(), {
    drain: async () => {
      throw new ControlRequestError("conflict", "already draining", 409);
    },
  });
  const r = await new Actions(controller, fakePlatform().platform, t).drain();
  assert.deepEqual(r, { ok: false, error: { code: "conflict", message: "already draining" } });
});

test("selectInstance only accepts instances that exist", async () => {
  const { controller, calls } = fakeController(connected());
  const a = new Actions(controller, fakePlatform().platform, t);
  assert.equal((await a.selectInstance("ghost")).ok, false);
  assert.deepEqual(await a.selectInstance("n1"), { ok: true });
  assert.deepEqual(calls, ["select:n1"]);
});

test("openExternal resolves the key to the fixed URL", async () => {
  const { controller } = fakeController(connected());
  const { platform, log } = fakePlatform();
  await new Actions(controller, platform, t).openExternal("docs");
  assert.deepEqual(log, ["ext:https://github.com/tzf1003/xmesh#readme"]);
});
