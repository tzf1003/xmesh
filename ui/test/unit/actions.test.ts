import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { EventEmitter } from "node:events";
import { test } from "node:test";
import { Actions, type Platform, checkOpenablePath } from "../../src/main/actions";
import { isSafeGatewayUrl } from "../../src/main/connect";
import type { ShellController } from "../../src/main/controller";
import { translator } from "../../src/shared/i18n";
import { ControlRequestError } from "../../src/shared/control";
import type { CiBinding, StatusResponse } from "../../src/shared/control";
import { EMPTY_SNAPSHOT, type Snapshot } from "../../src/shared/state";
import { fixture, peersFixture, statusWith } from "../../testserver/fixtures";

const t = translator("zh");
const TOKEN = "tok-0123456789abcdefABCDEF";
const OK_CREDENTIALS = { read: async () => TOKEN };
const NO_CREDENTIALS = {
  read: async () => {
    throw new Error("gateway token unavailable: the kernel has no PAT");
  },
};

function fakeController(snapshot: Snapshot, overrides: Partial<Record<"drain" | "resume", () => Promise<void>>> = {}) {
  const calls: string[] = [];
  const c = Object.assign(new EventEmitter(), {
    snapshot,
    drain: overrides.drain ?? (async () => void calls.push("drain")),
    resume: overrides.resume ?? (async () => void calls.push("resume")),
    selectInstance: (id: string) => void calls.push(`select:${id}`),
    nudge: () => void calls.push("nudge"),
    setLoginItem: (v: boolean) => void calls.push(`login:${v}`),
  });
  return { controller: c as unknown as ShellController, calls };
}

/** A gateway as the kernel reports it: a localhost address, plus whatever extra addresses the test lists. */
function gatewayWith(extra: { kind: "lan" | "host"; host: string; url: string }[] = [], url = "http://127.0.0.1:8080/mcp") {
  return {
    listen: ":8080",
    url,
    auth: "pat" as const,
    access: ["localhost", "lan"],
    hosts: [],
    addresses: [{ kind: "localhost" as const, host: "127.0.0.1", url }, ...extra],
  };
}

function connectedController() {
  return fakeController(connected({ gateway: gatewayWith() })).controller;
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
    removeStaleState: async (n) => void log.push(`remove:${n}`),
    pickPath: async () => undefined,
  };
  return { platform, log };
}

const connected = (patch: Partial<StatusResponse> = {}): Snapshot => ({
  ...EMPTY_SNAPSHOT,
  instances: [{ id: "n1", label: "laptop" }],
  connection: { kind: "connected", status: statusWith(patch), peers: peersFixture() },
});

test("copy writes each gateway address with the bearer token in every connect format", async () => {
  const { controller } = fakeController(connected({ gateway: gatewayWith() }));
  const { platform, log } = fakePlatform();
  const a = new Actions(controller, platform, t, OK_CREDENTIALS);
  assert.deepEqual(await a.copy("gatewayUrl"), { ok: true });
  assert.deepEqual(await a.copy("claudeCodeCommand"), { ok: true });
  assert.deepEqual(await a.copy("codexConfig"), { ok: true });
  assert.deepEqual(log, [
    `clip:http://127.0.0.1:8080/mcp\nAuthorization: Bearer ${TOKEN}`,
    `clip:claude mcp add --transport http xmesh http://127.0.0.1:8080/mcp --header "Authorization: Bearer ${TOKEN}"`,
    `clip:[mcp_servers.xmesh]\nurl = "http://127.0.0.1:8080/mcp"\nhttp_headers = { Authorization = "Bearer ${TOKEN}" }\n`,
  ]);
});

test("copyGateway copies a LAN address the kernel reports, and nothing it does not report", async () => {
  const lan = { kind: "lan" as const, host: "192.168.1.20", url: "http://192.168.1.20:8080/mcp" };
  const { controller } = fakeController(connected({ gateway: gatewayWith([lan]) }));
  const { platform, log } = fakePlatform();
  const a = new Actions(controller, platform, t, OK_CREDENTIALS);
  assert.deepEqual(await a.copyGateway("gatewayUrl", lan.url), { ok: true });
  assert.equal(log[0], `clip:${lan.url}\nAuthorization: Bearer ${TOKEN}`);

  const refused = await a.copyGateway("claudeCodeCommand", "http://10.9.9.9:8080/mcp");
  assert.equal(refused.ok, false);
  assert.equal(refused.ok === false && refused.error.code, "invalid_args");
  assert.equal(log.length, 1, "an address the kernel does not report is never copied");
});

test("a token that cannot be read copies nothing and fails with token_unavailable", async () => {
  const { controller } = fakeController(connected({ gateway: gatewayWith() }));
  const { platform, log } = fakePlatform();
  const a = new Actions(controller, platform, t, NO_CREDENTIALS);
  for (const kind of ["gatewayUrl", "claudeCodeCommand", "codexConfig"] as const) {
    const r = await a.copy(kind);
    assert.equal(r.ok, false);
    assert.equal(r.ok === false && r.error.code, "token_unavailable");
  }
  assert.deepEqual(log, [], "never a tokenless command");
});

test("a token with characters a connect command cannot carry is refused, not pasted", async () => {
  const { controller } = fakeController(connected({ gateway: gatewayWith() }));
  const { platform, log } = fakePlatform();
  const bad = { read: async () => 'abc"; rm -rf ~; "' };
  const r = await new Actions(controller, platform, t, bad).copy("claudeCodeCommand");
  assert.equal(r.ok === false && r.error.code, "token_unavailable");
  assert.deepEqual(log, []);
});

test("the tray's gateway copy uses the localhost address, and a failure becomes a notification", async () => {
  const { controller } = fakeController(connected({ gateway: gatewayWith() }));
  const { platform, log } = fakePlatform();
  const a = new Actions(controller, platform, t, NO_CREDENTIALS);
  const r = await a.dispatch({ type: "copy", kind: "codexConfig" });
  assert.equal(r.ok, false);
  assert.deepEqual(log, [`err:${t("menu.copyFailed")}`]);

  const ok = fakePlatform();
  await new Actions(connectedController(), ok.platform, t, OK_CREDENTIALS).dispatch({ type: "copy", kind: "gatewayUrl" });
  assert.deepEqual(ok.log, [`clip:http://127.0.0.1:8080/mcp\nAuthorization: Bearer ${TOKEN}`]);
});

test("mesh login commands are copied without reading any token", async () => {
  const { controller } = fakeController(EMPTY_SNAPSHOT);
  const { platform, log } = fakePlatform();
  let read = 0;
  const a = new Actions(controller, platform, t, {
    read: async () => {
      read++;
      return TOKEN;
    },
  });
  assert.deepEqual(await a.copy("meshLoginRead"), { ok: true });
  assert.equal(log.length, 1);
  assert.equal(read, 0);
});

test("copy refuses when there is no gateway, when offline, or when the URL is not plain", async () => {
  const noGateway = connected();
  if (noGateway.connection.kind === "connected") delete noGateway.connection.status.gateway;
  for (const snap of [EMPTY_SNAPSHOT, noGateway, connected({ gateway: gatewayWith([], 'http://x/"; rm -rf ~; "') })]) {
    const { controller } = fakeController(snap);
    const { platform, log } = fakePlatform();
    const r = await new Actions(controller, platform, t, OK_CREDENTIALS).copy("claudeCodeCommand");
    assert.equal(r.ok, false);
    assert.deepEqual(log, []);
  }
});

test("clipboard text only embeds plain http(s) URLs", () => {
  assert.equal(isSafeGatewayUrl("https://node.tailnet.ts.net/mcp"), true);
  assert.equal(isSafeGatewayUrl("http://127.0.0.1:7433/mcp"), true);
  for (const bad of ["javascript:alert(1)", "http://x y", "http://x/$(id)", "http://x/`id`", "http://x/\"", "http://x/'", "ftp://x", "", "http://x/\n"]) {
    assert.equal(isSafeGatewayUrl(bad), false, bad);
  }
});

test("the login commands copy the CLI as the app runs it, quoted for a terminal", async () => {
  const { controller } = fakeController({ ...EMPTY_SNAPSHOT, cliCommand: "'/Applications/My Tools/xmesh'" });
  const { platform, log } = fakePlatform();
  assert.deepEqual(await new Actions(controller, platform, t, NO_CREDENTIALS).copy("meshLoginRead"), { ok: true });
  assert.deepEqual(log, ["clip:'/Applications/My Tools/xmesh' mesh login --kind tailscale --access read --panel"]);
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
    const a = new Actions(controller, platform, t, OK_CREDENTIALS);
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
    await new Actions(controller, platform, t, OK_CREDENTIALS).dispatch({ type: "stop-kernel" });
    assert.deepEqual(log, expected);
  }
});

test("restart runs the CLI without asking, and a CLI failure is reported not thrown", async () => {
  const { controller } = fakeController(connected());
  const { platform, log } = fakePlatform();
  platform.runCli = async () => {
    throw new Error("exit 1");
  };
  const r = await new Actions(controller, platform, t, OK_CREDENTIALS).dispatch({ type: "restart-kernel" });
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
  const actions = new Actions(controller, platform, t, OK_CREDENTIALS);
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
  const actions = new Actions(controller, platform, t, NO_CREDENTIALS);
  assert.deepEqual(await actions.dispatch({ type: "start-kernel", node: "beta2-mac" }), { ok: true });
  assert.deepEqual(await actions.startKernel("office-pc"), { ok: false, error: { code: "not_found", message: "no stopped node with that name" } });
  assert.deepEqual(seen, ["start:beta2-mac"]);
});

test("restart of an unresponsive node runs the CLI for that node only; any other name is refused", async () => {
  const seen: string[] = [];
  const { controller } = fakeController({ ...EMPTY_SNAPSHOT, unresponsiveNodes: ["n1"] });
  const { platform } = fakePlatform();
  platform.runCli = async (command, node) => {
    seen.push(`${command}:${node}`);
  };
  const actions = new Actions(controller, platform, t, NO_CREDENTIALS);
  assert.deepEqual(await actions.dispatch({ type: "restart-unresponsive", node: "n1" }), { ok: true });
  assert.deepEqual(await actions.restartUnresponsive("office-pc"), { ok: false, error: { code: "not_found", message: "no unresponsive node with that name" } });
  assert.deepEqual(seen, ["restart:n1"]);
});

test("cleanup of a leftover state file only accepts a stale node and goes through the platform", async () => {
  const { controller } = fakeController({ ...EMPTY_SNAPSHOT, staleNodes: ["n1"] });
  const { platform, log } = fakePlatform();
  const actions = new Actions(controller, platform, t, NO_CREDENTIALS);
  assert.deepEqual(await actions.dispatch({ type: "cleanup-stale", node: "n1" }), { ok: true });
  assert.deepEqual(await actions.cleanupStale("office-pc"), { ok: false, error: { code: "not_found", message: "no stale node with that name" } });
  assert.deepEqual(log.filter((l) => l.startsWith("remove:")), ["remove:n1"]);
});

test("system service state requires administrator cleanup", async () => {
  const { controller } = fakeController({ ...EMPTY_SNAPSHOT, staleNodes: ["system"], protectedStaleNodes: ["system"] });
  const { platform, log } = fakePlatform();
  const r = await new Actions(controller, platform, t, NO_CREDENTIALS).cleanupStale("system");
  assert.equal(r.ok, false);
  assert.ok(!r.ok && r.error.code === "forbidden");
  assert.deepEqual(log, []);
});

test("kernel errors become results with the kernel's code", async () => {
  const { controller } = fakeController(connected(), {
    drain: async () => {
      throw new ControlRequestError("conflict", "already draining", 409);
    },
  });
  const r = await new Actions(controller, fakePlatform().platform, t, OK_CREDENTIALS).drain();
  assert.deepEqual(r, { ok: false, error: { code: "conflict", message: "already draining" } });
});

test("selectInstance only accepts instances that exist", async () => {
  const { controller, calls } = fakeController(connected());
  const a = new Actions(controller, fakePlatform().platform, t, OK_CREDENTIALS);
  assert.equal((await a.selectInstance("ghost")).ok, false);
  assert.deepEqual(await a.selectInstance("n1"), { ok: true });
  assert.deepEqual(calls, ["select:n1"]);
});

test("openExternal resolves the key to the fixed URL", async () => {
  const { controller } = fakeController(connected());
  const { platform, log } = fakePlatform();
  await new Actions(controller, platform, t, OK_CREDENTIALS).openExternal("docs");
  assert.deepEqual(log, ["ext:https://github.com/tzf1003/xmesh#readme"]);
});

test("stopping the kernel says so when CI jobs are running", async () => {
  const details: string[] = [];
  const busy = fixture<CiBinding>("CiBinding.busy");
  for (const status of [connected(), connected({ ci: { bindings: [busy] } })]) {
    const { controller } = fakeController(status);
    const { platform } = fakePlatform(false);
    platform.confirm = async (o) => {
      details.push(o.detail);
      return false;
    };
    await new Actions(controller, platform, t, OK_CREDENTIALS).dispatch({ type: "stop-kernel" });
  }
  assert.ok(!details[0]!.includes("CI"));
  assert.match(details[1]!, /还有 1 个 CI 任务正在运行/);
});

test("service actions use the manifest node name for an aliased state directory",async()=>{
 const {controller}=fakeController({...connected(),selectedInstance:"alias"});
 controller.nodeName=(id)=>id==="alias"?"real-node":id==="real-node"?"other-node":id;
 const {platform}=fakePlatform();
 const nodes:string[]=[];
 platform.runCli=async(_command,node)=>{nodes.push(node??"");};
 const actions=new Actions(controller,platform,t,OK_CREDENTIALS);
 await actions.dispatch({type:"restart-kernel"});
 await actions.dispatch({type:"stop-kernel"});
 assert.deepEqual(nodes,["real-node","real-node"]);
});

test("managed lifecycle actions retain an aliased directory ID even without a manifest", async () => {
  const { controller } = fakeController({ ...connected(), selectedInstance: "alias", stoppedNodes: ["offline-alias"] });
  controller.nodeName = () => "real-node";
  const { platform, log } = fakePlatform();
  const seen: string[] = [];
  platform.manageKernel = async (command, node) => { seen.push(`${command}:${node}`); };
  const actions = new Actions(controller, platform, t, OK_CREDENTIALS);
  await actions.dispatch({ type: "restart-kernel" });
  await actions.dispatch({ type: "stop-kernel" });
  await actions.dispatch({ type: "start-kernel", node: "offline-alias" });
  assert.deepEqual(seen, ["restart:alias", "stop:alias", "start:offline-alias"]);
  assert.ok(!log.some((entry) => entry.startsWith("cli:")));
});

test("native tray restart asks once, includes current task impact and honors cancellation", async () => {
  for (const answer of [false, true]) {
    const { controller } = fakeController({ ...connected(), selectedInstance: "n1" });
    const { platform, log } = fakePlatform(answer);
    let detail = "";
    platform.confirm = async opts => { detail = opts.detail; log.push("confirm"); return answer; };
    const result = await new Actions(controller, platform, t, OK_CREDENTIALS).dispatchFromTray({ type: "restart-kernel" });
    assert.equal(result.ok, true);
    assert.deepEqual(log, answer ? ["confirm", "cli:restart"] : ["confirm"]);
    assert.match(detail, /win-dev-01/);
    assert.match(detail, /终端会话/);
    assert.match(detail, /执行任务/);
    assert.match(detail, /CI/);
  }
});

test("native tray restart refuses instance and boot changes while confirmation is open", async () => {
  for (const change of ["instance", "boot"] as const) {
    const { controller } = fakeController({ ...connected(), selectedInstance: "n1" });
    const { platform, log } = fakePlatform();
    platform.confirm = async () => {
      const before = controller.snapshot;
      const c = before.connection;
      assert.equal(c.kind, "connected");
      if (c.kind !== "connected") return true;
      (controller as unknown as { snapshot: Snapshot }).snapshot = change === "instance"
        ? { ...before, selectedInstance: "n2" }
        : { ...before, connection: { ...c, status: { ...c.status, control: { ...c.status.control, boot_id: "new-boot" } } } };
      return true;
    };
    const result = await new Actions(controller, platform, t, OK_CREDENTIALS).dispatchFromTray({ type: "restart-kernel" });
    assert.equal(result.ok, false);
    assert.equal(!result.ok && result.error.code, "conflict");
    assert.deepEqual(log, []);
  }
});

test("connection copy rechecks current primary, secondary and gateway availability after reading credentials", async () => {
  const secondary = { kind: "lan" as const, host: "192.168.1.20", url: "http://192.168.1.20:8080/mcp" };
  for (const kind of ["gatewayUrl", "claudeCodeCommand", "codexConfig"] as const) {
    for (const change of ["primary", "secondary", "disabled"] as const) {
      const gateway = gatewayWith([secondary]);
      const { controller } = fakeController({ ...connected({ gateway }), selectedInstance: "n1" });
      const { platform, log } = fakePlatform();
      const selectedUrl = change === "secondary" ? secondary.url : gateway.url;
      const credentials = { read: async () => {
        const before = controller.snapshot;
        if (before.connection.kind !== "connected") throw new Error("not connected");
        const changed = change === "primary" ? gatewayWith([secondary], "http://127.0.0.1:9090/mcp") : change === "secondary" ? gatewayWith() : undefined;
        (controller as unknown as { snapshot: Snapshot }).snapshot = { ...before, connection: { ...before.connection, status: { ...before.connection.status, gateway: changed } } };
        return TOKEN;
      } };
      const result = await new Actions(controller, platform, t, credentials).copyGateway(kind, selectedUrl);
      assert.equal(!result.ok && result.error.code, "conflict", `${kind}/${change}`);
      assert.deepEqual(log, []);
    }
  }
});

test("connection copy permits token rotation and addresses that remain reachable in another representation", async () => {
  const secondary = { kind: "lan" as const, host: "192.168.1.20", url: "http://192.168.1.20:8080/mcp" };
  const { controller } = fakeController(connected({ gateway: gatewayWith([secondary]) }));
  const { platform, log } = fakePlatform();
  const rotatedToken = "rotated-0123456789abcdefABCDEF";
  const credentials = { read: async () => {
    const before = controller.snapshot;
    if (before.connection.kind !== "connected") throw new Error("not connected");
    (controller as unknown as { snapshot: Snapshot }).snapshot = { ...before, connection: { ...before.connection, status: { ...before.connection.status, gateway: gatewayWith([], secondary.url) } } };
    return rotatedToken;
  } };
  const result = await new Actions(controller, platform, t, credentials).copyGateway("claudeCodeCommand", secondary.url);
  assert.equal(result.ok, true);
  assert.equal(log.length, 1);
  assert.ok(log[0]?.includes(secondary.url));
  assert.ok(log[0]?.includes(rotatedToken));
});

test("native unresponsive restart confirms unknown task impact and rejects a replaced manifest", async () => {
  for (const answer of [false, true]) {
    const { controller } = fakeController({ ...EMPTY_SNAPSHOT, unresponsiveNodes: ["n1"] });
    const { platform, log } = fakePlatform(answer);
    await new Actions(controller, platform, t, OK_CREDENTIALS).dispatchFromTray({ type: "restart-unresponsive", node: "n1" });
    assert.deepEqual(log, answer ? ["confirm", "cli:restart"] : ["confirm"]);
  }
  const { controller } = fakeController({ ...EMPTY_SNAPSHOT, unresponsiveNodes: ["n1"] });
  let boot = "first";
  Object.assign(controller, { nodeBootId: () => boot });
  const { platform, log } = fakePlatform();
  platform.confirm = async () => { boot = "replacement"; return true; };
  const result = await new Actions(controller, platform, t, OK_CREDENTIALS).dispatchFromTray({ type: "restart-unresponsive", node: "n1" });
  assert.equal(!result.ok && result.error.code, "conflict");
  assert.deepEqual(log, []);
});
