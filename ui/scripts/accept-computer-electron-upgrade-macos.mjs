#!/usr/bin/env node
// Opt-in lifecycle acceptance using real packaged Electron entrypoints.
// The update trigger uses the production Updater module; the replacement app
// itself reads pending.json and restores the installed Host and business node.
import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { createRequire } from "node:module";
import { promisify } from "node:util";

const require = createRequire(import.meta.url);
const { Updater } = require("../dist/src/main/update/updater.js");
const { UpdateStore } = require("../dist/src/main/update/store.js");
const { macApplier } = require("../dist/src/main/update/apply.js");
const { ComputerUpdateTarget, computerHostServices, runComputerHost } = require("../dist/src/main/computer-host.js");
const { kernelServices } = require("../dist/src/main/update/kernels.js");
const { discoverInstances } = require("../dist/src/main/discovery.js");
const { runXmeshRaw } = require("../dist/src/main/cli.js");
const { installerName, CHECKSUM_FILE } = require("../dist/src/main/update/releases.js");
const { parseVersion } = require("../dist/src/shared/version.js");
const { Client, StreamableHTTPClientTransport } = require("../dist/src/main/mcp-sdk.js");
const exec = promisify(execFile);
const args = process.argv.slice(2);
const option = name => { const i = args.indexOf("--" + name); return i < 0 ? undefined : args[i + 1]; };
assert.equal(process.platform, "darwin");
assert.equal(process.env.XMESH_TEST_ALLOW_COMPUTER_UPGRADE, "1", "isolated upgrade acceptance opt-in required");
const oldInput = option("old-app"), dmg = option("new-dmg");
assert.ok(oldInput && path.isAbsolute(oldInput) && fs.statSync(oldInput).isDirectory());
assert.ok(dmg && path.isAbsolute(dmg) && fs.statSync(dmg).isFile());
const report = option("report");
assert.ok(!report || path.isAbsolute(report));
const uid = process.getuid();
const launchAgents = path.join(os.homedir(), "Library/LaunchAgents");
const hostLabel = "xmesh-computer-host";
const hostState = path.join(os.homedir(), "Library/Application Support/xmesh/computer");
const hostPlist = path.join(launchAgents, hostLabel + ".plist");
assert.equal(fs.existsSync(hostPlist), false, "preserve the existing Computer Host installation");
assert.equal(fs.existsSync(hostState), false, "preserve existing Computer Host state");
async function serviceAbsent(label) {
  try { await exec("launchctl", ["print", `gui/${uid}/${label}`]); return false; }
  catch (e) { assert.match(String(e.stderr), /Could not find service/); return true; }
}
assert.ok(await serviceAbsent(hostLabel));
const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-electron-upgrade-"));
// macApplier intentionally refuses read-only /Volumes installs. Only the app
// being replaced lives under user Applications; data and mounts use TMPDIR.
const applications = path.join(os.homedir(), "Applications");
fs.mkdirSync(applications, { recursive: true });
const installed = fs.mkdtempSync(path.join(applications, "xmesh-electron-accept-"));
const app = path.join(installed, "xmesh.app");
const executable = path.join(app, "Contents/MacOS/xmesh");
const binary = path.join(app, "Contents/Resources/bin/xmesh");
const customHost = args.includes("--custom-host-state");
const installedHostState = customHost ? fs.mkdtempSync(path.join("/tmp", "xmesh-electron-host-")) : hostState;
const hostEndpoint = customHost ? path.join(installedHostState, "control.sock") : undefined;
const hostCommand = command => runComputerHost(command, binary, hostEndpoint);
const userData = path.join(root, "user-data"), states = path.join(root, "nodes");
const node = "upgrade-" + randomBytes(5).toString("hex"), nodeLabel = "xmesh-" + node;
const nodePlist = path.join(launchAgents, nodeLabel + ".plist");
const state = path.join(states, node), config = path.join(state, "xmesh.yaml");
const events = [], ownedPids = new Set();
let installer, interrupted, cleaning = false, hostInstalled = false, nodeInstalled = false;
let beforeHost, beforeNode, beforeInfo, afterHost, afterNode, afterInfo, oldPid, newPid;
let mounts;
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => { interrupted = new Error("acceptance interrupted"); });
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const gone = pid => { try { process.kill(pid, 0); return false; } catch (e) { return e.code === "ESRCH"; } };
async function waitFor(check, label, timeout = 45000) {
  const deadline = Date.now() + timeout;
  do {
    if (interrupted && !cleaning) throw interrupted;
    if (await check()) return;
    await delay(100);
  } while (Date.now() < deadline);
  throw new Error("timed out: " + label);
}
async function plist(file) { return JSON.parse((await exec("plutil", ["-convert", "json", "-o", "-", file])).stdout); }
async function version() {
  const info = await plist(path.join(app, "Contents/Info.plist"));
  const v = info.CFBundleShortVersionString;
  assert.ok(parseVersion(v));
  assert.equal((await exec(binary, ["version"])).stdout.trim(), "xmesh v" + v);
  return v;
}
async function appProcesses() {
  const { stdout } = await exec("ps", ["-axo", "pid=,comm="]);
  return stdout.split("\n").map(line => /^\s*(\d+)\s+(.*)$/.exec(line)).filter(m => m && m[2] === executable).map(m => Number(m[1]));
}
async function launch() {
  // finishPending() removes stale installer files on startup. Its removal of
  // this owned marker proves cleanup finished before a new installer is staged.
  const updates = path.join(userData, "updates");
  fs.mkdirSync(updates, { recursive: true });
  const startupMarker = path.join(updates, "acceptance-startup-marker");
  fs.writeFileSync(startupMarker, "startup");
  await exec("/usr/bin/open", ["-g", "-n", "--env", "XMESH_UI_STATE_ROOT=" + states,
    "--env", "XMESH_UPDATE_FEED=http://127.0.0.1:1", app, "--args", "--user-data-dir=" + userData]);
  await waitFor(async () => (await appProcesses()).length === 1, "formal Electron startup");
  await waitFor(() => !fs.existsSync(startupMarker), "formal Electron startup update cleanup");
  events.push("old-electron-startup-cleanup-observed");
  const [pid] = await appProcesses(); ownedPids.add(pid); return pid;
}
async function nodeStatus() {
  const instance = discoverInstances(states).find(i => i.id === node);
  assert.ok(instance, "business node manifest missing");
  return { instance, status: await nodeRead(instance, "status") };
}
async function nodeRead(instance, resource) {
  const major = instance.manifest.api_version.split(".")[0];
  assert.ok(major === "1" || major === "2", "unsupported control API");
  // The fixture spans the Control API migration; read each installed version
  // through its advertised protocol while production clients retain v2 checks.
  return new Promise((resolve, reject) => {
    const request = http.get({ socketPath: instance.manifest.endpoint, path: `/v${major}/${resource}`,
      headers: { Host: "xmesh-control", Accept: "application/json" } }, response => {
      let body = "";
      response.setEncoding("utf8");
      response.on("data", chunk => { body += chunk; });
      response.on("error", reject);
      response.on("end", () => {
        try {
          assert.equal(response.statusCode, 200);
          assert.equal(String(response.headers["x-xmesh-control-version"]).split(".")[0], major);
          resolve(JSON.parse(body));
        } catch (error) { reject(error); }
      });
    });
    request.setTimeout(5000, () => request.destroy(new Error("fixture control request timed out")));
    request.on("error", reject);
  });
}
async function computerInfo(snapshot) {
  return (await nodeRead(snapshot.instance, "computer")).info;
}
async function freeAddress() {
  const server = net.createServer();
  await new Promise((resolve, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", resolve); });
  const port = server.address().port; await new Promise(resolve => server.close(resolve));
  return "127.0.0.1:" + port;
}
async function businessCall(gateway) {
  const token = fs.readFileSync(path.join(state, "pats/default.token"), "utf8").trim();
  const client = new Client({ name: "electron-upgrade-acceptance", version: "1" });
  try {
    await client.connect(new StreamableHTTPClientTransport(new URL("http://" + gateway + "/mcp"), { requestInit: { headers: { Authorization: "Bearer " + token } } }));
    const result = await client.callTool({ name: "local/sys.info", arguments: {} });
    assert.ok(!result.isError && result.structuredContent, "business MCP call failed");
    assert.equal(result.structuredContent.hostname, node);
  } finally { await client.close(); }
}
async function stopApp(pid) { if (pid && !gone(pid)) process.kill(pid, "SIGTERM"); if (pid) await waitFor(() => gone(pid), "owned Electron exit"); }
let outcome, failure;
try {
  assert.equal(fs.existsSync(nodePlist), false); assert.ok(await serviceAbsent(nodeLabel));
  await exec("ditto", [oldInput, app], { timeout: 120000 });
  await exec("codesign", ["--verify", "--deep", "--strict", app]);
  const beforeVersion = await version();
  fs.mkdirSync(state, { recursive: true }); fs.mkdirSync(userData, { recursive: true });
  new UpdateStore(path.join(userData, "update.json")).set({ prefs: { auto: false, prerelease: true } });
  const direct = await freeAddress(), gateway = await freeAddress();
  fs.writeFileSync(config, JSON.stringify({ node: { name: node, roles: ["agent", "gateway"] },
    mesh: { mode: "lan", lan: { mdns: true } }, gateway: { listen: gateway },
    agent: { default_cwd: root, cwd_policy: { allow: [root] }, policy: { isolation: "L1" },
      computer: { enabled: true, ...(hostEndpoint && { endpoint: hostEndpoint }) }, authz: { "tag:peer": { tools: ["*"], backends: ["*"] } } } }));
  await exec(binary, ["install", "--config", config, "--state-dir", state]); nodeInstalled = true;
  const installedService = await plist(nodePlist);
  assert.equal(installedService.ProgramArguments[0], binary);
  // Keep the owned business fixture on loopback, using the real CLI's local mode.
  installedService.ProgramArguments.push("--local", "--listen", direct);
  fs.writeFileSync(nodePlist, JSON.stringify(installedService)); await exec("plutil", ["-convert", "xml1", nodePlist]);
  await exec(binary, ["computer", "install", "--state-dir", installedHostState, "--json"]); hostInstalled = true;
  assert.equal((await plist(hostPlist)).ProgramArguments[0], binary);
  await exec(binary, ["computer", "start", "--json"]);
  await exec(binary, ["start", "--name", nodeLabel, "--json"]);
  await waitFor(async () => { try { beforeNode = await nodeStatus(); return true; } catch { return false; } }, "old business node");
  beforeInfo = await computerInfo(beforeNode);
  beforeHost = await hostCommand("status");
  if (customHost) {
    assert.equal((await runComputerHost("status", binary)).running, false);
    assert.equal(fs.existsSync(hostState), false);
    events.push("custom-host-ready-default-endpoint-absent");
  }
  assert.equal(beforeHost.version, "v" + beforeVersion); assert.ok(beforeHost.native_pid > 0);
  assert.equal(beforeNode.status.control.kernel_version, "v" + beforeVersion);
  await businessCall(gateway);
  for (const pid of [beforeHost.pid, beforeHost.native_pid, beforeNode.instance.manifest.pid]) ownedPids.add(pid);
  oldPid = await launch(); events.push("formal-old-electron-started");
  // Read version metadata from the actual replacement DMG before handing it over.
  // DiskImages cannot mount inside some mounted sparsebundle build volumes.
  // Mountpoints contain no copied installer data and are removed after detach.
  mounts = fs.mkdtempSync(path.join("/tmp", "xmesh-electron-mounts-"));
  const inspectMount = path.join(mounts, "inspect"); fs.mkdirSync(inspectMount);
  await exec("hdiutil", ["attach", "-nobrowse", "-readonly", "-noverify", "-mountpoint", inspectMount, dmg]);
  let afterVersion;
  try {
    afterVersion = (await plist(path.join(inspectMount, "xmesh.app/Contents/Info.plist"))).CFBundleShortVersionString;
    assert.ok(parseVersion(afterVersion)); assert.notEqual(afterVersion, beforeVersion);
    await exec("codesign", ["--verify", "--deep", "--strict", path.join(inspectMount, "xmesh.app")]);
  } finally { await exec("hdiutil", ["detach", inspectMount]); fs.rmdirSync(inspectMount); }
  const body = fs.readFileSync(dmg), sha = createHash("sha256").update(body).digest("hex");
  const name = installerName(parseVersion(afterVersion), { platform: "mac", arch: process.arch });
  const source = {
    releases: async () => [{ tag_name: "v" + afterVersion, draft: false, prerelease: afterVersion.includes("-"), published_at: "2026-10-20T00:00:00Z", body: "Electron upgrade acceptance", assets: [{ name, size: body.length, digest: "sha256:" + sha }, { name: CHECKSUM_FILE, size: 100 }] }],
    text: async () => `${sha}  ${name}\n`,
    download: async (_tag, _asset, dest, progress, signal) => { signal.throwIfAborted(); fs.writeFileSync(dest, body); progress({ received: body.length, total: body.length }); return { sha256: sha, bytes: body.length }; },
  };
  const computerTarget = new ComputerUpdateTarget(path.join(userData, "computer-update-target.json"), { discover: () => discoverInstances(states) });
  const computer = computerHostServices({ version: beforeVersion, beforeStop: () => computerTarget.capture(),
    run: command => runComputerHost(command, binary, computerTarget.endpoint()) });
  const kernels = kernelServices({ version: beforeVersion, discover: () => discoverInstances(states), run: (command, name) => runXmeshRaw(command, name, binary) });
  // open's environment options keep the disposable app's custom discovery and
  // user-data directories across the normal production handover script.
  const wrappers = path.join(root, "launch-wrapper"); fs.mkdirSync(wrappers);
  const quote = value => "'" + value.replaceAll("'", "'\\''") + "'";
  fs.writeFileSync(path.join(wrappers, "open"), `#!/bin/sh\nexec /usr/bin/open -g -n --env ${quote("XMESH_UI_STATE_ROOT=" + states)} --env XMESH_UPDATE_FEED=http://127.0.0.1:1 "$@" --args ${quote("--user-data-dir=" + userData)}\n`, { mode: 0o700 });
  const applier = macApplier(executable, (command, arguments_, options) => {
    installer = spawn(command, arguments_, { ...options, env: { ...process.env, PATH: wrappers + path.delimiter + process.env.PATH, TMPDIR: mounts } });
    return installer;
  });
  const updater = new Updater({ version: beforeVersion, packaged: true, platform: "darwin", arch: process.arch, kernelBundled: true,
    execPath: executable, pid: oldPid, dataDir: path.join(userData, "updates"), source, applier,
    store: new UpdateStore(path.join(userData, "update.json")), runningNodes: () => discoverInstances(states).map(i => i.id),
    stopNode: name => kernels.stop(name), startNode: name => kernels.start(name),
    stopComputer: () => computer.stop(), startComputer: () => computer.start(),
    quit: () => { process.kill(oldPid, "SIGTERM"); }, openPath: async () => { throw new Error("unexpected manual installation"); }, notify: () => {} });
  await updater.check(); assert.equal(updater.state.phase, "available"); assert.deepEqual(await updater.install(), { ok: true });
  const pendingFile = path.join(userData, "updates/pending.json");
  const pending = JSON.parse(fs.readFileSync(pendingFile, "utf8")); assert.deepEqual(pending.nodes, [node]); assert.equal(pending.computer, true);
  assert.ok(gone(beforeHost.pid) && gone(beforeHost.native_pid) && gone(beforeNode.instance.manifest.pid));
  events.push("production-updater-stopped-host-native-and-business-node");
  await waitFor(() => installer.exitCode !== null || installer.signalCode !== null, "DMG installer exit", 120000); assert.equal(installer.exitCode, 0);
  await waitFor(async () => { const pids = await appProcesses(); if (pids.length !== 1 || pids[0] === oldPid) return false; newPid = pids[0]; ownedPids.add(newPid); return true; }, "replacement formal Electron startup");
  await waitFor(async () => { try { afterHost = await hostCommand("status"); afterNode = await nodeStatus(); return !fs.existsSync(pendingFile) && afterHost.running && afterHost.version === "v" + afterVersion && afterNode.status.control.kernel_version === "v" + afterVersion; } catch { return false; } }, "formal app pending-update restoration", 90000);
  afterInfo = await computerInfo(afterNode);
  afterHost = await hostCommand("status");
  if (customHost) {
    assert.equal(computerTarget.endpoint(), hostEndpoint);
    assert.equal((await runComputerHost("status", binary)).running, false);
    assert.equal(fs.existsSync(hostState), false);
    events.push("custom-host-endpoint-retained-through-electron-restart");
  }
  for (const pid of [afterHost.pid, afterHost.native_pid, afterNode.instance.manifest.pid]) if (pid) ownedPids.add(pid);
  assert.notEqual(afterHost.pid, beforeHost.pid); assert.ok(afterHost.native_pid > 0 && afterHost.native_pid !== beforeHost.native_pid);
  assert.notEqual(afterNode.instance.manifest.pid, beforeNode.instance.manifest.pid);
  assert.ok(beforeInfo.desktop_epoch && afterInfo.desktop_epoch);
  assert.notEqual(afterInfo.desktop_epoch, beforeInfo.desktop_epoch);
  assert.equal(await version(), afterVersion); assert.ok(gone(oldPid) && !gone(newPid));
  await businessCall(gateway);
  assert.equal(fs.existsSync(app + ".update-old"), false);
  await exec("codesign", ["--verify", "--deep", "--strict", app]);
  await exec("xcrun", ["stapler", "validate", app]);
  events.push("real-dmg-replaced-signed-bundle", "formal-new-electron-finished-pending", "business-node-and-host-new-versions-and-pids-verified", "business-mcp-call-before-and-after-upgrade", "installed-signature-and-stapling-verified");
  outcome = { result: "PASS", from: beforeVersion, to: afterVersion, application_entry: "formal-packaged-electron", trigger: "production-updater-module", launch: "background-open-with-isolated-data-and-discovery", checks: events,
    host_pid_changed: true, native_pid_changed: true, business_node_pid_changed: true, app_pid_changed: true,
    before_desktop_state: beforeInfo.state, after_desktop_state: afterInfo.state,
    desktop_epoch_changed: true, custom_host_state: customHost, installer_sha256: sha };
} catch (error) {
  failure = error;
} finally {
  try {
    cleaning = true;
    for (const pid of await appProcesses()) { ownedPids.add(pid); await stopApp(pid); }
    if (installer && installer.exitCode === null && installer.signalCode === null) { installer.kill("SIGTERM"); await waitFor(() => installer.exitCode !== null || installer.signalCode !== null, "owned installer exit"); }
    if (nodeInstalled && fs.existsSync(nodePlist)) {
      assert.equal((await plist(nodePlist)).ProgramArguments[0], binary, "service ownership changed");
      for (const item of discoverInstances(states)) ownedPids.add(item.manifest.pid);
      if (!(await serviceAbsent(nodeLabel))) await exec(binary, ["stop", "--name", nodeLabel, "--json"]);
      await exec(binary, ["uninstall", "--name", nodeLabel, "--json"]);
      assert.equal(fs.existsSync(nodePlist), false); assert.ok(await serviceAbsent(nodeLabel));
    }
    if (hostInstalled && fs.existsSync(hostPlist)) {
      assert.equal((await plist(hostPlist)).ProgramArguments[0], binary, "Host ownership changed");
      const status = await hostCommand("status");
      for (const pid of [status.pid, status.native_pid]) if (pid) ownedPids.add(pid);
      if (await serviceAbsent(hostLabel)) fs.unlinkSync(hostPlist);
      else await exec(binary, ["computer", "uninstall", "--json"]);
      assert.equal(fs.existsSync(hostPlist), false); assert.ok(await serviceAbsent(hostLabel));
      fs.rmSync(installedHostState, { recursive: true, force: true });
    }
    await waitFor(() => [...ownedPids].every(gone), "owned application and service process cleanup");
    if (mounts) for (const mount of fs.readdirSync(mounts)) { await exec("hdiutil", ["detach", path.join(mounts, mount), "-force"]); }
    if (mounts) fs.rmSync(mounts, { recursive: true, force: true });
    if (customHost) fs.rmSync(installedHostState, { recursive: true, force: true });
    fs.rmSync(installed, { recursive: true, force: true }); fs.rmSync(root, { recursive: true, force: true });
  } catch (cleanupError) {
    throw failure ? new AggregateError([failure, cleanupError], "upgrade acceptance and cleanup failed") : cleanupError;
  }
}
if (failure) throw failure;
if (interrupted) throw interrupted;
const result = { ...outcome, cleanup: "PASS" };
if (report) fs.writeFileSync(report, JSON.stringify(result, null, 2) + "\n");
console.log(JSON.stringify(result));
