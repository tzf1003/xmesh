#!/usr/bin/env node
// Install the agent-browser backend into a dedicated prefix.
//
//   node install.mjs --prefix /opt/xmesh-browser [--skip-chrome] [--check]
//
// Steps: copy package.json, the lockfile and the launcher into the prefix; `npm ci` with the
// pinned agent-browser version (install scripts are not run); download Chrome only when none is
// found (`agent-browser install`); print the xmesh configuration to apply. --check verifies an
// existing prefix without changing anything.
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { resolveBinary } from "./bin/xmesh-agent-browser.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));

function usage(code) {
  process.stderr.write("usage: node install.mjs --prefix DIR [--skip-chrome] [--check]\n");
  process.exit(code);
}

function npmCommand() {
  // Run npm through node so no shell is involved (a .cmd shim cannot be spawned directly on Windows).
  const candidates = [
    process.env.npm_execpath,
    path.join(path.dirname(process.execPath), "node_modules", "npm", "bin", "npm-cli.js"),
    path.join(path.dirname(process.execPath), "..", "lib", "node_modules", "npm", "bin", "npm-cli.js"),
  ].filter(Boolean);
  const cli = candidates.find((item) => item.endsWith("npm-cli.js") && fs.existsSync(item));
  if (!cli) throw new Error("npm-cli.js not found next to node; install npm or run `npm ci` in the prefix by hand");
  return [process.execPath, [cli]];
}

function run(command, args, options = {}) {
  const done = spawnSync(command, args, { stdio: "inherit", ...options });
  if (done.error) throw done.error;
  if (done.status !== 0) throw new Error(`${path.basename(command)} ${args.join(" ")} exited with ${done.status}`);
}

export function configSnippet(prefix, binary = process.execPath) {
  const launcher = path.join(prefix, "bin", "xmesh-agent-browser.mjs");
  return `agent:
  cwd_policy:
    allow: ["${path.join(prefix, "work").replaceAll("\\", "/")}"]
  backends:
    browser:
      transport: stdio
      command: [${JSON.stringify(binary)}, ${JSON.stringify(launcher)}, "--tools", "core,tabs"]
      cwd: ${path.join(prefix, "work").replaceAll("\\", "/")}
      policy: per-caller
      idle_timeout: 10m
      shutdown_timeout: 15s
      process_cleanup: tree
`;
}

function main() {
  const args = process.argv.slice(2);
  let prefix = "";
  let skipChrome = false;
  let check = false;
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--prefix") prefix = args[++i] ?? "";
    else if (args[i] === "--skip-chrome") skipChrome = true;
    else if (args[i] === "--check") check = true;
    else usage(2);
  }
  if (!prefix) usage(2);
  prefix = path.resolve(prefix);
  if (Number(process.versions.node.split(".")[0]) < 20) throw new Error("Node.js 20 or newer is required");

  if (!check) {
    fs.mkdirSync(path.join(prefix, "bin"), { recursive: true });
    fs.mkdirSync(path.join(prefix, "work"), { recursive: true, mode: 0o700 });
    for (const file of ["package.json", "package-lock.json"]) fs.copyFileSync(path.join(here, file), path.join(prefix, file));
    fs.copyFileSync(path.join(here, "bin", "xmesh-agent-browser.mjs"), path.join(prefix, "bin", "xmesh-agent-browser.mjs"));
    const [node, npm] = npmCommand();
    run(node, [...npm, "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--prefix", prefix]);
  }

  const binary = resolveBinary(path.join(prefix, "bin"));
  if (binary === "agent-browser" || !fs.existsSync(binary)) throw new Error(`agent-browser binary not found under ${prefix}`);
  // Install scripts are skipped, so the packaged native binary keeps the mode the tarball gave it.
  if (process.platform !== "win32") fs.chmodSync(binary, 0o755);
  const version = spawnSync(binary, ["--version"], { encoding: "utf8" });
  if (version.status !== 0) throw new Error(`agent-browser does not run: ${version.stderr || version.error}`);
  process.stdout.write(`agent-browser: ${version.stdout.trim()}\n`);

  const doctor = spawnSync(binary, ["doctor"], { encoding: "utf8", env: { ...process.env, HOME: os.homedir() } });
  const hasChrome = /pass\s+Google Chrome|pass\s+Chrome/i.test(doctor.stdout ?? "");
  if (!hasChrome && !skipChrome && !check) {
    process.stdout.write("Chrome not found; downloading with `agent-browser install`\n");
    run(binary, ["install"]);
  } else if (!hasChrome) {
    process.stdout.write("Chrome not found (skipped). Install it, or point AGENT_BROWSER_EXECUTABLE_PATH at an existing one.\n");
  }
  process.stdout.write(`\nAdd to xmesh.yaml (then set AGENT_BROWSER_ALLOWED_DOMAINS in the backend environment):\n\n${configSnippet(prefix)}`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (error) {
    process.stderr.write(`install: ${error.message}\n`);
    process.exit(1);
  }
}
