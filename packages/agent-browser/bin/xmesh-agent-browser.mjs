#!/usr/bin/env node
// Launcher that runs `agent-browser mcp` as an xmesh stdio backend.
//
// What it adds on top of the upstream MCP server:
//   * safe defaults: required domain allowlist, content boundaries, output cap, short daemon
//     idle timeout, and an action policy that denies evaluate / download / upload;
//   * a refusal to attach to the user's own browser (CDP, auto-connect, profile, state) unless
//     the node owner opts in with BROWSER_BACKEND_ALLOW_USER_BROWSER=1;
//   * one private namespace per launch, so per-caller instances never share a daemon;
//   * cleanup: the upstream daemon and Chrome are not in the MCP process group, so on exit this
//     launcher closes the namespace's browser itself (`close --all`) and removes its files.
//
// Usage: xmesh-agent-browser.mjs [--tools core,tabs]
// Configuration is read from the environment; secrets and policy come from the xmesh
// backend environment (backend-env.json), not from the command line.
import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const DEFAULT_TOOLS = "core,tabs";
export const DEFAULT_DENY = ["evaluate", "download", "upload"];
const KNOWN_PROFILES = new Set(["core", "network", "state", "debug", "tabs", "react", "mobile", "all"]);
// Variables that attach agent-browser to a browser or login state the node owner did not
// set aside for the backend.
const USER_BROWSER_VARS = ["AGENT_BROWSER_CDP", "AGENT_BROWSER_AUTO_CONNECT", "AGENT_BROWSER_PROFILE", "AGENT_BROWSER_STATE"];

export function fail(message) {
  process.stderr.write(`xmesh-agent-browser: ${message}\n`);
  process.exit(2);
}

export function parseArgs(argv) {
  let tools = process.env.BROWSER_BACKEND_TOOLS || DEFAULT_TOOLS;
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--tools" && i + 1 < argv.length) tools = argv[++i];
    else if (argv[i].startsWith("--tools=")) tools = argv[i].slice(8);
    else fail(`unknown argument ${argv[i]}`);
  }
  const profiles = tools.split(",").map((item) => item.trim()).filter(Boolean);
  if (profiles.length === 0 || profiles.some((item) => !KNOWN_PROFILES.has(item))) {
    fail(`invalid tool profiles ${JSON.stringify(tools)}; use a comma list of ${[...KNOWN_PROFILES].join(", ")}`);
  }
  if (profiles.includes("all") && process.env.BROWSER_BACKEND_ALLOW_ALL_TOOLS !== "1") {
    fail("--tools all exposes every upstream tool; set BROWSER_BACKEND_ALLOW_ALL_TOOLS=1 to allow it");
  }
  return profiles.join(",");
}

// Build the child environment. Pure function so it can be tested without a browser.
export function buildEnv(source, namespace, policyFile) {
  const env = { ...source };
  if (!env.AGENT_BROWSER_ALLOWED_DOMAINS?.trim()) {
    fail("AGENT_BROWSER_ALLOWED_DOMAINS is not set. Set a comma list such as example.com,*.example.com in the backend environment, "
      + "or BROWSER_BACKEND_UNRESTRICTED_DOMAINS=1 to browse any site");
  }
  if (env.AGENT_BROWSER_ALLOWED_DOMAINS.trim() === "*") {
    if (env.BROWSER_BACKEND_UNRESTRICTED_DOMAINS !== "1") fail("AGENT_BROWSER_ALLOWED_DOMAINS=* needs BROWSER_BACKEND_UNRESTRICTED_DOMAINS=1");
    delete env.AGENT_BROWSER_ALLOWED_DOMAINS;
  }
  if (env.BROWSER_BACKEND_ALLOW_USER_BROWSER !== "1") {
    for (const name of USER_BROWSER_VARS) {
      if (env[name]) fail(`${name} would attach to a browser or login state outside the backend; set BROWSER_BACKEND_ALLOW_USER_BROWSER=1 to allow it`);
    }
  }
  env.AGENT_BROWSER_NAMESPACE = namespace;
  env.AGENT_BROWSER_SESSION ||= namespace;
  env.AGENT_BROWSER_CONTENT_BOUNDARIES ||= "1";
  env.AGENT_BROWSER_MAX_OUTPUT ||= "50000";
  env.AGENT_BROWSER_IDLE_TIMEOUT_MS ||= "300000";
  env.AGENT_BROWSER_ACTION_POLICY ||= policyFile;
  return env;
}

export function policyDocument(extraDeny = process.env.BROWSER_BACKEND_EXTRA_DENY) {
  const extra = (extraDeny || "").split(",").map((item) => item.trim()).filter(Boolean);
  return { default: "allow", deny: [...new Set([...DEFAULT_DENY, ...extra])] };
}

// Find the upstream binary: an explicit path, the package installed next to this launcher,
// then PATH. Spawning the native binary directly avoids npm's .cmd shims on Windows.
export function resolveBinary(here = path.dirname(fileURLToPath(import.meta.url))) {
  if (process.env.BROWSER_BACKEND_BIN) return process.env.BROWSER_BACKEND_BIN;
  const arch = process.arch === "x64" ? "x64" : process.arch;
  const suffix = process.platform === "win32" ? ".exe" : "";
  const platform = process.platform === "linux" && isMusl() ? "linux-musl" : process.platform;
  const file = `agent-browser-${platform}-${arch}${suffix}`;
  for (const root of [path.join(here, ".."), path.join(here, "..", "..")]) {
    const candidate = path.join(root, "node_modules", "agent-browser", "bin", file);
    if (fs.existsSync(candidate)) return candidate;
  }
  return "agent-browser";
}

function isMusl() {
  try {
    return !process.report.getReport().header.glibcVersionRuntime;
  } catch {
    return false;
  }
}

const NAMESPACE_PATTERN = /^xmesh-(\d+)-[0-9a-f]{8}$/;
const TMP_PATTERN = /^xmesh-ab-(\d+)-/;

function pidAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    // EPERM means the process exists but belongs to someone else.
    return error.code === "EPERM";
  }
}

// A launcher killed with SIGKILL (or a crashed machine) cannot clean up, so its daemon and Chrome
// stay until the daemon's idle timeout. Every launcher therefore sweeps the namespaces left by
// launchers that no longer exist, at start and once a minute. A namespace is only touched when
// its embedded launcher pid is dead; a reused pid leaves it alone until the idle timeout.
export function sweepStale({ binary, env, home = os.homedir(), tmp = process.env.BROWSER_BACKEND_TMP || os.tmpdir(), alive = pidAlive }) {
  const swept = [];
  const namespaces = path.join(home, ".agent-browser", "namespaces");
  let names = [];
  try {
    names = fs.readdirSync(namespaces);
  } catch {
    // No namespaces yet.
  }
  for (const name of names) {
    const match = NAMESPACE_PATTERN.exec(name);
    if (!match || alive(Number(match[1]))) continue;
    spawnSync(binary, ["close", "--all"], {
      env: { ...env, AGENT_BROWSER_NAMESPACE: name, AGENT_BROWSER_SESSION: name },
      stdio: "ignore", timeout: 8000, windowsHide: true,
    });
    fs.rmSync(path.join(namespaces, name), { recursive: true, force: true });
    swept.push(name);
  }
  try {
    for (const name of fs.readdirSync(tmp)) {
      const match = TMP_PATTERN.exec(name);
      if (match && !alive(Number(match[1]))) fs.rmSync(path.join(tmp, name), { recursive: true, force: true });
    }
  } catch {
    // Temporary directory not readable; nothing to sweep.
  }
  return swept;
}

function main() {
  const tools = parseArgs(process.argv.slice(2));
  const namespace = `xmesh-${process.pid}-${randomBytes(4).toString("hex")}`;
  const dir = fs.mkdtempSync(path.join(process.env.BROWSER_BACKEND_TMP || os.tmpdir(), `xmesh-ab-${process.pid}-`));
  fs.chmodSync(dir, 0o700);
  const policyFile = path.join(dir, "policy.json");
  fs.writeFileSync(policyFile, JSON.stringify(policyDocument()), { mode: 0o600 });
  const env = buildEnv(process.env, namespace, policyFile);
  const binary = resolveBinary();
  // The sweep environment carries no action policy: `close` must work even if a policy file is gone.
  const sweepEnv = { ...process.env };
  delete sweepEnv.AGENT_BROWSER_ACTION_POLICY;
  const sweep = () => {
    try {
      sweepStale({ binary, env: sweepEnv });
    } catch (error) {
      process.stderr.write(`xmesh-agent-browser: sweep failed: ${error.message}\n`);
    }
  };
  sweep();
  setInterval(sweep, 60_000).unref();

  const child = spawn(binary, ["mcp", "--tools", tools], { env, stdio: ["pipe", "pipe", "inherit"] });
  child.on("error", (error) => {
    process.stderr.write(`xmesh-agent-browser: cannot start ${binary}: ${error.message}\n`);
    cleanup();
    process.exit(127);
  });
  process.stdin.pipe(child.stdin);
  child.stdout.pipe(process.stdout);
  child.stdin.on("error", () => {});

  let cleaned = false;
  function cleanup() {
    if (cleaned) return;
    cleaned = true;
    // The daemon outlives the MCP process, so close its browser explicitly.
    spawnSync(binary, ["close", "--all"], { env, stdio: "ignore", timeout: 8000, windowsHide: true });
    for (const target of [dir, path.join(os.homedir(), ".agent-browser", "namespaces", namespace)]) {
      fs.rmSync(target, { recursive: true, force: true });
    }
  }

  process.stdin.on("end", () => child.stdin.end());
  child.on("exit", (code, signal) => {
    cleanup();
    process.exit(code ?? (signal ? 1 : 0));
  });
  for (const signal of ["SIGTERM", "SIGINT", "SIGHUP"]) {
    process.on(signal, () => {
      child.kill("SIGTERM");
      // Give the child a moment to exit on its own; its exit handler cleans up.
      setTimeout(() => {
        cleanup();
        process.exit(1);
      }, 3000).unref();
    });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
