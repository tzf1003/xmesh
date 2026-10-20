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
  return quoteForTerminal(xmeshBinary(env,resourcesPath,platform),platform);
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

export interface CliRun {
  /** Exit code; 127 when the binary was not found. */
  code: number;
  stdout: string;
  stderr: string;
}

/** Like `runXmesh`, but never rejects: the caller reads the exit code and the `--json` answer itself. */
export function runXmeshRaw(command: CliCommand, node?: string, bin: string = xmeshBinary()): Promise<CliRun> {
  return runKernelCli(cliArgs(command, node), bin);
}

/** Main-process lifecycle commands; renderer requests never provide argv. */
export function runKernelCli(args: readonly string[], bin: string = xmeshBinary()): Promise<CliRun> {
  return new Promise((resolve) => {
    execFile(bin, [...args], { timeout: 120_000, windowsHide: true, encoding: "utf8" }, (err, stdout, stderr) => {
      if (!err) return resolve({ code: 0, stdout, stderr });
      const e = err as NodeJS.ErrnoException;
      resolve({ code: e.code === "ENOENT" ? 127 : typeof e.code === "number" ? e.code : 1, stdout, stderr: stderr || e.message });
    });
  });
}

export interface KernelServiceInfo { config_file: string; state_dir: string; oauth_password_file: string; local_only: boolean; system: boolean }

export async function kernelServiceInfo(paths: { configFile: string; serviceNode?: string },
  run: (args: readonly string[]) => Promise<CliRun> = runKernelCli): Promise<KernelServiceInfo | undefined> {
  const selector = paths.serviceNode ? ["--name", `xmesh-${paths.serviceNode}`] : ["--config", paths.configFile];
  const result = await run(["service-info", ...selector, "--json"]);
  let envelope: { error?: { reason?: string }; messages?: string[] };
  try { envelope = JSON.parse(result.stdout); } catch { throw new CliError("service-info", result.stderr || "无法读取原服务参数"); }
  if (result.code !== 0) {
    if (envelope.error?.reason === "not_installed") return undefined;
    throw new CliError("service-info", result.stderr || result.stdout);
  }
  const info = JSON.parse(envelope.messages?.[0] ?? "null") as Partial<KernelServiceInfo> | null;
  if (typeof info?.config_file !== "string" || !path.isAbsolute(info.config_file) ||
    typeof info.state_dir !== "string" || (info.state_dir !== "" && !path.isAbsolute(info.state_dir)) ||
    typeof info.oauth_password_file !== "string" || (info.oauth_password_file !== "" && !path.isAbsolute(info.oauth_password_file)) ||
    typeof info.local_only !== "boolean" || typeof info.system !== "boolean") throw new CliError("service-info", "原服务的配置、状态目录或安装参数无效");
  return info as KernelServiceInfo;
}

/** Retain non-configuration service arguments before a registration is removed. */
export async function kernelServiceOptions(paths: { configFile: string; stateDir: string; serviceNode?: string },
  run: (args: readonly string[]) => Promise<CliRun> = runKernelCli): Promise<string[]> {
  const info = await kernelServiceInfo(paths, run);
  if (!info) return [];
  if (info.system || path.resolve(info.config_file) !== path.resolve(paths.configFile) ||
    (info.state_dir !== "" && path.resolve(info.state_dir) !== path.resolve(paths.stateDir))) throw new CliError("service-info", "原服务的配置、状态目录或权限不匹配");
  return [...(info.local_only ? ["--local-only"] : []), ...(info.oauth_password_file ? ["--oauth-password-file", info.oauth_password_file] : [])];
}

/** First-run subcommands. Unlike `CliCommand` these take arguments, built here from validated values only. */
export type SetupCliArgs = readonly ["install", "--config", string] | readonly ["install", "--config", string, "--state-dir", string] | readonly ["start", "--config", string] | readonly ["migrate","--config",string,"--state-dir",string] | readonly ["migrate", "--name", string];

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
