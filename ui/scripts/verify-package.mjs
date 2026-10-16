// Checks an unpacked electron-builder output (release/<x>-unpacked or release/mac*/xmesh.app's parent).
//
//   node scripts/verify-package.mjs --dir release/win-unpacked [--kernel] [--run-kernel] [--smoke]
//
//   always          app.asar holds the compiled app and nothing else; release fuses are on
//   --kernel        resources/bin/xmesh[.exe] is bundled
//   --run-kernel    ... and `xmesh version` runs (target must match the host)
//   --smoke         launch the packaged app against a fake kernel and wait until it talks to it
//                   (proves the asar-integrity fuse, protocol handler, discovery and the control client
//                   all work in the shipped binary; the target must match the host)
//
// Needs `npm run build` first (the fake kernel is taken from dist/testserver).

import { execFileSync, spawn } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { pathToFileURL } from "node:url";
import { fileURLToPath } from "node:url";
import * as asar from "@electron/asar";

const UI = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
const flag = (n) => args.includes(`--${n}`);
const value = (n) => {
  const i = args.indexOf(`--${n}`);
  return i === -1 ? undefined : args[i + 1];
};
const fail = (msg) => {
  console.error(`FAIL ${msg}`);
  process.exit(1);
};
const ok = (msg) => console.log(`ok   ${msg}`);

const dir = path.resolve(value("dir") ?? fail("--dir is required"));
const base = path.basename(dir);
const platform = value("platform") ?? (base.startsWith("win") ? "win32" : base.startsWith("linux") ? "linux" : "darwin");

let binary;
let resources;
if (platform === "win32") {
  binary = path.join(dir, "xmesh.exe");
  resources = path.join(dir, "resources");
} else if (platform === "darwin") {
  const app = path.join(dir, "xmesh.app");
  binary = path.join(app, "Contents", "MacOS", "xmesh");
  resources = path.join(app, "Contents", "Resources");
} else {
  binary = path.join(dir, "xmesh-ui");
  resources = path.join(dir, "resources");
}
for (const p of [binary, path.join(resources, "app.asar")]) if (!fs.existsSync(p)) fail(`missing ${p}`);
ok(`found ${binary}`);

// app.asar content
const entries = asar.listPackage(path.join(resources, "app.asar"), { isPack: false }).map((e) => e.replaceAll("\\", "/"));
for (const need of ["/package.json", "/dist/src/main/main.js", "/dist/src/preload/index.js", "/assets/tray/tray-ok.png"]) {
  if (!entries.includes(need)) fail(`app.asar lacks ${need}`);
}
const stray = entries.filter((e) => /^\/(dist\/test|dist\/testserver|testserver|test|src|node_modules)(\/|$)/.test(e));
if (stray.length > 0) fail(`app.asar contains files that must not ship: ${stray.slice(0, 5).join(", ")}`);
if (!entries.some((e) => e.startsWith("/dist/renderer/"))) fail("app.asar has no panel page");
ok(`app.asar: ${entries.length} entries, compiled app only`);

// fuses
execFileSync(process.execPath, [path.join(UI, "scripts", "flip-fuses.mjs"), "--verify", "--profile", "release", "--binary", binary], { stdio: "inherit" });

// bundled kernel
const kernel = path.join(resources, "bin", platform === "win32" ? "xmesh.exe" : "xmesh");
if (flag("kernel") || flag("run-kernel")) {
  if (!fs.existsSync(kernel)) fail(`kernel not bundled at ${kernel}`);
  ok(`kernel bundled (${fs.statSync(kernel).size} bytes)`);
}
if (flag("run-kernel")) {
  const out = execFileSync(kernel, ["version"], { encoding: "utf8", timeout: 30_000 }).trim();
  if (!/^xmesh \S+/.test(out)) fail(`unexpected \`xmesh version\` output: ${out}`);
  ok(`bundled kernel runs: ${out}`);
}

if (flag("smoke")) await smoke();

async function smoke() {
  const { FakeKernel } = await import(pathToFileURL(path.join(UI, "dist", "testserver", "server.js")).href);
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-smoke-"));
  const userData = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-smoke-ud-"));
  const kernelServer = await FakeKernel.start({ pingMs: 5000 });
  kernelServer.writeManifest(root, "smoke-node");

  const extra = value("app-arg") ? [value("app-arg")] : [];
  const child = spawn(binary, [`--user-data-dir=${userData}`, ...extra], {
    env: { ...process.env, XMESH_UI_STATE_ROOT: root, XMESH_UI_LANG: "en" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  child.stdout.on("data", (d) => (output += d));
  child.stderr.on("data", (d) => (output += d));
  let exited;
  child.on("exit", (code, signal) => (exited = { code, signal }));

  const deadline = Date.now() + 60_000;
  let status = false;
  while (Date.now() < deadline && !exited) {
    status = kernelServer.requests.some((r) => r.method === "GET" && r.path === "/v1/status");
    if (status && kernelServer.requests.some((r) => r.path.startsWith("/v1/events"))) break;
    await new Promise((r) => setTimeout(r, 250));
  }
  const seen = kernelServer.requests.map((r) => `${r.method} ${r.path}`);

  if (!exited) {
    if (platform === "win32") {
      try {
        execFileSync("taskkill", ["/pid", String(child.pid), "/T", "/F"], { stdio: "ignore" });
      } catch {}
    } else {
      child.kill("SIGKILL");
    }
  }
  await kernelServer.stop();
  fs.rmSync(root, { recursive: true, force: true });
  try {
    fs.rmSync(userData, { recursive: true, force: true });
  } catch {}

  if (exited) fail(`packaged app exited early (${JSON.stringify(exited)}):\n${output}`);
  if (!status) fail(`packaged app never asked the fake kernel for /v1/status; saw [${seen.join(", ")}]\n${output}`);
  ok(`packaged app started and talked to the fake kernel: ${[...new Set(seen)].join(", ")}`);
}
