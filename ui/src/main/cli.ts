import { execFile } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import { defaultLookup, resolveXmeshBinary } from "./system/binary";

/** The only `xmesh` subcommands the UI ever runs. K1 adds them alongside `--json` output. */
export const CLI_COMMANDS = ["restart", "start", "stop"] as const;
export type CliCommand = (typeof CLI_COMMANDS)[number];

/** Where the installer puts the kernel it ships with: `<resources>/bin/xmesh[.exe]` (electron-builder extraResources). */
export function bundledXmesh(resourcesPath: string | undefined, platform: NodeJS.Platform = process.platform): string | undefined {
  if (!resourcesPath) return undefined;
  const file = path.join(resourcesPath, "bin", platform === "win32" ? "xmesh.exe" : "xmesh");
  return fs.existsSync(file) ? file : undefined;
}

/**
 * `XMESH_BIN` wins, then the kernel bundled with the installed app, then `PATH`
 * and the usual install directories (a login item starts with a minimal PATH).
 */
export function xmeshBinary(
  env: NodeJS.ProcessEnv = process.env,
  resourcesPath: string | undefined = (process as { resourcesPath?: string }).resourcesPath,
  platform: NodeJS.Platform = process.platform,
): string {
  return env.XMESH_BIN || bundledXmesh(resourcesPath, platform) || resolveXmeshBinary({ ...defaultLookup(), env, platform });
}

/**
 * Writes a binary path so a terminal runs it as written. Windows terminals default to PowerShell, which
 * needs `&` before a quoted path; POSIX shells take a plain path or single quotes.
 */
export function quoteForTerminal(file: string, platform: NodeJS.Platform = process.platform): string {
  if (platform === "win32") return `& '${file.replace(/'/g, "''")}'`;
  return /^[A-Za-z0-9_@%+=:,./-]+$/.test(file) ? file : `'${file.replace(/'/g, "'\\''")}'`;
}

/**
 * The CLI as the user should type it in a terminal, naming the binary the app runs: `XMESH_BIN`, then the
 * kernel bundled with the app (inside the app bundle, so not on PATH), else the bare name PATH provides.
 */
export function cliCommandText(
  env: NodeJS.ProcessEnv = process.env,
  resourcesPath: string | undefined = (process as { resourcesPath?: string }).resourcesPath,
  platform: NodeJS.Platform = process.platform,
): string {
  const bin = env.XMESH_BIN || bundledXmesh(resourcesPath, platform);
  return bin ? quoteForTerminal(bin, platform) : "xmesh";
}

export class CliError extends Error {
  constructor(
    readonly command: string,
    message: string,
  ) {
    super(message);
    this.name = "CliError";
  }
}

/**
 * Arguments for `xmesh <command> [--name xmesh-<node>] --json`. Without `--name` the CLI reads
 * `./xmesh.yaml`, which a tray app does not have, so the service is named whenever it is known.
 */
export function cliArgs(command: CliCommand, node?: string): string[] {
  const service = node && /^[A-Za-z0-9._-]+$/.test(node) ? ["--name", `xmesh-${node}`] : [];
  return [command, ...service, "--json"];
}

/** Runs `xmesh <command> --json` without a shell and resolves on exit code 0. */
export function runXmesh(command: CliCommand, node?: string, bin: string = xmeshBinary()): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(bin, cliArgs(command, node), { timeout: 120_000, windowsHide: true }, (err, stdout, stderr) => {
      if (err) reject(new CliError(command, String(stderr || err.message).trim()));
      else resolve(stdout);
    });
  });
}

/** First-run subcommands. Unlike `CliCommand` these take arguments, built here from validated values only. */
export type SetupCliArgs = readonly ["install", "--config", string] | readonly ["start", "--config", string];

/**
 * Runs `xmesh install|start --config <file>` without a shell and returns a
 * single line for people: the first line of stdout on success, of stderr on
 * failure. A missing binary is reported as such.
 */
export function runXmeshSetup(args: SetupCliArgs, bin: string = xmeshBinary()): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(bin, [...args], { timeout: 120_000, windowsHide: true }, (err, stdout, stderr) => {
      const line = (s: string): string => s.trim().split(/\r?\n/)[0]?.slice(0, 300) ?? "";
      if (!err) return resolve(line(stdout));
      const missing = (err as NodeJS.ErrnoException).code === "ENOENT";
      reject(new CliError(args[0], missing ? `找不到 xmesh 可执行文件：${bin}` : line(String(stderr)) || err.message));
    });
  });
}
